package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/model"
)

func agentCall(t *testing.T, s *Server, ident collab.Identity, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	s.registerAgent(mux)
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(WithIdentity(req.Context(), ident))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func TestTaskContextErrorsDistinguishMissingInvalidAndStorageFailure(t *testing.T) {
	s, p := newBindingServer(t)
	if rec := call(t, s, http.MethodPut, "/api/intents/in_missing/context", map[string]any{"version": 0}); rec.Code != http.StatusNotFound {
		t.Fatalf("missing intent = %d %s", rec.Code, rec.Body.String())
	}
	in, err := p.Store.CreateIntent(model.Intent{Title: "context errors"})
	if err != nil {
		t.Fatal(err)
	}
	if rec := call(t, s, http.MethodPut, "/api/intents/"+in.ID+"/context", map[string]any{"version": 0, "references": []string{"f_missing"}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing fact reference = %d %s", rec.Code, rec.Body.String())
	}
	db, err := sql.Open("sqlite", p.Store.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TRIGGER fail_context BEFORE UPDATE ON meta BEGIN SELECT RAISE(ABORT,'fixture context failure'); END"); err != nil {
		t.Fatal(err)
	}
	if rec := call(t, s, http.MethodPut, "/api/intents/"+in.ID+"/context", map[string]any{"version": 0, "entities": []string{"host:fixture"}}); rec.Code != http.StatusInternalServerError {
		t.Fatalf("storage failure = %d %s", rec.Code, rec.Body.String())
	}
}

func TestTaskContextAPIUsesVersionCASAndRunOwnership(t *testing.T) {
	s, p := newBindingServer(t)
	ref, err := p.Store.AddFact(model.Fact{IntentID: "in_elsewhere", Kind: "note", Title: "参考"})
	if err != nil {
		t.Fatal(err)
	}
	in, err := p.Store.CreateIntent(model.Intent{Title: "task"})
	if err != nil {
		t.Fatal(err)
	}

	// Operator (console credential, no run identity) edits with the version it
	// read. A stale edit is a conflict, not a silent overwrite.
	if rec := call(t, s, http.MethodPut, "/api/intents/"+in.ID+"/context", map[string]any{
		"version": 0, "references": []string{ref.ID}, "pins": []string{}, "entities": []string{"host:10.0.0.5"},
	}); rec.Code != http.StatusOK {
		t.Fatalf("operator update = %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(t, s, http.MethodPut, "/api/intents/"+in.ID+"/context", map[string]any{
		"version": 0, "references": []string{},
	}); rec.Code != http.StatusConflict {
		t.Fatalf("stale update = %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(t, s, http.MethodPut, "/api/intents/"+in.ID+"/context", map[string]any{
		"version": 1, "references": []string{"f_missing"},
	}); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown reference = %d %s", rec.Code, rec.Body.String())
	}

	// A participant may update its own direction while its run holds it, and
	// nothing else.
	if _, _, err := p.Store.StartIntentRun(in.ID, "local", "", model.Run{}); err != nil {
		t.Fatal(err)
	}
	current, _ := p.Store.GetIntent(in.ID)
	ident := collab.Identity{
		ProjectID: p.ID, ParticipantID: collab.ParticipantID(in.ID),
		IntentID: in.ID, RunID: current.RunID, Generation: current.Attempts,
	}
	if rec := agentCall(t, s, ident, http.MethodPut, "/api/intents/"+in.ID+"/context", map[string]any{
		"version": current.ContextVersion, "references": []string{ref.ID},
	}); rec.Code != http.StatusOK {
		t.Fatalf("participant update = %d %s", rec.Code, rec.Body.String())
	}
	foreign := ident
	foreign.IntentID = "in_other"
	if rec := agentCall(t, s, foreign, http.MethodPut, "/api/intents/"+in.ID+"/context", map[string]any{
		"version": 2,
	}); rec.Code != http.StatusForbidden {
		t.Fatalf("foreign direction = %d %s", rec.Code, rec.Body.String())
	}
	staleRun := ident
	staleRun.RunID = "r_stale"
	if rec := agentCall(t, s, staleRun, http.MethodPut, "/api/intents/"+in.ID+"/context", map[string]any{
		"version": 2,
	}); rec.Code != http.StatusConflict {
		t.Fatalf("stale holder = %d %s", rec.Code, rec.Body.String())
	}
	// The edits are ledger state, not a message or a fact.
	if len(p.Store.Snapshot().Facts) != 1 {
		t.Fatal("context editing created a fact")
	}
}
