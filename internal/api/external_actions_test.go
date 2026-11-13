package api

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

type externalWaitExecutor struct {
	calls   atomic.Int32
	started chan executor.Request
}

func (e *externalWaitExecutor) Name() string { return "external-wait-fixture" }
func (e *externalWaitExecutor) Run(ctx context.Context, req executor.Request, _ func(executor.Event)) (executor.Result, error) {
	n := e.calls.Add(1)
	e.started <- req
	if n == 1 {
		<-ctx.Done()
		return executor.Result{}, ctx.Err()
	}
	return executor.Result{ExitCode: 0, StopReason: "completed"}, nil
}

func TestExternalResultOnlyResumesItsWaiterAfterExplicitProjectResume(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	ex := &externalWaitExecutor{started: make(chan executor.Request, 4)}
	s := New(st, ex, dir, filepath.Join(dir, "cases"))
	t.Cleanup(func() { s.SetPaused(true); s.CancelAll(); s.WaitIdle(3 * time.Second) })
	owner, err := st.CreateIntent(model.Intent{Title: "wait for external result"})
	if err != nil {
		t.Fatal(err)
	}
	runID, err := s.StartRun(owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-ex.started:
	case <-time.After(time.Second):
		t.Fatal("first turn did not start")
	}
	rec := call(t, s, http.MethodPost, "/api/actions", map[string]string{"intent_id": owner.ID, "idempotency_key": "job-1", "name": "external operation", "reference": "job:fixture"})
	if rec.Code != 201 {
		t.Fatalf("begin=%d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Action model.ExternalAction `json:"action"`
	}
	if err = json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	a := created.Action
	// An unrelated action/wait must remain untouched by the receipt for a.
	other, _ := st.CreateIntent(model.Intent{Title: "unrelated waiter"})
	_, otherRun, err := st.StartIntentRun(other.ID, "local", "", model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := st.BeginAction(other.ID, otherRun.ID, "other", "different operation", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.RegisterWait(other.ID, otherRun.ID, model.Wait{Actions: []string{b.ID}}); err != nil {
		t.Fatal(err)
	}
	_, _ = st.UpdateRun(otherRun.ID, func(r *model.Run) { r.Status = model.RunCanceled; r.CancelCause = model.CancelCauseActionWait })
	if _, err = st.SettleIntentForRun(other.ID, otherRun.ID, model.RunCanceled, model.CancelCauseActionWait); err != nil {
		t.Fatal(err)
	}
	if rec = call(t, s, http.MethodPost, "/api/intents/"+owner.ID+"/wait", map[string]any{"actions": []string{a.ID}}); rec.Code != 202 {
		t.Fatalf("wait=%d %s", rec.Code, rec.Body.String())
	}
	waitFor(t, 3*time.Second, func() bool {
		in, _ := st.GetIntent(owner.ID)
		r, _ := st.GetRun(runID)
		return in.Status == model.StatusWaiting && r.Status == model.RunCanceled
	})
	if rec = call(t, s, http.MethodPost, "/api/project/pause", nil); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if rec = call(t, s, http.MethodPost, "/api/actions/"+a.ID+"/result", map[string]any{"version": a.Version, "state": "completed", "result": "external-result-fixture"}); rec.Code != 200 {
		t.Fatalf("result=%d %s", rec.Code, rec.Body.String())
	}
	if ex.calls.Load() != 1 {
		t.Fatal("receipt dispatched while paused")
	}
	if got, _ := st.GetIntent(owner.ID); !got.Wait.WakePending {
		t.Fatal("paused project lost receipt")
	}
	if got, _ := st.GetIntent(other.ID); got.Wait.WakePending {
		t.Fatal("unrelated waiter woke")
	}
	if rec = call(t, s, http.MethodPost, "/api/project/resume", nil); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	select {
	case req := <-ex.started:
		if req.IntentID != owner.ID || !strings.Contains(req.Prompt, "external-result-fixture") || !strings.Contains(req.Prompt, a.ID) {
			t.Fatalf("wrong recipient/context=%+v", req)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("explicit resume did not deliver receipt")
	}
	waitFor(t, 3*time.Second, func() bool { in, _ := st.GetIntent(owner.ID); return in.Status == model.StatusDone })
	if ex.calls.Load() != 2 {
		t.Fatalf("model calls=%d", ex.calls.Load())
	}
	if rec = call(t, s, http.MethodPost, "/api/actions/"+a.ID+"/result", map[string]any{"version": a.Version, "state": "completed", "result": "duplicate"}); rec.Code != 409 {
		t.Fatalf("duplicate=%d", rec.Code)
	}
	if len(st.ListFacts("", 0)) != 0 {
		t.Fatal("external receipt was promoted to a fact")
	}
}

func TestParticipantCannotResolveAnotherDirectionsExternalAction(t *testing.T) {
	f := newAgentFixture(t)
	f.makeLive(t)
	live, _ := f.p.Store.GetIntent(f.target.ID)
	a, _, err := f.p.Store.BeginAction(live.ID, live.RunID, "key", "external operation", "")
	if err != nil {
		t.Fatal(err)
	}
	rec := f.call(t, f.srcID, http.MethodPost, "/api/actions/"+a.ID+"/result", map[string]any{"version": 1, "state": "completed", "result": "foreign result"})
	if rec.Code != 403 {
		t.Fatalf("foreign result=%d %s", rec.Code, rec.Body.String())
	}
}
