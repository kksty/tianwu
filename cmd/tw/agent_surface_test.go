package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// D109: harness sessions (commander and executors) authenticate with their own
// token and see only the battlefield surface. Operator-only routes are absent
// (404, not 403) so a probing model learns nothing; the operator keeps the full
// API through the login token/cookie.
func TestAgentTokenSeesOnlyTheBattlefield(t *testing.T) {
	const console = "console-token"
	agent := agentAPIToken(console)
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	h := withConsoleAuth(ok, console)

	do := func(token, method, path string) int {
		req := httptest.NewRequest(method, path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	// The engagement surface: board, ledger, dispatch, collaboration and the
	// knowledge/tier lookups the commander skill documents.
	visible := []struct{ method, path string }{
		{http.MethodGet, "/p/p1/api/instructions"},
		{http.MethodGet, "/p/p1/api/instructions/instruction_1"},
		{http.MethodGet, "/p/p1/api/instructions/instruction_1/history"},
		{http.MethodPost, "/p/p1/api/instructions/instruction_1/ack"},
		{http.MethodGet, "/p/p1/api/board"},
		{http.MethodGet, "/p/p1/api/intents"},
		{http.MethodPost, "/p/p1/api/intents"},
		{http.MethodPost, "/p/p1/api/intents/in_1/retry"},
		{http.MethodGet, "/p/p1/api/facts"},
		{http.MethodGet, "/p/p1/api/facts/f_1"},
		{http.MethodGet, "/p/p1/api/facts/f_1/history"},
		{http.MethodGet, "/p/p1/api/changes"},
		{http.MethodGet, "/p/p1/api/context"},
		{http.MethodPost, "/p/p1/api/context/ack"},
		{http.MethodGet, "/p/p1/api/graph"},
		{http.MethodGet, "/p/p1/api/experience"},
		{http.MethodGet, "/p/p1/api/focus?intent=in_1"},
		{http.MethodGet, "/p/p1/api/resources"},
		{http.MethodGet, "/p/p1/api/communication"},
		{http.MethodPut, "/p/p1/api/intents/in_1/context"},
		{http.MethodGet, "/p/p1/api/retrieval/status"},
		{http.MethodPost, "/p/p1/api/retrieval/index"},
		{http.MethodGet, "/p/p1/api/evidence"},
		{http.MethodGet, "/p/p1/api/goals"},
		{http.MethodPost, "/p/p1/api/goals/g_1/complete"},
		{http.MethodGet, "/p/p1/api/playbooks"},
		{http.MethodGet, "/p/p1/api/playbooks/pb_1/history"},
		{http.MethodPost, "/p/p1/api/playbooks/nominate"},
		{http.MethodGet, "/p/p1/api/runs"},
		{http.MethodGet, "/p/p1/api/runs/r_1/events"},
		{http.MethodGet, "/p/p1/api/messages"},
		{http.MethodPost, "/p/p1/api/messages/m_1/retract"},
		{http.MethodGet, "/p/p1/api/participants"},
		{http.MethodGet, "/p/p1/api/state"},
		{http.MethodPut, "/p/p1/api/state"},
		// The commander's own global stop request: the one /api/project/ action a
		// harness session may take. Pause/resume/append-goal stay operator-only.
		// Visibility is not success: the handler additionally requires a commander
		// attribution and a live commander round (declarative boundary, D78).
		{http.MethodPost, "/p/p1/api/project/standby"},
		{http.MethodGet, "/p/p1/api/model-profiles"},
		{http.MethodGet, "/api/model-profiles"},
		{http.MethodGet, "/api/kb?q=smtp"},
	}
	for _, tc := range visible {
		if got := do(agent, tc.method, tc.path); got != http.StatusNoContent {
			t.Errorf("agent %s %s = %d, want the handler", tc.method, tc.path, got)
		}
	}

	// Operator controls and delivery surfaces are not part of the agent's API.
	hidden := []struct{ method, path string }{
		{http.MethodPost, "/p/p1/api/instructions"},
		{http.MethodPut, "/p/p1/api/instructions/instruction_1"},
		// The premise admission mode is an operator decision about what the
		// project accepts as an actionable predecessor; a harness session only
		// reads the resulting gate through dispatch errors.
		{http.MethodGet, "/p/p1/api/premise-policy"},
		{http.MethodPut, "/p/p1/api/premise-policy"},
		// One operator text entry: harness sessions receive its effects as
		// messages/instructions/focus, never as a second control surface.
		{http.MethodPost, "/p/p1/api/directives"},
		{http.MethodGet, "/p/p1/api/reports"},
		{http.MethodPost, "/p/p1/api/reports"},
		{http.MethodGet, "/p/p1/api/reports/precheck"},
		{http.MethodGet, "/p/p1/api/reports/rp_1"},
		{http.MethodGet, "/p/p1/api/reports/rp_1/file"},
		{http.MethodGet, "/p/p1/api/reviews"},
		{http.MethodPost, "/p/p1/api/facts/f_1/verify"},
		{http.MethodPost, "/p/p1/api/facts/f_1/review"},
		{http.MethodPost, "/p/p1/api/facts/verify-pending"},
		{http.MethodGet, "/api/settings"},
		{http.MethodPut, "/api/settings"},
		{http.MethodPost, "/api/model-profiles"},
		{http.MethodPut, "/api/model-profiles/fast"},
		{http.MethodDelete, "/api/model-profiles/fast"},
		{http.MethodGet, "/api/model-profiles/fast"},
		{http.MethodPost, "/api/model-profiles/fast/activate"},
		{http.MethodPost, "/api/model-profiles/fast/pause"},
		{http.MethodPost, "/api/model-profiles/fast/probe"},
		{http.MethodPost, "/p/p1/api/model-profiles"},
		{http.MethodPost, "/p/p1/api/model-profiles/fast/pause"},
		{http.MethodGet, "/api/models"},
		{http.MethodGet, "/api/model-catalogue"},
		{http.MethodGet, "/api/projects"},
		{http.MethodPut, "/api/projects/p1"},
		{http.MethodGet, "/api/fleet"},
		{http.MethodGet, "/api/nodes"},
		{http.MethodGet, "/api/decisions"},
		{http.MethodPost, "/api/decide"},
		{http.MethodPost, "/api/decider/pause"},
		{http.MethodPost, "/api/decider/resume"},
		{http.MethodPost, "/api/project/pause"},
		{http.MethodPost, "/api/project/resume"},
		// Appending a goal and re-arming a parked project is an operator decision;
		// a harness session only sees the resulting goal and dispatch. It stays on
		// the same operator-only project surface as pause/resume.
		{http.MethodPost, "/api/project/append-goal"},
		{http.MethodPost, "/p/p1/api/project/append-goal"},
		{http.MethodPost, "/api/hint"},
		{http.MethodPost, "/api/spawn"},
		{http.MethodGet, "/api/workers"},
		{http.MethodPost, "/api/workers/w_1/kill"},
		{http.MethodGet, "/api/vulns"},
		{http.MethodPost, "/api/traffic/import"},
		{http.MethodPost, "/api/assets/import"},
		{http.MethodPost, "/api/pocs/import"},
		{http.MethodPost, "/api/bundles/export"},
		{http.MethodGet, "/api/bundles/b1"},
		{http.MethodGet, "/api/version"},
		{http.MethodGet, "/p/p1/api/graph/export"},
		{http.MethodPost, "/p/p1/api/playbooks/pb_1/archive"},
		{http.MethodPost, "/p/p1/api/retrieval/unknown"},
	}
	for _, tc := range hidden {
		if got := do(agent, tc.method, tc.path); got != http.StatusNotFound {
			t.Errorf("agent %s %s = %d, want 404 (route not exposed)", tc.method, tc.path, got)
		}
		if got := do(console, tc.method, tc.path); got != http.StatusNoContent {
			t.Errorf("operator %s %s = %d, want the handler", tc.method, tc.path, got)
		}
	}

	// Neither credential (or a wrong one) is still a 401: the hidden routes are
	// not a way around authentication.
	for _, token := range []string{"", "not-a-token"} {
		if got := do(token, http.MethodGet, "/api/settings"); got != http.StatusUnauthorized {
			t.Errorf("token %q GET /api/settings = %d, want 401", token, got)
		}
	}
}

// The host environment is what every harness session inherits. It carries the
// scoped agent token and never the operator's login token; the derived value is
// stable (so a host restart keeps working) and distinct from the console one.
func TestHostEnvCarriesScopedAgentToken(t *testing.T) {
	const console = "console-token"
	env := hostEnvWithAgentToken([]string{"CYBER_MODEL=m1"}, console)
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "TW_CONSOLE_TOKEN") {
		t.Fatalf("host env carries the operator token: %v", env)
	}
	if want := "TW_AGENT_TOKEN=" + agentAPIToken(console); !strings.Contains(joined, want) {
		t.Fatalf("host env missing %s: %v", want, env)
	}
	if !strings.Contains(joined, "CYBER_MODEL=m1") {
		t.Fatalf("host env lost the model env: %v", env)
	}
	if agentAPIToken(console) == agentAPIToken("another-console") {
		t.Fatal("agent token does not depend on the console token")
	}
	if agentAPIToken("") == "" {
		t.Fatal("empty console token must not produce an empty agent token")
	}
}

// Windows toolchains default to the ANSI code page; a session that reads a UTF-8
// file without an explicit encoding must not have to discover that per command.
// The defaults are UTF-8 mode, and an operator-provided value still wins.
func TestHostEnvDefaultsToolchainsToUTF8(t *testing.T) {
	env := hostEnvWithAgentToken([]string{"CYBER_MODEL=m1"}, "console")
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "PYTHONUTF8=1") || !strings.Contains(joined, "PYTHONIOENCODING=utf-8") {
		t.Fatalf("host env missing UTF-8 defaults: %v", env)
	}
	overridden := hostEnvWithAgentToken([]string{"PYTHONUTF8=0", "PYTHONIOENCODING=gbk"}, "")
	joined = strings.Join(overridden, "\n")
	if !strings.Contains(joined, "PYTHONUTF8=0") || !strings.Contains(joined, "PYTHONIOENCODING=gbk") {
		t.Fatalf("operator values were overwritten: %v", overridden)
	}
	if strings.Count(joined, "PYTHONUTF8=") != 1 {
		t.Fatalf("duplicate PYTHONUTF8 entries: %v", overridden)
	}
}
