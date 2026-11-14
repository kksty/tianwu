package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kksty/tianwu/internal/model"
)

// The board's handoff_warnings are per-run text. A report or verification run
// is operator-side post-processing, so its warnings must not reach a harness
// session's board: they would name a support run and leak its outcome
// (docs/01-architecture.md §8).
func TestBoardHandoffWarningsHideSupportRunsFromSessions(t *testing.T) {
	s, _ := newBindingServer(t)

	dirRun, err := s.Store.AddRun(model.Run{IntentID: "in_direction"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.UpdateRun(dirRun.ID, func(r *model.Run) {
		r.Warnings = []string{"方向 run 的交接告警"}
	}); err != nil {
		t.Fatal(err)
	}
	verifyRun, err := s.Store.AddRun(model.Run{IntentID: "in_direction", Purpose: "verifier"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.UpdateRun(verifyRun.ID, func(r *model.Run) {
		r.Warnings = []string{"复核 run 的交接告警"}
	}); err != nil {
		t.Fatal(err)
	}

	decode := func(req *http.Request) []map[string]any {
		t.Helper()
		rec := serveMux(t, s, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("board = %d: %s", rec.Code, rec.Body.String())
		}
		var body struct {
			HandoffWarnings []map[string]any `json:"handoff_warnings"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode board: %v (%s)", err, rec.Body.String())
		}
		return body.HandoffWarnings
	}
	ids := func(rows []map[string]any) map[string]bool {
		out := map[string]bool{}
		for _, row := range rows {
			if id, ok := row["run"].(string); ok {
				out[id] = true
			}
		}
		return out
	}

	commander := ids(decode(harnessRequest(http.MethodGet, "/api/board")))
	if commander[verifyRun.ID] {
		t.Fatalf("commander board leaked a verifier run warning: %v", commander)
	}
	if !commander[dirRun.ID] {
		t.Fatalf("commander board dropped the direction run warning: %v", commander)
	}

	operator := ids(decode(httptest.NewRequest(http.MethodGet, "/api/board", nil)))
	if !operator[dirRun.ID] || !operator[verifyRun.ID] {
		t.Fatalf("operator board must keep every run warning: %v", operator)
	}
}
