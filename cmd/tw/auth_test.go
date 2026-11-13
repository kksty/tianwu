package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/collab"
)

// Where a write came from must not depend on the model remembering a header.
// The agent token is the credential of a harness session on the battle surface,
// and the only harness session that talks to the project mux directly is the
// commander (executors arrive through /agent/). The gate therefore stamps the
// actor from the credential: a say without X-Tw-Actor used to land as
// `operator` in the ledger.
func TestAgentTokenStampsProjectWritesAsCommander(t *testing.T) {
	const operatorToken = "operator-token-for-the-auth-test"
	auth := newConsoleAuth(operatorToken)
	if auth == nil {
		t.Fatal("no console auth was created")
	}
	var seen string
	protected := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("X-Tw-Actor")
		w.WriteHeader(http.StatusOK)
	})
	wrapped := auth.wrap(protected)

	call := func(path, token string) int {
		t.Helper()
		seen = ""
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		wrapped.ServeHTTP(rec, req)
		return rec.Code
	}

	// The commander's credential on the project mux is attributed structurally.
	if code := call("/p/prj-1/api/intents/in_1/say", agentAPIToken(operatorToken)); code != http.StatusOK {
		t.Fatalf("agent token on the project mux: %d", code)
	}
	if seen != collab.CommanderID {
		t.Fatalf("X-Tw-Actor = %q, want %q", seen, collab.CommanderID)
	}

	// The operator's own credential stays the operator: it authors messages
	// itself, so the gate must not relabel it.
	if code := call("/p/prj-1/api/intents/in_1/say", operatorToken); code != http.StatusOK {
		t.Fatalf("operator token on the project mux: %d", code)
	}
	if seen != "" {
		t.Fatalf("operator request was stamped as %q", seen)
	}

	// /agent/ is the participant surface: executors authenticate there with
	// their own per-run token, and their identity must not be replaced by the
	// commander's.
	if code := call("/agent/run/r_1/api/messages", agentAPIToken(operatorToken)); code != http.StatusOK {
		t.Fatalf("/agent/ passthrough: %d", code)
	}
	if seen != "" {
		t.Fatalf("/agent/ request was stamped as %q", seen)
	}
}

// next is attacker-controlled (a link to /login?next=…) and lands in the hidden
// form field. Escaping is what stops a reflected script on the console origin.
func TestLoginPageEscapesNext(t *testing.T) {
	page := renderLogin(`/"><script>alert(1)</script>`, "")
	if strings.Contains(page, "<script>alert(1)</script>") {
		t.Fatalf("next was written into the page unescaped")
	}
	if !strings.Contains(page, "&lt;script&gt;") {
		t.Fatalf("expected the escaped form in the field:\n%s", page)
	}
}

func TestSafeNextOnlyAllowsLocalPaths(t *testing.T) {
	cases := map[string]string{
		"/p/prj-1/":        "/p/prj-1/",
		"/":                "/",
		"":                 "/",
		"//evil.example":   "/",
		"https://evil/x":   "/",
		"javascript:alert": "/",
		`\\evil\share`:     "/",
		"/ok\r\nX-Bad: 1":  "/",
	}
	for in, want := range cases {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHumanKnowledgeCurationIsNotAvailableThroughAgentCredentials(t *testing.T) {
	const token = "knowledge-operator-test-token"
	protected := newConsoleAuth(token).wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	for _, test := range []struct{ method, path string }{
		{"DELETE", "/p/project/api/playbooks/pb_1"},
		{"POST", "/p/project/api/playbooks/pb_1/kb-candidate"},
		{"DELETE", "/api/kb/entries/kb001"},
		{"POST", "/api/kb/candidates/id/publish"},
	} {
		for _, agent := range []bool{false, true} {
			req := httptest.NewRequest(test.method, test.path, nil)
			credential := token
			if agent {
				credential = agentAPIToken(token)
			}
			req.Header.Set("Authorization", "Bearer "+credential)
			rec := httptest.NewRecorder()
			protected.ServeHTTP(rec, req)
			want := 200
			if agent {
				want = 404
			}
			if rec.Code != want {
				t.Fatalf("%s %s agent=%v: %d", test.method, test.path, agent, rec.Code)
			}
		}
	}
}
