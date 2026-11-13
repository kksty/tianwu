// Package host starts and supervises the local harness host (`aiscan web`).
//
// Every execution node is an AOP host. The control-plane machine is not an
// exception: instead of spawning a one-shot `aiscan-full agent` per run, tw
// starts one `aiscan web` process and registers it as the node "local". That
// gives the local machine exactly what a remote node has — sessions, steer,
// verification, report drafts, a shell and a REPL — through one transport
// (docs/14-nodes.md, D64).
//
// The host is a child of tw, not a daemon: it inherits the instance's model
// configuration, its working directory is the data directory (an agent's tools
// only resolve paths below it), and it dies with the control plane.
package host

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/kksty/tianwu/internal/aop"
)

const (
	// readyTimeout bounds how long Start waits for the embedded agent to answer
	// ListAgents. The host loads fingerprints and templates before it serves,
	// which takes a few seconds on a cold disk; its own provider activation probes
	// can each take the full request budget on a slow gateway (the commander
	// profile is a shared "smart split" route measured at 6-25s for the first
	// response). The window therefore has to cover two of them, or a working but
	// slow endpoint would be killed mid-activation and reported as unhealthy.
	readyTimeout = 2 * time.Minute
	readyPoll    = 250 * time.Millisecond
	// maxReadyFailures is how many consecutive launch/readiness failures the
	// supervisor tolerates before it marks the host unhealthy and stops
	// restarting. A host whose model configuration is broken (wrong key, bad
	// base_url) would otherwise be restarted forever: the watchdog's job is to
	// keep the process alive, not to retry a credential that cannot work until an
	// operator fixes it (docs/17 §3.6; watchdog-liveness §1.1/§3.2).
	maxReadyFailures = 3
	// restartBackoff is the first restart delay; it doubles up to restartBackoffCap.
	restartBackoff    = time.Second
	restartBackoffCap = 30 * time.Second
	// LocalAgent is the node id of the embedded agent `aiscan web` starts.
	LocalAgent = "local"
)

// Config describes the host tw wants.
type Config struct {
	// Bin is the harness binary (`aiscan-full`); it must exist.
	Bin string
	// Dir is the child's working directory. An agent's file tools only accept
	// paths below it, so this must be the data directory: the case
	// directories live under <data>/projects/<id>/cases.
	Dir string
	// Addr is the preferred listen address. When empty or taken, a free
	// loopback port is picked.
	Addr string
	// DB is the host's session database. Empty means <Dir>/host/cyber-web.db.
	// A profile host needs its own file: two `aiscan web` processes sharing one
	// SQLite database would overwrite each other's sessions.
	DB string
	// Token is the host's access key. Empty generates one for this process.
	Token string
	// Env is appended to the child environment (CYBER_PROVIDER, …).
	Env []string
	// DropEnvPrefixes removes inherited variables with these prefixes before
	// Env is applied. A profile host uses it for CYBER_*: the parent process
	// may carry the default deployment's model, and a variable it never set is
	// the only way to leave a field unset.
	DropEnvPrefixes []string
	// Args are extra flags for the `web` command (--context-window, …).
	Args []string
	// ExcludeTools is the harness's CYBER_EXCLUDE_TOOLS value for this host
	// (for example "subagent"). Empty leaves the variable alone. Tianwu sets it
	// by default so the harness does not expose delegation tools that create
	// sessions outside the ledger (docs/17 §3.2; `--allow-native-subagent` is
	// the operator's escape hatch).
	ExcludeTools string
	// Persist writes <Dir>/host/local.json (endpoint, PID, …) whenever the host
	// becomes ready and removes it on a clean stop. It is for the one local host
	// only: a profile host is rebuilt on every revision and must not overwrite
	// the instance's record.
	Persist bool
	// ReadyTimeout overrides readyTimeout for one readiness attempt. 0 uses the
	// package default; tests set a short one so the failure path is observable.
	ReadyTimeout time.Duration
	// RetryBackoff overrides restartBackoff. 0 uses the package default.
	RetryBackoff time.Duration
	// MaxReadyFailures overrides maxReadyFailures. 0 uses the package default.
	MaxReadyFailures int
	// RequireProvider makes readiness mean "the built-in agent has a bound
	// provider/model", not just "an agent exists". Without it a host whose
	// provider initialization timed out reports ready and then every run fails
	// in milliseconds with "agent provider is nil". Only enable it when the
	// control plane has a model configured; a console-only deployment without a
	// model legitimately has no provider.
	RequireProvider bool
	// AllowNotReady turns "readiness never succeeded" into a state the caller
	// keeps instead of an error: Start returns the Host together with
	// ErrNotReady. A reachable agent with ConfigError keeps its process and
	// supervisor for terminal/native tools; an unreachable agent stops after
	// bounded retries. A settings save retries either state (D101).
	AllowNotReady bool
	// Logf receives the host's output lines and lifecycle messages.
	Logf func(format string, args ...any)
}

// ErrNotReady is returned by Start, together with a non-nil Host, when
// AllowNotReady let a host that never became ready survive. The console keeps
// running in degraded mode: the API and settings still work, dispatch is
// refused by the caller, and a settings save revives the host (D101).
var ErrNotReady = errors.New("本机宿主未就绪")

// HostStatus is the supervisor's view of the host, for the nodes API and the
// console guard panel. Online means the agent/working directory were verified;
// Ready additionally means its model can accept dispatch.
type HostStatus struct {
	Endpoint    string `json:"endpoint"`
	PID         int    `json:"pid"`
	Online      bool   `json:"online"`
	Ready       bool   `json:"ready"`
	Restarts    int    `json:"restarts"`
	FailedReady int    `json:"failed_ready"`
	Unhealthy   bool   `json:"unhealthy"`
	LastError   string `json:"last_error,omitempty"`
}

// process is one supervised child. The fields are functions so the liveness
// tests can drive crash/restart without launching a binary; production wraps
// *exec.Cmd.
type process struct {
	pid  int
	wait func() error
	kill func()
}

func (p *process) PID() int { return p.pid }

// launchProcess and dialHost are the package's two test seams: production
// starts the real binary and talks to it over HTTP.
var (
	launchProcess = func(h *Host) (*process, error) { return h.execLaunch() }
	dialHost      = func(addr, token string) agentLister { return aop.New(addr, token) }
)

// agentLister is the one AOP management call readiness needs.
type agentLister interface {
	Agents(ctx context.Context) ([]aop.Agent, error)
}

// Host is one supervised `aiscan web` process.
type Host struct {
	cfg   Config
	addr  string
	token string
	db    string

	mu       sync.Mutex
	cmd      *process
	stopping bool
	stopped  chan struct{}
	restarts int
	// failedReady counts consecutive launches/readiness checks that failed. It
	// resets to 0 on every ready host, so three isolated crashes over a day do
	// not add up to a permanent stop.
	failedReady int
	unhealthy   bool
	lastErr     string
	launchedAt  time.Time
	started     time.Time
	readyOnce   bool
	online      bool
	running     bool
	onReady     []func(aop.Agent)
	// ctx is the instance lifetime, kept so a settings edit can revive a
	// supervisor that stopped after going unhealthy.
	ctx context.Context

	tail *tailBuffer
}

// readyAgent picks the local agent out of ListAgents and checks the fields that
// make it usable.
//
// The count alone is not enough: a `--no-agent` host reports none, and a host
// that grabbed the port with a different agent reports one that is not ours.
// Provider/Model are required only when RequireProvider is set: a provider
// whose initialization timed out still answers ListAgents, and calling that
// "ready" is exactly the state where every subsequent run fails instantly.
func readyAgent(agents []aop.Agent, requireProvider bool) (aop.Agent, bool) {
	a, ok := aop.Find(agents, LocalAgent)
	if !ok {
		return aop.Agent{}, false
	}
	if strings.TrimSpace(a.WorkingDir) == "" {
		return aop.Agent{}, false
	}
	if a.ConfigError != "" || requireProvider && (strings.TrimSpace(a.Provider) == "" || strings.TrimSpace(a.Model) == "") {
		return aop.Agent{}, false
	}
	return a, true
}

// sameWorkingDir compares two paths the way the OS does: case-insensitive on
// Windows, cleaned on both. A host started from another data directory is a
// different instance's host and must not be adopted.
func sameWorkingDir(a, b string) bool {
	a, b = filepath.Clean(strings.TrimSpace(a)), filepath.Clean(strings.TrimSpace(b))
	if a == "" || b == "" || a == "." || b == "." {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func (h *Host) readyTimeout() time.Duration {
	if h.cfg.ReadyTimeout > 0 {
		return h.cfg.ReadyTimeout
	}
	return readyTimeout
}

func (h *Host) initialBackoff() time.Duration {
	if h.cfg.RetryBackoff > 0 {
		return h.cfg.RetryBackoff
	}
	return restartBackoff
}

func (h *Host) maxReadyFailures() int {
	if h.cfg.MaxReadyFailures > 0 {
		return h.cfg.MaxReadyFailures
	}
	return maxReadyFailures
}

// nextBackoff doubles a delay, capped. A non-positive delay starts at the
// configured floor.
func nextBackoff(d, floor time.Duration) time.Duration {
	if d <= 0 {
		return floor
	}
	d *= 2
	if d > restartBackoffCap {
		return restartBackoffCap
	}
	return d
}

// Start launches the host, waits until its embedded agent answers, and starts
// supervising it. A failure to become ready is an error: a control plane whose
// only node is dead should not pretend to be up.
//
// With AllowNotReady it is not a fatal error: Start returns the Host plus
// ErrNotReady, and the caller keeps a console that can still be used to fix the
// model. A native model configuration error retains the host and supervisor;
// transport/agent failures still stop after bounded retries (D101).
//
// Readiness is retried maxReadyFailures times with backoff before giving up:
// a cold disk or a slow first model load may miss one readiness window, but a
// configuration that can never work must not be retried forever.
func Start(ctx context.Context, cfg Config) (*Host, error) {
	if strings.TrimSpace(cfg.Bin) == "" {
		return nil, errors.New("本地宿主缺少 aiscan-full：用 --aiscan 指定路径")
	}
	if st, err := os.Stat(cfg.Bin); err != nil || st.IsDir() {
		return nil, fmt.Errorf("找不到 harness 二进制：%s", cfg.Bin)
	}
	if strings.TrimSpace(cfg.Dir) == "" {
		return nil, errors.New("本地宿主缺少工作目录")
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	token := strings.TrimSpace(cfg.Token)
	if token == "" {
		var buf [16]byte
		if _, err := rand.Read(buf[:]); err != nil {
			return nil, err
		}
		token = hex.EncodeToString(buf[:])
	}
	addr, err := pickAddr(cfg.Addr)
	if err != nil {
		return nil, err
	}
	db := strings.TrimSpace(cfg.DB)
	if db == "" {
		db = filepath.Join(cfg.Dir, "host", "cyber-web.db")
	}
	if err := os.MkdirAll(filepath.Dir(db), 0o755); err != nil {
		return nil, fmt.Errorf("建宿主数据目录：%w", err)
	}
	dir, err := filepath.Abs(cfg.Dir)
	if err != nil {
		return nil, fmt.Errorf("解析宿主工作目录：%w", err)
	}
	cfg.Dir = dir
	h := &Host{
		cfg:     cfg,
		addr:    "http://" + addr,
		token:   token,
		db:      db,
		stopped: make(chan struct{}),
		started: time.Now(),
		ctx:     ctx,
		tail:    &tailBuffer{max: 12},
	}
	floor := h.initialBackoff()
	backoff := floor
	for attempt := 1; ; attempt++ {
		if err := h.launch(); err != nil {
			if h.noteReadyFailure(err) {
				return nil, fmt.Errorf("本地宿主启动失败：%w%s", err, h.diagnostic())
			}
			if !h.sleep(ctx, backoff) {
				return nil, ctx.Err()
			}
			backoff = nextBackoff(backoff, floor)
			continue
		}
		h.cfg.Logf("host_event=start bin=%s endpoint=%s dir=%s pid=%d attempt=%d",
			cfg.Bin, h.addr, cfg.Dir, h.pid(), attempt)
		readyCtx, cancel := context.WithTimeout(ctx, h.readyTimeout())
		err := h.waitReady(readyCtx)
		cancel()
		if err == nil {
			go h.watch(ctx)
			return h, nil
		}
		if h.keepModelDegraded(err) {
			go h.watch(ctx)
			return h, fmt.Errorf("%w：%v", ErrNotReady, err)
		}
		h.kill()
		h.reap()
		if h.noteReadyFailure(err) {
			if !cfg.AllowNotReady {
				return nil, fmt.Errorf("本地宿主没就绪：%w%s", err, h.diagnostic())
			}
			// No supervisor ever started, so Stop must not block waiting for
			// one. Update replaces this channel when it revives the host.
			close(h.stopped)
			h.cfg.Logf("host_event=degraded endpoint=%s last_error=%q（控制台继续启动：可在「设置 → 模型」修好后自动重启宿主）",
				h.addr, err.Error())
			return h, fmt.Errorf("%w：%v%s", ErrNotReady, err, h.diagnostic())
		}
		if !h.sleep(ctx, backoff) {
			return nil, ctx.Err()
		}
		backoff = nextBackoff(backoff, floor)
	}
}

// Endpoint is the host's base URL (AOP application socket + management API).
func (h *Host) Endpoint() string { return h.addr }

// Token is the access key the node entry carries.
func (h *Host) Token() string { return h.token }

// Agent is the id of the embedded agent a session pins to.
func (h *Host) Agent() string { return LocalAgent }

// PID is the live child's process id, or 0 before the first launch. The orphan
// sweep uses it to never kill the host this process is supervising.
func (h *Host) PID() int { return h.pid() }

func (h *Host) pid() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cmd == nil {
		return 0
	}
	return h.cmd.pid
}

// Status is the supervisor's observation for the nodes API.
func (h *Host) Status() HostStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := HostStatus{
		Endpoint:    h.addr,
		PID:         h.cmdPIDLocked(),
		Restarts:    h.restarts,
		FailedReady: h.failedReady,
		Unhealthy:   h.unhealthy,
		LastError:   h.lastErr,
		Online:      h.online && !h.stopping,
	}
	if !h.running {
		st.PID = 0
	}
	st.Ready = h.readyOnce && !h.unhealthy && !h.stopping
	return st
}

// Ready reports whether the host has a usable agent right now. A degraded
// start (Start returned ErrNotReady) is not ready, and neither is a host whose
// supervisor gave up after repeated failures. Dispatch is gated on this so a
// dead model endpoint produces one actionable refusal instead of runs that fail
// in milliseconds inside the harness (D101).
func (h *Host) Ready() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.readyOnce && !h.unhealthy && !h.stopping
}

func (h *Host) cmdPIDLocked() int {
	if h.cmd == nil {
		return 0
	}
	return h.cmd.pid
}

// Unhealthy reports that the supervisor gave up: the configured model
// credentials or binary cannot bring a host up, and no further restart will fix
// that. The console can still edit settings, which updates and restarts the
// host (Update), so an unhealthy host is a visible stop, not a dead process.
func (h *Host) Unhealthy() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.unhealthy
}

// AddOnReady installs a callback that runs after every successful readiness
// check (first start and each restart). It runs on the supervisor goroutine, so
// it must be quick. More than one observer is allowed: main sweeps stale session
// markers, and the hub wakes its deciders only once the local host can serve a
// round (D111).
func (h *Host) AddOnReady(fn func(aop.Agent)) {
	if fn == nil {
		return
	}
	h.mu.Lock()
	h.onReady = append(h.onReady, fn)
	h.mu.Unlock()
}

// markReady publishes a ready host: reset the failure streak, log the structured
// event, persist the record, then run the hook.
func (h *Host) markReady(a aop.Agent) {
	h.mu.Lock()
	h.failedReady = 0
	h.lastErr = ""
	h.readyOnce = true
	h.online = true
	launchedAt := h.launchedAt
	onReady := append([]func(aop.Agent){}, h.onReady...)
	h.mu.Unlock()
	ms := int64(0)
	if !launchedAt.IsZero() {
		ms = time.Since(launchedAt).Milliseconds()
	}
	h.cfg.Logf("host_event=ready endpoint=%s agent=%s working_dir=%s ms=%d", h.addr, a.ID, a.WorkingDir, ms)
	h.writeRecord()
	for _, fn := range onReady {
		fn(a)
	}
}

// noteReadyFailure counts one failed launch/readiness attempt and reports
// whether the supervisor must stop. The structured log lines are the operator's
// only trace of "the host is up but its agent never became usable".
func (h *Host) noteReadyFailure(err error) bool {
	message := h.maskSecrets(err.Error())
	h.mu.Lock()
	h.failedReady++
	n := h.failedReady
	limit := h.maxReadyFailures()
	unhealthy := n >= limit
	if unhealthy {
		h.unhealthy = true
	}
	h.lastErr = message
	h.mu.Unlock()
	h.cfg.Logf("host_event=not_ready attempt=%d err=%q", n, err.Error())
	if unhealthy {
		h.cfg.Logf("host_event=unhealthy attempts=%d endpoint=%s last_error=%q", n, h.addr, err.Error())
	}
	return unhealthy
}

// sleep waits, unless the instance is shutting down.
func (h *Host) sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// Stop kills the host and everything it started, and returns once the child is
// gone. It is safe to call more than once.
func (h *Host) Stop() {
	h.mu.Lock()
	if h.stopping {
		h.mu.Unlock()
		<-h.stopped
		return
	}
	h.stopping = true
	h.mu.Unlock()
	h.kill()
	<-h.stopped
	// A clean stop removes the endpoint/PID record: its presence next start is
	// how the control plane knows the previous process died without stopping.
	h.removeRecord()
}

func (h *Host) isStopping() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.stopping
}

// Update replaces the harness environment/flags and restarts the host, so a
// settings save reaches the local node without restarting the control plane.
// The supervisor owns the restart: killing the child is enough. It reports
// whether the new configuration needs that restart: callers use true to defer
// work that needs a ready host (data-plane wakes) until AddOnReady fires, instead
// of dispatching into the restart window (D111).
//
// When the supervisor already gave up (unhealthy), a settings edit is the
// operator's fix: the failure budget is reset and supervision resumes. That is
// the console's "fix the key/base_url and retry" path (watchdog-liveness §9);
// without it the only way back from a bad credential would be restarting tw.
func (h *Host) Update(env, args []string) bool {
	h.mu.Lock()
	if h.stopping {
		h.mu.Unlock()
		return false
	}
	// Saving the settings page without changing anything must not restart the
	// host: the restart kills every local AOP session, which marks the runs on
	// this node as failed. Healthy hosts restart only on a configuration change.
	changed := !equalStrings(h.cfg.Env, env) || !equalStrings(h.cfg.Args, args)
	if changed {
		h.cfg.Env = append([]string(nil), env...)
		h.cfg.Args = append([]string(nil), args...)
	}
	cmd := h.cmd
	// A failed model can be fixed outside this process (for example a balance
	// top-up). Saving unchanged settings is an explicit retry when not ready.
	retry := !h.readyOnce
	revive := (changed || retry) && h.unhealthy
	if changed || retry {
		h.readyOnce = false
		h.online = false
	}
	ctx := h.ctx
	if revive {
		h.unhealthy = false
		h.failedReady = 0
		h.lastErr = ""
		// The old supervisor returned and closed its channel; a fresh one needs
		// a fresh channel for Stop's wait.
		h.stopped = make(chan struct{})
	}
	h.mu.Unlock()
	if !changed && !retry {
		return false
	}
	if revive {
		h.cfg.Logf("host: 配置变更，按新配置重新监督已停止的宿主")
		// The supervisor (re)enters its crash path: it waits on the dead child,
		// backs off, relaunches and — importantly — verifies readiness again,
		// exactly like any other restart.
		if ctx != nil {
			go h.watch(ctx)
		} else {
			h.kill()
		}
		return true
	}
	if cmd == nil {
		// No live child: the launch loop (or the next revive) already reads the
		// new configuration; there is nothing to kill.
		return true
	}
	h.cfg.Logf("host: 配置变更，重启本机宿主")
	cmd.kill()
	return true
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (h *Host) launch() error {
	proc, err := launchProcess(h)
	if err != nil {
		return err
	}
	h.mu.Lock()
	h.cmd = proc
	h.running = true
	h.online = false
	h.readyOnce = false
	h.launchedAt = time.Now()
	h.mu.Unlock()
	return nil
}

// execLaunch starts the real binary. It is what launchProcess calls in
// production; the liveness tests replace launchProcess instead.
func (h *Host) execLaunch() (*process, error) {
	h.mu.Lock()
	env := append([]string(nil), h.cfg.Env...)
	extra := append([]string(nil), h.cfg.Args...)
	h.mu.Unlock()
	args := []string{
		"web",
		"--addr", strings.TrimPrefix(h.addr, "http://"),
		"--db", h.db,
		"--token", h.token,
	}
	args = append(args, extra...)
	cmd := exec.Command(h.cfg.Bin, args...)
	cmd.Dir = h.cfg.Dir
	cmd.Env = withExcludeTools(h.childEnv(env), h.cfg.ExcludeTools)
	setProcAttr(cmd)
	cmd.WaitDelay = 10 * time.Second
	cmd.Stdout = &lineWriter{h: h, name: "out"}
	cmd.Stderr = &lineWriter{h: h, name: "err"}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动本地宿主：%w", err)
	}
	return &process{
		pid:  cmd.Process.Pid,
		wait: cmd.Wait,
		kill: func() { killGroup(cmd) },
	}, nil
}

// childEnv builds the host's environment: tw's own, plus the instance settings.
//
// On Windows it also fixes the one gap that makes the agent's shell useless: a
// control plane started from PowerShell has no `ls`/`grep`/`sh` on PATH (only
// C:\Windows\System32\bash.exe, which is WSL). The harness's shell tool is a
// POSIX interpreter that runs external commands from PATH, so without coreutils
// the model falls back to `cmd`/`dir` — and `cmd //c dir` starts an interactive
// shell that never returns (no per-command timeout upstream, D64 note). Git for
// Windows ships those tools; add its usr/bin and say so in the log.
func (h *Host) childEnv(extra []string) []string {
	env := append([]string{}, os.Environ()...)
	for _, prefix := range h.cfg.DropEnvPrefixes {
		if prefix == "" {
			continue
		}
		kept := env[:0]
		for _, kv := range env {
			if k, _, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(strings.ToUpper(k), strings.ToUpper(prefix)) {
				continue
			}
			kept = append(kept, kv)
		}
		env = kept
	}
	for _, kv := range extra {
		if k, v, ok := strings.Cut(kv, "="); ok {
			setEnv(&env, k, v)
		}
	}
	if runtime.GOOS != "windows" {
		return env
	}
	path := envValue(env, "PATH")
	if hasExe(path, "ls.exe") {
		return env
	}
	dir := coreutilsDir()
	if dir == "" {
		h.cfg.Logf("host: 警告：宿主的 PATH 里没有 ls/grep（agent 的 shell 需要 coreutils）；装 Git for Windows，或把它的 usr\\bin 加进 PATH。否则 agent 会用 cmd 兜底，可能卡住")
		return env
	}
	// Prepend, not append: `bash` here must be Git's, not WSL's
	// C:\Windows\System32\bash.exe, or the agent sees /mnt/c paths.
	setEnv(&env, "PATH", dir+string(os.PathListSeparator)+path)
	h.cfg.Logf("host: PATH 里没有 ls，已把 %s 加进宿主 PATH（agent 的 shell 需要 coreutils）", dir)
	return env
}

// withExcludeTools pins CYBER_EXCLUDE_TOOLS in the child environment. The
// control plane's own environment may already carry the variable; setEnv keeps
// exactly one definition, so the instance's decision (default "subagent", or
// nothing with --allow-native-subagent) wins over a stale inherited value.
func withExcludeTools(env []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return env
	}
	setEnv(&env, "CYBER_EXCLUDE_TOOLS", value)
	return env
}

// coreutilsDir finds a Git for Windows installation that carries ls.exe.
func coreutilsDir() string {
	var candidates []string
	if pf := os.Getenv("ProgramFiles"); pf != "" {
		candidates = append(candidates, filepath.Join(pf, "Git", "usr", "bin"))
	}
	if pf := os.Getenv("ProgramFiles(x86)"); pf != "" {
		candidates = append(candidates, filepath.Join(pf, "Git", "usr", "bin"))
	}
	if la := os.Getenv("LOCALAPPDATA"); la != "" {
		candidates = append(candidates, filepath.Join(la, "Programs", "Git", "usr", "bin"))
	}
	for _, dir := range candidates {
		if st, err := os.Stat(filepath.Join(dir, "ls.exe")); err == nil && !st.IsDir() {
			return dir
		}
	}
	return ""
}

func hasExe(path, name string) bool {
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			continue
		}
		if st, err := os.Stat(filepath.Join(dir, name)); err == nil && !st.IsDir() {
			return true
		}
	}
	return false
}

// envValue reads a variable case-insensitively: Windows keeps whatever case the
// launcher used ("Path=" is common), and a second PATH= entry is not merged.
func envValue(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if k, v, ok := strings.Cut(env[i], "="); ok && strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
}

// setEnv sets key to val, keeping exactly one definition: Windows launchers can
// hand us both "Path=" and "PATH=", and a child that sees two would be
// ambiguous. The single value is appended; order inside the block is irrelevant
// (PATH order is inside the value, not the environment array).
func setEnv(env *[]string, key, val string) {
	out := make([]string, 0, len(*env)+1)
	for _, kv := range *env {
		if k, _, ok := strings.Cut(kv, "="); ok && strings.EqualFold(k, key) {
			continue
		}
		out = append(out, kv)
	}
	*env = append(out, key+"="+val)
}

// lineWriter forwards one child stream to the log, one line at a time, and
// keeps the tail for error messages. The token is masked: the harness prints it
// in its own startup banner.
type lineWriter struct {
	h    *Host
	name string
	buf  []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := strings.IndexByte(string(w.buf), '\n')
		if i < 0 {
			break
		}
		line := strings.TrimRight(string(w.buf[:i]), "\r")
		w.buf = w.buf[i+1:]
		if strings.TrimSpace(line) != "" {
			w.h.tail.add(line)
			w.h.cfg.Logf("host[%s]: %s", w.name, maskToken(line, w.h.token))
		}
	}
	// A host that never emits a newline must not grow the buffer forever.
	if len(w.buf) > 64<<10 {
		w.buf = w.buf[len(w.buf)-(1<<10):]
	}
	return len(p), nil
}

// waitReady polls ListAgents until the local agent is usable. "Usable" means
// the agent id is `local`, it reports a working directory, and that directory
// is this instance's data directory: a host that reports ready but whose agent
// has a nil provider/model (the failure we measured in the field) answers this
// call, so every attempt is verified end to end rather than by process liveness.
func (h *Host) waitReady(ctx context.Context) error {
	client := dialHost(h.addr, h.token)
	var last error
	for {
		callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		agents, err := client.Agents(callCtx)
		cancel()
		if err == nil {
			// Native ConfigError distinguishes a usable host with a failed model
			// from a dead host. Do not infer provider health from startup prose.
			if a, ok := aop.Find(agents, LocalAgent); ok && sameWorkingDir(a.WorkingDir, h.cfg.Dir) && a.ConfigError != "" {
				return &modelNotReady{reason: a.ConfigError}
			}
			if a, ok := readyAgent(agents, h.cfg.RequireProvider); ok {
				if sameWorkingDir(a.WorkingDir, h.cfg.Dir) {
					h.markReady(a)
					return nil
				}
				err = fmt.Errorf("宿主 agent 的工作目录是 %q，不是本实例的 %q（端口可能被另一个实例的宿主占了）",
					a.WorkingDir, h.cfg.Dir)
			} else if h.cfg.RequireProvider {
				err = fmt.Errorf("宿主内置 agent 的 provider/model 未就绪（期望 id=%q 且 WorkingDir/provider/model 均非空；实际 %d 个 agent）",
					LocalAgent, len(agents))
			} else {
				err = fmt.Errorf("宿主没有可用的内置 agent（期望 id=%q 且 WorkingDir 非空；实际 %d 个 agent，是不是用了 --no-agent？）",
					LocalAgent, len(agents))
			}
		}
		last = err
		select {
		case <-ctx.Done():
			return last
		case <-time.After(readyPoll):
		}
	}
}

type modelNotReady struct{ reason string }

func (e *modelNotReady) Error() string { return "模型不可用：" + e.reason }

// A model failure must not take down PTYs, native commands or browser control.
// Keep supervising the reachable host, but never publish dispatch readiness.
func (h *Host) keepModelDegraded(err error) bool {
	var modelErr *modelNotReady
	if !h.cfg.AllowNotReady || !errors.As(err, &modelErr) {
		return false
	}
	message := h.maskSecrets(err.Error())
	h.mu.Lock()
	h.online = true
	h.readyOnce = false
	h.failedReady = 0
	h.lastErr = message
	h.mu.Unlock()
	h.writeRecord()
	h.cfg.Logf("host_event=degraded endpoint=%s last_error=%q（宿主保留终端，模型配置保存后重试）", h.addr, h.maskSecrets(err.Error()))
	return true
}

func (h *Host) diagnostic() string {
	if tail := h.tail.join(); tail != "" {
		return "\n宿主最后输出：\n" + maskToken(tail, h.token)
	}
	return ""
}

// watch waits for the child and restarts it with backoff when it dies on its
// own. A host crash would otherwise silently strand every project.
//
// The restart loop is bounded: after maxReadyFailures consecutive launches or
// readiness checks that never produce a usable agent, the host is marked
// unhealthy and the supervisor stops. The operator sees `host_event=unhealthy`
// once, instead of an endless restart storm that hides the real cause (an
// expired key or a bad base_url).
func (h *Host) watch(ctx context.Context) {
	defer close(h.stopped)
	floor := h.initialBackoff()
	backoff := floor
	for {
		h.mu.Lock()
		proc := h.cmd
		h.mu.Unlock()
		if proc == nil {
			h.cfg.Logf("host_event=stopped reason=no_process")
			return
		}
		err := proc.wait()
		h.mu.Lock()
		h.running = false
		h.online = false
		h.readyOnce = false
		h.lastErr = "宿主已退出：" + exitReason(err)
		h.mu.Unlock()
		if h.isStopping() {
			h.cfg.Logf("host_event=stopped reason=shutdown")
			return
		}
		if ctx.Err() != nil {
			h.kill()
			h.cfg.Logf("host_event=stopped reason=context")
			return
		}
		h.mu.Lock()
		h.restarts++
		n := h.restarts
		h.mu.Unlock()
		h.cfg.Logf("host_event=restart attempt=%d backoff=%s reason=%q", n, backoff, exitReason(err))
		if !h.sleep(ctx, backoff) {
			h.kill()
			h.cfg.Logf("host_event=stopped reason=context")
			return
		}
		backoff = nextBackoff(backoff, floor)
		for {
			// Stop does not cancel the parent context; bail out so Stop's wait on
			// h.stopped cannot block for a whole backoff window.
			if h.isStopping() || ctx.Err() != nil {
				h.kill()
				h.cfg.Logf("host_event=stopped reason=shutdown")
				return
			}
			if launchErr := h.launch(); launchErr != nil {
				if h.noteReadyFailure(launchErr) {
					h.cfg.Logf("host_event=stopped reason=unhealthy")
					return
				}
				if !h.sleep(ctx, backoff) {
					h.cfg.Logf("host_event=stopped reason=context")
					return
				}
				backoff = nextBackoff(backoff, floor)
				continue
			}
			readyCtx, cancel := context.WithTimeout(ctx, h.readyTimeout())
			readyErr := h.waitReady(readyCtx)
			cancel()
			if readyErr == nil || h.keepModelDegraded(readyErr) {
				backoff = floor
				break
			}
			h.kill()
			h.reap()
			if h.noteReadyFailure(readyErr) {
				h.cfg.Logf("host_event=stopped reason=unhealthy")
				return
			}
			if !h.sleep(ctx, backoff) {
				h.cfg.Logf("host_event=stopped reason=context")
				return
			}
			backoff = nextBackoff(backoff, floor)
		}
	}
}

// exitReason renders a Wait error for the restart log without the noisy
// "exit status N" wrapper around an already-readable message.
func exitReason(err error) string {
	if err == nil {
		return "clean exit"
	}
	return err.Error()
}

func (h *Host) kill() {
	h.mu.Lock()
	proc := h.cmd
	h.mu.Unlock()
	if proc != nil {
		proc.kill()
	}
}

// reap waits for a child that was killed before the supervisor started (the
// Start failure path), so no zombie or half-closed pipe is left behind.
func (h *Host) reap() {
	h.mu.Lock()
	proc := h.cmd
	h.mu.Unlock()
	if proc != nil && proc.wait != nil {
		_ = proc.wait()
	}
	h.mu.Lock()
	h.running = false
	h.online = false
	h.readyOnce = false
	h.mu.Unlock()
}

func (h *Host) maskSecrets(s string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	s = maskToken(s, h.token)
	for _, kv := range h.cfg.Env {
		if k, v, ok := strings.Cut(kv, "="); ok && v != "" && (strings.Contains(strings.ToUpper(k), "KEY") || strings.Contains(strings.ToUpper(k), "TOKEN")) {
			s = strings.ReplaceAll(s, v, "<secret>")
		}
	}
	return s
}

// pickAddr returns an address the host can listen on. A preferred address is
// honoured when free; otherwise a loopback port is taken from the OS, so a
// second instance (or a leftover host) cannot collide with the default.
func pickAddr(preferred string) (string, error) {
	if p := strings.TrimSpace(preferred); p != "" {
		if ln, err := net.Listen("tcp", p); err == nil {
			addr := ln.Addr().String()
			_ = ln.Close()
			return addr, nil
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("本地宿主没有可用端口：%w", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr, nil
}

// maskToken keeps the access key out of the log: the harness prints it in its
// startup banner.
func maskToken(s, token string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "<token>")
}

// tailBuffer keeps the last n lines of host output for diagnostics.
type tailBuffer struct {
	mu    sync.Mutex
	max   int
	lines []string
}

func (t *tailBuffer) add(line string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines = append(t.lines, line)
	if len(t.lines) > t.max {
		t.lines = t.lines[len(t.lines)-t.max:]
	}
}

func (t *tailBuffer) join() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(t.lines, "\n")
}
