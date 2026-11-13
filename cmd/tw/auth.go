package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"html"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kksty/tianwu/internal/api"
	"github.com/kksty/tianwu/internal/collab"
)

// Console authentication: one operator token, one signed cookie, one small login
// page. It exists because a control plane that can dispatch offensive work must
// not be reachable by anyone who finds the port; it is deliberately the smallest
// thing that is actually a gate (no user table, no password reset, no sessions
// store — the cookie is self-contained and expires).
//
// Harness sessions (commander and executors) get a second, derived credential:
// the same control plane, but only the battlefield surface exists for it —
// everything else (settings, projects, reports, review triggers, worker controls,
// imports, bundles, …) is absent (D109). The operator token never enters the
// host environment those sessions run in.
type consoleAuth struct {
	// key is the operator token's hash; agentKey is the derived agent token's
	// hash. Both are compared in constant time.
	key      []byte
	agentKey []byte
	ttl      time.Duration
	limiter  *loginLimiter
}

// consoleRole is what an authenticated credential may reach.
type consoleRole int

const (
	// roleOperator is the human console: everything, including reports/reviews.
	roleOperator consoleRole = iota
	// roleAgent is a harness session (commander or executor): the battlefield
	// surface only, so it focuses on the engagement instead of the operator's
	// controls.
	roleAgent
)

const (
	consoleCookie = "tw_session"
	consoleTTL    = 12 * time.Hour

	// loginMaxFailures failures per IP inside loginWindow park that IP for the
	// rest of the window. A strong token cannot be guessed; this exists so the
	// try loop is not free: it costs CPU, fills the log, and turns a typo into
	// an oracle.
	loginMaxFailures = 8
	loginWindow      = 10 * time.Minute
)

// loginLimiter is the in-memory failure window for one console. A restart clears
// it, which is fine: the token remains the gate, this only bounds the attempts.
type loginLimiter struct {
	mu     sync.Mutex
	window time.Duration
	max    int
	fails  map[string][]time.Time
}

func newLoginLimiter(max int, window time.Duration) *loginLimiter {
	if max <= 0 {
		max = loginMaxFailures
	}
	if window <= 0 {
		window = loginWindow
	}
	return &loginLimiter{window: window, max: max, fails: make(map[string][]time.Time)}
}

// retryAfter reports how long the IP must wait. Zero means "not blocked". It
// also prunes the failures that have aged out, so no cleanup goroutine is needed.
func (l *loginLimiter) retryAfter(ip string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-l.window)
	kept := l.fails[ip][:0]
	for _, at := range l.fails[ip] {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	if len(kept) == 0 {
		delete(l.fails, ip)
		return 0
	}
	l.fails[ip] = kept
	if len(kept) < l.max {
		return 0
	}
	return kept[0].Add(l.window).Sub(now)
}

func (l *loginLimiter) fail(ip string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fails[ip] = append(l.fails[ip], now)
}

func (l *loginLimiter) reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, ip)
}

// clientIP is the peer address. X-Forwarded-For is deliberately not trusted:
// behind a reverse proxy it would let any client pick its own bucket, and an
// operator who does run a proxy can still rate-limit there.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// throttled writes the 429 shared by the login form and the API gate. The
// Retry-After header keeps scripts honest without a second round trip.
func (a *consoleAuth) throttled(w http.ResponseWriter, retry time.Duration, wantsJSON bool) {
	seconds := int(retry.Seconds() + 0.5)
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	if wantsJSON {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"尝试过多，请稍后再试"}`))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusTooManyRequests)
	_, _ = w.Write([]byte(renderLogin("", `<div class="err">尝试过多，请稍后再试</div>`)))
}

// newConsoleAuth returns nil when no token is configured: local development
// stays open, and the caller logs that fact loudly.
func newConsoleAuth(token string) *consoleAuth {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	sum := sha256.Sum256([]byte(token))
	agent := sha256.Sum256([]byte(agentAPIToken(token)))
	return &consoleAuth{
		key:      sum[:],
		agentKey: agent[:],
		ttl:      consoleTTL,
		limiter:  newLoginLimiter(loginMaxFailures, loginWindow),
	}
}

func (a *consoleAuth) issue(w http.ResponseWriter) {
	exp := time.Now().Add(a.ttl).Unix()
	value := fmt.Sprintf("%d.%s", exp, a.sign(exp))
	http.SetCookie(w, &http.Cookie{
		Name:     consoleCookie,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(a.ttl.Seconds()),
	})
}

func (a *consoleAuth) sign(exp int64) string {
	mac := hmac.New(sha256.New, a.key)
	fmt.Fprintf(mac, "tw-session:%d", exp)
	return hex.EncodeToString(mac.Sum(nil))
}

// authorize checks the credential without touching any server state. A bearer
// token is accepted too: scripts and curl should not have to log in through the
// HTML form, and the harness ecosystem already speaks Authorization: Bearer.
// Two bearers are understood: the operator token (full API) and the derived
// agent token (battlefield surface only, D109).
func (a *consoleAuth) authorize(r *http.Request) (consoleRole, bool) {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		got := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
		sum := sha256.Sum256([]byte(got))
		agent := subtle.ConstantTimeCompare(sum[:], a.agentKey) == 1
		operator := subtle.ConstantTimeCompare(sum[:], a.key) == 1
		switch {
		case operator:
			return roleOperator, true
		case agent:
			return roleAgent, true
		}
	}
	c, err := r.Cookie(consoleCookie)
	if err != nil {
		return roleOperator, false
	}
	parts := strings.SplitN(c.Value, ".", 2)
	if len(parts) != 2 {
		return roleOperator, false
	}
	exp, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return roleOperator, false
	}
	want := a.sign(exp)
	// Only the operator's browser gets a cookie; harness sessions talk to the
	// API with their bearer token and cannot mint one.
	if subtle.ConstantTimeCompare([]byte(parts[1]), []byte(want)) == 1 {
		return roleOperator, true
	}
	return roleOperator, false
}

// agentAPIToken derives the harness sessions' API token from the operator token.
// Derivation keeps one secret on disk and one on the command line; whoever holds
// the operator token already has the full API, so this is not a second secret to
// manage. The value itself is inert: it only matches the agent role.
func agentAPIToken(consoleToken string) string {
	mac := hmac.New(sha256.New, []byte(strings.TrimSpace(consoleToken)))
	mac.Write([]byte("tw-agent-api"))
	return hex.EncodeToString(mac.Sum(nil))
}

// hostEnvWithAgentToken is the environment the local harness host starts with:
// the model's own variables plus the scoped agent token. The operator console
// token is deliberately NOT in it — every harness session inherits the host
// environment, and a session that could read the operator token would bypass the
// battlefield-only surface (D109). Callers also drop an inherited
// TW_CONSOLE_TOKEN from the child env; see host.Config.DropEnvPrefixes.
func hostEnvWithAgentToken(modelEnv []string, consoleToken string) []string {
	out := append([]string{}, modelEnv...)
	if t := strings.TrimSpace(consoleToken); t != "" {
		out = append(out, "TW_AGENT_TOKEN="+agentAPIToken(t))
	}
	// Windows toolchains default to the ANSI code page (cp936 on the hosts this
	// console runs on): a Python remediation script that opens a UTF-8 JSON file
	// without an explicit encoding decodes as GBK and dies on the first Chinese
	// byte. Python's UTF-8 mode is the supported switch, and every harness
	// session inherits it, so a run does not rediscover this per command. An
	// explicit value from the operator's model env wins.
	out = withDefaultEnv(out, "PYTHONUTF8", "1")
	out = withDefaultEnv(out, "PYTHONIOENCODING", "utf-8")
	return out
}

// withDefaultEnv appends key=value unless the caller already set that key.
func withDefaultEnv(env []string, key, value string) []string {
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return env
		}
	}
	return append(env, prefix+value)
}

// agentRouteHidden reports whether a route is absent from the agent's API view.
// A harness session (commander or executor) sees only the battlefield: board,
// intents, facts, evidence, goals, playbooks, runs, messages, participants,
// state, retrieval/context, the knowledge base and the model tiers it may pick.
// Everything else — settings, projects, reports, review triggers, worker controls, imports,
// bundles, fleet/version, the decider/project switches — is operator-only and
// returns 404, so the agent neither sees it nor learns it exists (D109).
//
// 404, not 403: a refusal would confirm that a hidden surface exists, which is
// exactly the learning the boundary prevents (D102). This is a surface boundary
// for a trusted internal session, not a sandbox (D78).
func agentRouteHidden(path string) bool {
	// World publication/export is instance content management. Project sessions
	// nominate through their own authenticated API, not the operator's routes.
	if strings.HasPrefix(path, "/api/kb/") {
		return true
	}
	i := strings.Index(path, "/api/")
	if i < 0 {
		return false
	}
	rest := path[i+len("/api/"):]
	// Manual world-knowledge nomination and experience archiving are operator curation.
	if parts := strings.Split(rest, "/"); len(parts) == 3 && parts[0] == "playbooks" && (parts[2] == "kb-candidate" || parts[2] == "archive") {
		return true
	}
	if strings.HasSuffix(rest, "/operator/complete") {
		return true
	}
	seg := rest
	if strings.HasPrefix(rest, "traffic/") {
		return rest != "traffic/flows" && rest != "traffic/flow"
	}
	if j := strings.IndexByte(rest, '/'); j >= 0 {
		seg = rest[:j]
	}
	switch seg {
	case "instructions":
		return false // Read and ACK are available; operator writes are gated by method below.
	case "changes", "context", "experience", "focus", "resources", "communication":
		return false
	case "graph":
		return rest != "graph" // Full-project export remains an operator action.
	case "retrieval":
		return rest != "retrieval/status" && rest != "retrieval/index"
	case "model-profiles":
		return rest != "model-profiles" // Sessions choose bindings; operators manage them.
	case "board", "intents", "facts", "evidence", "goals", "playbooks",
		"runs", "messages", "participants", "state", "kb", "access", "artifacts", "operator-requests", "actions":
		// A fact carries its review history inside the fact body (readable), but
		// starting a review is an operator click: hide only the trigger routes.
		if seg != "facts" {
			return false
		}
		if rest == "facts/verify-pending" {
			return true
		}
		parts := strings.Split(rest, "/")
		return len(parts) == 3 && (parts[2] == "verify" || parts[2] == "review")
	default:
		return true
	}
}

// wrap gates every route except the login page, its assets and the liveness
// probe. API clients get a 401 they can read; browsers get the login form.
func (a *consoleAuth) wrap(next http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", a.loginPage)
	mux.HandleFunc("POST /login", a.loginSubmit)
	mux.HandleFunc("GET /logout", a.logout)
	// /healthz belongs to the instance, not to the login gate: a probe must not
	// depend on whether a token happens to be configured.
	mux.Handle("/healthz", next)
	mux.Handle("/static/", next)
	// Participant calls authenticate with a per-run collab token, not the
	// operator's console token. The hub validates that token and the project
	// binding before any handler runs; passing the path through here is not an
	// authentication decision.
	mux.Handle("/agent/", next)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if role, ok := a.authorize(r); ok {
			a.limiter.reset(clientIP(r))
			if role == roleAgent {
				instructionWrite := strings.Contains(r.URL.Path, "/api/instructions") && r.Method != http.MethodGet && !(r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/ack"))
				profileWrite := strings.HasSuffix(r.URL.Path, "/api/model-profiles") && r.Method != http.MethodGet
				// Goals are operator-defined: sessions may read them (they judge
				// completion) and close one with complete/reopen, but they cannot
				// create or rewrite the endpoint. The operator's manual "met" pin
				// is a separate write: it stamps a goal by hand without the
				// ledger's citation rules, so it stays operator-only too.
				goalWrite := strings.HasSuffix(r.URL.Path, "/api/goals") && r.Method != http.MethodGet
				goalPin := strings.Contains(r.URL.Path, "/api/goals/") && strings.HasSuffix(r.URL.Path, "/met") && r.Method != http.MethodGet
				if agentRouteHidden(r.URL.Path) || instructionWrite || profileWrite || goalWrite || goalPin || (strings.Contains(r.URL.Path, "/api/access") && r.Method != http.MethodGet) || (strings.Contains(r.URL.Path, "/api/playbooks/") && r.Method == http.MethodDelete) {
					// Operator-only surfaces are absent for harness sessions, not
					// merely forbidden: a 403 would confirm they exist, which is
					// the learning the boundary prevents (D102/D109).
					http.NotFound(w, r)
					return
				}
				// The agent token is a harness session's credential, and this
				// branch guards the commander's channel: executors authenticate
				// their own per-run identity on /agent/ above, not here. Writes
				// through the project mux record their author from
				// X-Tw-Actor, so the attribution is stamped from the credential
				// instead of relying on the model to volunteer a header it can
				// forget — a say without the header used to land as `operator`.
				// The header stays a claim about the speaker, not a second
				// permission (D86); the surface boundary above is unchanged.
				r.Header.Set("X-Tw-Actor", collab.CommanderID)
				// The project mux has no per-run identity for the commander (it
				// does not go through /agent/), so mark the request here: the API
				// handlers use it to keep operator-side support runs and the
				// review queue out of the session's view.
				r = r.WithContext(api.WithAgentSurface(r.Context()))
			}
			next.ServeHTTP(w, r)
			return
		}
		// Only an actual credential attempt counts as a failure. A browser that
		// has not logged in yet sends neither header nor cookie, and counting
		// those would lock out a user whose cookie merely expired.
		if attempt := strings.HasPrefix(strings.ToLower(r.Header.Get("Authorization")), "bearer "); attempt {
			ip := clientIP(r)
			if retry := a.limiter.retryAfter(ip, time.Now()); retry > 0 {
				a.throttled(w, retry, wantsJSON(r))
				return
			}
			a.limiter.fail(ip, time.Now())
		} else if c, err := r.Cookie(consoleCookie); err == nil && c.Value != "" {
			ip := clientIP(r)
			if retry := a.limiter.retryAfter(ip, time.Now()); retry > 0 {
				a.throttled(w, retry, wantsJSON(r))
				return
			}
			a.limiter.fail(ip, time.Now())
		}
		if wantsJSON(r) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"需要登录：打开 /login 输入控制台令牌"}`))
			return
		}
		target := "/login"
		if r.Method == http.MethodGet && r.URL.Path != "/" {
			target += "?next=" + urlEscape(r.URL.RequestURI())
		}
		http.Redirect(w, r, target, http.StatusFound)
	})
	return mux
}

func wantsJSON(r *http.Request) bool {
	if strings.HasPrefix(r.URL.Path, "/api/") || strings.Contains(r.URL.Path, "/api/") {
		return true
	}
	return strings.Contains(r.Header.Get("Accept"), "application/json")
}

func (a *consoleAuth) loginPage(w http.ResponseWriter, r *http.Request) {
	next := safeNext(r.URL.Query().Get("next"))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(renderLogin(next, "")))
}

func (a *consoleAuth) loginSubmit(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if retry := a.limiter.retryAfter(ip, time.Now()); retry > 0 {
		a.throttled(w, retry, false)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "表单读不出来", http.StatusBadRequest)
		return
	}
	next := safeNext(r.FormValue("next"))
	got := strings.TrimSpace(r.FormValue("token"))
	want := string(a.key)
	// The cookie key is a hash of the token; compare the hash of what was typed
	// against it, so the plaintext token never has to be kept around.
	sum := sha256.Sum256([]byte(got))
	if subtle.ConstantTimeCompare(sum[:], []byte(want)) != 1 {
		a.limiter.fail(ip, time.Now())
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(renderLogin(next, `<div class="err">令牌不对</div>`)))
		return
	}
	a.limiter.reset(ip)
	a.issue(w)
	http.Redirect(w, r, next, http.StatusFound)
}

func (a *consoleAuth) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: consoleCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	http.Redirect(w, r, "/login", http.StatusFound)
}

// safeNext accepts only an absolute path on this console. Anything else (an
// absolute URL, a scheme-relative //host, a control byte that could split the
// Location header) becomes "/". The value is still HTML-escaped at render time:
// this check is about the redirect, not about the form.
func safeNext(next string) string {
	if next == "" || strings.HasPrefix(next, "//") {
		return "/"
	}
	if strings.ContainsAny(next, "\\\r\n\x00") {
		return "/"
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || !strings.HasPrefix(u.Path, "/") {
		return "/"
	}
	return next
}

func urlEscape(s string) string {
	return strings.NewReplacer("%", "%25", " ", "%20", "?", "%3F", "&", "%26", "#", "%23").Replace(s)
}

// withSecurityHeaders wraps the whole console, including the gate: a 401, a 429
// and the login page get the same headers as the app. No CSP: the login page and
// the SPA both rely on inline styles, and a policy loose enough for them would
// not buy much.
func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
		next.ServeHTTP(w, r)
	})
}

// withConsoleAuth wraps the hub. There is no "open" mode: a control plane that
// dispatches offensive work is never unauthenticated.
func withConsoleAuth(next http.Handler, token string) http.Handler {
	auth := newConsoleAuth(token)
	if auth == nil {
		log.Fatal("控制台令牌为空：拒绝以未授权方式启动")
	}
	log.Printf("控制台登录：已启用（令牌 %d 字节，会话 %s）", len(token), consoleTTL)
	return auth.wrap(next)
}

// ensureConsoleToken resolves the token from the flag/file/env, generating and
// persisting one when the operator did not supply any. The file is the source a
// restart reads, so the login survives a restart without repeating the flag.
func ensureConsoleToken(dataDir, inline, file string) (token, path string, generated bool, err error) {
	t, err := resolveConsoleToken(inline, file)
	if err != nil {
		return "", "", false, err
	}
	if t != "" {
		return t, "", false, nil
	}
	// The generated token is the instance's identity: reuse it, or every
	// restart logs the operator out and invalidates copied URLs.
	path = filepath.Join(dataDir, "console.token")
	if b, err := os.ReadFile(path); err == nil {
		if v := strings.TrimSpace(string(b)); v != "" {
			return v, path, false, nil
		}
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", "", false, err
	}
	t = hex.EncodeToString(buf)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return "", "", false, err
	}
	if err := os.WriteFile(path, []byte(t+"\n"), 0o600); err != nil {
		return "", "", false, err
	}
	return t, path, true, nil
}

// resolveConsoleToken reads --console-token, then --console-token-file, then
// TW_CONSOLE_TOKEN. The file variant exists so the secret does not show up in a
// process listing or a shell history.
func resolveConsoleToken(inline, file string) (string, error) {
	if v := strings.TrimSpace(inline); v != "" {
		return v, nil
	}
	if file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(data)), nil
	}
	return strings.TrimSpace(os.Getenv("TW_CONSOLE_TOKEN")), nil
}

const loginHTML = `<!doctype html>
<html lang="zh-CN" class="dark"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="theme-color" content="#0a0a0b">
<title>天吴 · 登录</title>
<link rel="icon" type="image/svg+xml" href="/static/favicon.svg">
<style>
:root{--bg:#0a0a0b;--card:#121215;--border:#26262b;--fg:#e9e9ee;--muted:#8b8b93;--accent:#ff4d4d}
*{box-sizing:border-box}
body{margin:0;height:100vh;display:grid;place-items:center;background:
radial-gradient(700px 400px at 30% -10%,rgba(255,77,77,.08),transparent),var(--bg);
color:var(--fg);font:14px/1.65 "Inter","PingFang SC","Microsoft YaHei",system-ui,sans-serif}
.card{width:340px;border:1px solid var(--border);border-radius:10px;background:var(--card);padding:22px}
.brand{display:flex;flex-direction:column;gap:6px;margin-bottom:20px}
.wordmark{display:flex;align-items:flex-start;gap:3px}
.name{font-family:"Kaiti SC","STKaiti","KaiTi","Noto Serif SC","Songti SC",serif;
font-size:26px;font-weight:700;line-height:1;letter-spacing:.12em;
background:linear-gradient(180deg,#fff8f2 0%,#ffd2c2 55%,#ff9b84 100%);
-webkit-background-clip:text;background-clip:text;color:transparent;
filter:drop-shadow(0 1px 0 rgba(110,8,8,.5))}
.dot{width:6px;height:6px;margin-top:5px;border-radius:1px;background:var(--accent);
box-shadow:0 0 8px rgba(255,77,77,.7)}
.sub{color:var(--muted);font-size:7px;font-weight:600;letter-spacing:.36em;text-transform:uppercase}
label{display:block;color:var(--muted);font-size:11px;margin-bottom:6px}
input{width:100%;height:34px;padding:0 10px;border-radius:6px;border:1px solid var(--border);
background:#0d0d10;color:var(--fg);font:inherit;font-family:ui-monospace,monospace}
input:focus{outline:1px solid var(--accent);border-color:var(--accent)}
button{margin-top:14px;width:100%;height:34px;border:0;border-radius:6px;background:var(--accent);
color:#170303;font:inherit;font-weight:700;cursor:pointer}
.err{color:var(--accent);font-size:11px;margin-top:10px}
.hint{color:var(--muted);font-size:10px;margin-top:12px}
</style></head><body>
<form class="card" method="post" action="/login">
  <div class="brand">
    <div class="wordmark"><span class="name">天吴</span><span class="dot" aria-hidden="true"></span></div>
    <div class="sub">TIANWU</div>
  </div>
  <input type="hidden" name="next" value="{{next}}">
  <label for="token">控制台令牌</label>
  <input id="token" name="token" type="password" autofocus autocomplete="current-password"
         placeholder="启动时 --console-token 的值">
  <button type="submit">进入</button>
  {{err}}
  <div class="hint">令牌在启动日志里（自动生成时会同时写到 &lt;data&gt;/console.token）；会话 12 小时，退出访问 /logout。</div>
</form></body></html>`

// renderLogin fills the two placeholders; formatting the page with Printf would
// mean escaping every percent in the CSS, which is how the first version broke.
//
// next is operator input from the URL/form and lands inside a value="…"
// attribute, so it is HTML-escaped. Without this a crafted /login?next= URL ran
// script on the console origin, where the session cookie already lives.
func renderLogin(next, errBlock string) string {
	return strings.NewReplacer(
		"{{next}}", html.EscapeString(next),
		"{{err}}", errBlock,
	).Replace(loginHTML)
}
