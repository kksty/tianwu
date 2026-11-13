package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/board"
	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/retrieval"
)

// This executor only writes local synthetic observations. The test validates
// actual ledger/dispatch/review/goal handlers, not model strategy or real-world
// penetration effectiveness; the independent review oracle is also a fixture.
type isolatedWorkflowExec struct{}

func (isolatedWorkflowExec) Name() string { return "isolated-workflow" }
func (isolatedWorkflowExec) Run(_ context.Context, req executor.Request, emit func(executor.Event)) (executor.Result, error) {
	if err := os.MkdirAll(filepath.Join(req.WorkDir, ".tw"), 0700); err != nil {
		return executor.Result{}, err
	}
	emit(executor.Event{Kind: executor.KindToolCall, Tool: "fixture_write"})
	if err := os.WriteFile(filepath.Join(req.WorkDir, "proof.txt"), []byte("isolated proof: "+req.IntentID), 0600); err != nil {
		return executor.Result{}, err
	}
	body, _ := json.Marshal(map[string]any{"kind": "vuln", "declaration_id": "output", "title": "isolated result " + req.IntentID, "summary": "fixture observation", "evidence": []string{"proof.txt"}})
	if err := os.WriteFile(filepath.Join(req.WorkDir, ".tw", "facts.jsonl"), append(body, '\n'), 0600); err != nil {
		return executor.Result{}, err
	}
	emit(executor.Event{Kind: executor.KindToolResult, Tool: "fixture_write", OK: true})
	return executor.Result{StopReason: "completed"}, nil
}

func TestIsolatedLongHorizonGraphAndRecovery(t *testing.T) {
	for _, scenario := range []string{"pause_append", "stale_receipt", "revalidation"} {
		t.Run(scenario, func(t *testing.T) {
			s, p, early, _ := proofPolicyFixture(t)
			s.Exec = isolatedWorkflowExec{}
			s.SetAutoVerify(true)
			advance := func(title string, from []string) model.Fact {
				t.Helper()
				in, err := p.Store.CreateIntent(model.Intent{Title: title, Description: "local fixture only", FromFactIDs: from})
				if err != nil {
					t.Fatal(err)
				}
				s.SchedulePendingReviews()
				waitFor(t, 5*time.Second, func() bool { return s.readiness(p.Store.Snapshot(), in, s.premisePolicy()).Ready })
				id, err := s.StartRun(in.ID)
				if err != nil {
					t.Fatal(err)
				}
				waitFor(t, 5*time.Second, func() bool { r, _ := p.Store.GetRun(id); return r.Status == model.RunDone })
				var out model.Fact
				waitFor(t, 5*time.Second, func() bool {
					f, ok := p.Store.FindDeclaration(in.ID, "", "output")
					out = f
					return ok && f.Status == "verified"
				})
				return out
			}
			var branches []string
			for i := 0; i < 3; i++ {
				branches = append(branches, advance(fmt.Sprintf("branch-%d", i), []string{early.ID}).ID)
			}
			for i := 0; i < 70; i++ {
				if _, err := p.Store.AddFact(model.Fact{IntentID: early.IntentID, Kind: "note", Title: fmt.Sprintf("unrelated-%d", i)}); err != nil {
					t.Fatal(err)
				}
			}
			var last model.Fact
			for i := 0; i < 5; i++ {
				last = advance(fmt.Sprintf("stage-%d", i), branches)
				branches = []string{last.ID}
			}
			probe, _ := p.Store.CreateIntent(model.Intent{Title: "reuse early observation", FromFactIDs: []string{early.ID}, Hold: true})
			db := p.Store.Snapshot()
			rows, err := retrieval.FocusItems(db, probe.ID, focusVersions(db))
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, row := range rows {
				if row.ID == early.ID && row.Temperature == retrieval.FocusHot {
					found = true
				}
			}
			if !found {
				t.Fatal("early prerequisite was lost behind unrelated observations")
			}
			s.SetAutoVerify(false)
			goal, err := p.Store.AddGoal(model.Goal{Statement: "local fixture goal"})
			if err != nil {
				t.Fatal(err)
			}
			response := call(t, s, "POST", "/api/goals/"+goal.ID+"/complete", map[string]any{"from": []string{last.ID}, "note": "independent fixture proof"})
			if response.Code != 200 {
				t.Fatal(response.Body.String())
			}
			original := p.Store.Goals()[0]
			switch scenario {
			case "pause_append":
				s.AutoPause("local fixture complete")
				body := map[string]any{"statement": "follow-up fixture goal", "idempotency_key": "stable-follow-up"}
				for i := 0; i < 2; i++ {
					if r := call(t, s, "POST", "/api/project/append-goal", body); r.Code != 200 {
						t.Fatal(r.Body.String())
					}
				}
				if len(p.Store.Goals()) != 2 || s.paused.Load() || !reflect.DeepEqual(original, p.Store.Goals()[0]) {
					t.Fatal("append/resume changed history or duplicated a goal")
				}
			case "stale_receipt", "revalidation":
				if _, err := p.Store.UpdateFact(last.ID, func(f *model.Fact) { f.Summary = "corrected fixture assertion" }); err != nil {
					t.Fatal(err)
				}
				if len(board.Build(p.Store, 20).Goals.NeedsReview) != 1 {
					t.Fatal("stale proof did not become an actionable review")
				}
				if scenario == "revalidation" {
					if _, err := s.Verifier.Run(context.Background(), last.ID); err != nil {
						t.Fatal(err)
					}
					if r := call(t, s, "POST", "/api/goals/"+goal.ID+"/reopen", map[string]any{}); r.Code != 200 {
						t.Fatal(r.Body.String())
					}
					if r := call(t, s, "POST", "/api/goals/"+goal.ID+"/complete", map[string]any{"from": []string{last.ID}, "note": "revalidated fixture"}); r.Code != 200 {
						t.Fatal(r.Body.String())
					}
					if p.Store.Goals()[0].ReceiptStale {
						t.Fatal("new completion still cites the old assertion")
					}
				}
			}
		})
	}
}
