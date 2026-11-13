package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeRunEvents lays down a synthetic run journal. The tail endpoint reads the
// same file dispatch writes, so a fixture file is enough to test its edges
// without starting a harness.
func (f *agentFixture) writeRunEvents(t *testing.T, runID string, n int) {
	t.Helper()
	dir := filepath.Join(f.srv.DataDir, "runs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, `{"kind":"tool_call","tool":"bash","text":"line-%d"}`+"\n", i)
	}
	if err := os.WriteFile(filepath.Join(dir, runID+".jsonl"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A participant can peek at another run in its project: this is the sapper
// `peek` equivalent (read-only, bounded, no writes).
func TestAgentPeekReturnsTailOfAnotherRunsEvents(t *testing.T) {
	f := newAgentFixture(t)
	run := f.makeLive(t)
	f.writeRunEvents(t, run.ID, 10)

	rec := f.call(t, f.srcID, http.MethodGet,
		"/api/runs/"+run.ID+"/events/tail?lines=3", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("tail: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Run       runTailView       `json:"run"`
		Events    []json.RawMessage `json:"events"`
		Lines     int               `json:"lines"`
		Truncated bool              `json:"truncated"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Events) != 3 || !resp.Truncated || resp.Lines != 3 {
		t.Fatalf("events=%d lines=%d truncated=%v", len(resp.Events), resp.Lines, resp.Truncated)
	}
	if !strings.Contains(string(resp.Events[2]), "line-10") {
		t.Fatalf("last event = %s, want line-10", resp.Events[2])
	}
	if resp.Run.ID != run.ID || resp.Run.IntentID != f.target.ID {
		t.Fatalf("run view = %+v", resp.Run)
	}
}

// A bounded read must not become an unbounded one: lines over the ceiling is a
// client error, and a traversal-shaped id is not a run id at all.
func TestAgentPeekClampsLinesAndRejectsTraversal(t *testing.T) {
	f := newAgentFixture(t)
	run := f.makeLive(t)
	f.writeRunEvents(t, run.ID, 5)

	rec := f.call(t, f.srcID, http.MethodGet,
		"/api/runs/"+run.ID+"/events/tail?lines=9999", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized lines: %d %s", rec.Code, rec.Body.String())
	}

	// A decoded %2F would otherwise let the id escape data/runs.
	rec = f.call(t, f.srcID, http.MethodGet, "/api/runs/..%2F..%2Fsecret/events/tail", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("traversal: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "line-") {
		t.Fatalf("traversal response leaked a journal: %s", rec.Body.String())
	}

	// A missing lines parameter falls back to the bounded default rather than
	// erroring, so a plain `?` or no query is always safe to call.
	rec = f.call(t, f.srcID, http.MethodGet, "/api/runs/"+run.ID+"/events/tail", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("default lines: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Lines int `json:"lines"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Lines != defaultEventTailLines {
		t.Fatalf("default lines = %d, want %d", resp.Lines, defaultEventTailLines)
	}
}

// The project boundary is the Store: a run that exists only in another
// project's ledger is 404 here, even though the URL shape is valid.
func TestAgentPeekUnknownRunIs404(t *testing.T) {
	f := newAgentFixture(t)
	f.makeLive(t)

	rec := f.call(t, f.srcID, http.MethodGet, "/api/runs/r_does_not_exist/events/tail?lines=5", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown run: %d %s", rec.Code, rec.Body.String())
	}

	other := newAgentFixture(t)
	otherRun := other.makeLive(t)
	other.writeRunEvents(t, otherRun.ID, 2)
	rec = f.call(t, f.srcID, http.MethodGet,
		"/api/runs/"+otherRun.ID+"/events/tail?lines=5", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-project run: %d %s", rec.Code, rec.Body.String())
	}
}

// Like every participant handler, peek needs the run's identity: the hub
// resolves the token before the handler is reached, and a bare mux call without
// one is anonymous.
func TestAgentPeekRequiresIdentity(t *testing.T) {
	f := newAgentFixture(t)
	run := f.makeLive(t)
	req := httptest.NewRequest(http.MethodGet, "/api/runs/"+run.ID+"/events/tail", nil)
	rec := httptest.NewRecorder()
	f.srv.AgentHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no identity: %d %s", rec.Code, rec.Body.String())
	}
}

// The console and the agent read the same file through the same helper; the
// same N must produce the same events, or two observers of one run would disagree.
func TestRunEventsAndAgentTailUseTheSameReader(t *testing.T) {
	f := newAgentFixture(t)
	run := f.makeLive(t)
	f.writeRunEvents(t, run.ID, 10)

	console := call(t, f.srv, http.MethodGet, "/api/runs/"+run.ID+"/events?limit=4", nil)
	if console.Code != http.StatusOK {
		t.Fatalf("console events: %d %s", console.Code, console.Body.String())
	}
	peek := f.call(t, f.srcID, http.MethodGet, "/api/runs/"+run.ID+"/events/tail?lines=4", nil)
	if peek.Code != http.StatusOK {
		t.Fatalf("agent tail: %d %s", peek.Code, peek.Body.String())
	}
	decode := func(body []byte) []json.RawMessage {
		var v struct {
			Events []json.RawMessage `json:"events"`
		}
		if err := json.Unmarshal(body, &v); err != nil {
			t.Fatal(err)
		}
		return v.Events
	}
	a, b := decode(console.Body.Bytes()), decode(peek.Body.Bytes())
	if len(a) != len(b) || len(a) != 4 {
		t.Fatalf("console %d events, agent %d events", len(a), len(b))
	}
	for i := range a {
		if string(a[i]) != string(b[i]) {
			t.Fatalf("event %d differs: %s vs %s", i, a[i], b[i])
		}
	}
}
