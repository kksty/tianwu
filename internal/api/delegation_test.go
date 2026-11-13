package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/project"
)

func TestReplacementTurnUpdatesWaitBeforeDispatch(t *testing.T) {
	for _, ready := range []bool{false, true} {
		name := "preserve_unfinished_wait"
		if ready {
			name = "consume_completed_wait"
		}
		t.Run(name, func(t *testing.T) {
			f := newParentFixture(t)
			ex := &blockingExec{}
			f.srv.Exec = ex
			t.Cleanup(func() { f.srv.CancelAll(); f.srv.WaitIdle(5 * time.Second) })
			request, err := f.srv.Store.RegisterOperatorWait(f.ident.IntentID, f.run.ID, model.OperatorWait{Request: "first login"})
			if err != nil {
				t.Fatal(err)
			}
			if ready {
				if err := f.srv.Store.CompleteOperatorWait(f.ident.IntentID, request.ID, "done"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.srv.Store.SettleIntentForRun(f.ident.IntentID, f.run.ID, model.RunDone); err != nil {
				t.Fatal(err)
			}
			if _, err := f.srv.Store.UpdateRun(f.run.ID, func(r *model.Run) { r.Status = model.RunDone }); err != nil {
				t.Fatal(err)
			}
			if _, err := f.srv.Store.UpdateIntent(f.ident.IntentID, func(in *model.Intent) { in.Status = model.StatusPending }); err != nil {
				t.Fatal(err)
			}
			runID, err := f.srv.startRunWith(f.ident.IntentID, "continue with handoff", "", false, true)
			if err != nil {
				t.Fatal(err)
			}
			waitFor(t, 5*time.Second, func() bool { return len(ex.requests()) == 1 })
			in := f.parent(t)
			if ready {
				if in.Wait != nil {
					t.Fatal("completed wait is still attached to the replacement turn")
				}
				if _, err := f.srv.Store.RegisterOperatorWait(in.ID, runID, model.OperatorWait{Request: "second login"}); err != nil {
					t.Fatal(err)
				}
			} else if in.Wait == nil || in.Wait.Operator.ID != request.ID || in.Wait.RegisteredByRun != runID {
				t.Fatalf("unfinished wait was not rebound before dispatch: %+v", in.Wait)
			}
		})
	}
}

func TestWaitRejectsInvalidJSONWithoutRegistering(t *testing.T) {
	for _, body := range []string{`{"timeout_seconds":"oops"}`, `{"mode":"all",`, `{"children":42}`} {
		t.Run(body, func(t *testing.T) {
			f := newParentFixture(t)
			if _, _, err := f.p.Store.CreateChildIntent(f.ident.IntentID, model.Intent{Title: "child"}); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("POST", "/api/intents/"+f.ident.IntentID+"/wait", strings.NewReader(body))
			req = req.WithContext(WithIdentity(req.Context(), f.ident))
			rec := httptest.NewRecorder()
			f.srv.AgentHandler().ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest || f.parent(t).Wait != nil {
				t.Fatalf("invalid JSON changed wait: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestWaitClampsHugeTimeoutBeforeDurationConversion(t *testing.T) {
	f := newParentFixture(t)
	if _, _, err := f.p.Store.CreateChildIntent(f.ident.IntentID, model.Intent{Title: "child"}); err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	rec := f.call(t, &f.ident, "POST", "/api/intents/"+f.ident.IntentID+"/wait", map[string]any{"timeout_seconds": int(^uint(0) >> 1)})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("wait: %d %s", rec.Code, rec.Body.String())
	}
	w := f.parent(t).Wait
	if w == nil || w.Deadline == nil || w.Deadline.Before(before.Add(maxWaitTimeout)) || w.Deadline.After(time.Now().Add(maxWaitTimeout)) {
		t.Fatalf("timeout was not clamped to 7 days: %+v", w)
	}
}

type parentFixture struct {
	srv   *Server
	p     *project.Project
	run   model.Run
	ident collab.Identity
}

func newParentFixture(t *testing.T) *parentFixture {
	t.Helper()
	s, p := newBindingServer(t)
	if err := p.SetNodeBinding([]string{"local"}, "local"); err != nil {
		t.Fatal(err)
	}
	s.LocalNode = "local"
	s.Collab = collab.NewRegistry(time.Hour)
	s.MaxRuns = 0
	s.Exec = nodeExec{}
	parent, err := p.Store.CreateIntent(model.Intent{Title: "父方向", Description: "父", Doc: "操作员授权范围"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := p.Store.AddRun(model.Run{IntentID: parent.ID, Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Store.UpdateIntent(parent.ID, func(i *model.Intent) {
		i.Status = model.StatusRunning
		i.RunID = run.ID
		i.Attempts = 1
	}); err != nil {
		t.Fatal(err)
	}
	parent, _ = p.Store.GetIntent(parent.ID)
	ident, err := s.Collab.Issue(collab.Identity{
		ProjectID: p.ID, ParticipantID: collab.ParticipantID(parent.ID),
		IntentID: parent.ID, RunID: run.ID, Generation: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &parentFixture{srv: s, p: p, run: run, ident: ident}
}

func (f *parentFixture) call(t *testing.T, id *collab.Identity, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	if id != nil {
		req = req.WithContext(WithIdentity(req.Context(), *id))
	}
	rec := httptest.NewRecorder()
	f.srv.AgentHandler().ServeHTTP(rec, req)
	return rec
}

func (f *parentFixture) parent(t *testing.T) model.Intent {
	t.Helper()
	in, ok := f.p.Store.GetIntent(f.ident.IntentID)
	if !ok {
		t.Fatal("parent missing")
	}
	return in
}

// waitNoRunning lets fixture goroutines finish writing before TempDir cleanup.
func waitNoRunning(t *testing.T, p *project.Project) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		running := false
		for _, r := range p.Store.Runs() {
			if r.Status == model.RunRunning {
				running = true
				break
			}
		}
		if !running {
			time.Sleep(20 * time.Millisecond)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("a run never settled")
}

func TestDelegateChildFromOwnRun(t *testing.T) {
	f := newParentFixture(t)
	f.srv.Profiles = func() ([]ProfileInfo, string, error) {
		return []ProfileInfo{{ID: "fast", Provider: "openai", Model: "fast-model", Revision: "rev", Runnable: true}}, "fast", nil
	}
	rec := f.call(t, &f.ident, http.MethodPost, "/api/intents/"+f.ident.IntentID+"/children", map[string]any{
		"title": "子方向", "description": "查 A", "profile": "fast",
		"from_fact_ids": []string{}, "idempotency_key": "k1",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("delegate: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Child   childIntentView `json:"child"`
		Created bool            `json:"created"`
		Started bool            `json:"started"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Created || resp.Child.ParentID != f.ident.IntentID || resp.Child.Title != "子方向" {
		t.Fatalf("resp = %+v", resp)
	}
	child, _ := f.p.Store.GetIntent(resp.Child.ID)
	if child.ParentID != f.ident.IntentID || child.CreatedBy != f.ident.ParticipantID ||
		child.DelegatedByRun != f.ident.RunID || child.Doc != "操作员授权范围" || child.Profile != "fast" {
		t.Fatalf("child = %+v", child)
	}
	// The same key is one child, not two.
	rec = f.call(t, &f.ident, http.MethodPost, "/api/intents/"+f.ident.IntentID+"/children", map[string]any{
		"title": "子方向", "description": "查 A", "profile": "fast", "idempotency_key": "k1",
	})
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"created":false`)) {
		t.Fatalf("retry: %d %s", rec.Code, rec.Body.String())
	}
	if got := f.p.Store.Children(f.ident.IntentID); len(got) != 1 {
		t.Fatalf("children = %d", len(got))
	}
	// The fixture's parent run was never dispatched; settle it so the helper
	// only waits for the real child run.
	_, _ = f.p.Store.UpdateRun(f.run.ID, func(r *model.Run) {
		now := time.Now()
		r.Status = model.RunDone
		r.EndedAt = &now
	})
	waitNoRunning(t, f.p)
}

func TestDelegateFromAnotherRunIsRefused(t *testing.T) {
	f := newParentFixture(t)
	other := f.ident
	other.IntentID = "in_other"
	other.ParticipantID = collab.ParticipantID("in_other")
	rec := f.call(t, &other, http.MethodPost, "/api/intents/"+f.ident.IntentID+"/children", map[string]any{"title": "x"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("foreign delegate: %d %s", rec.Code, rec.Body.String())
	}
	// A stale run id cannot delegate either: it is a different generation.
	stale := f.ident
	stale.RunID = "r_stale"
	rec = f.call(t, &stale, http.MethodPost, "/api/intents/"+f.ident.IntentID+"/children", map[string]any{"title": "x"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale delegate: %d %s", rec.Code, rec.Body.String())
	}
}

func TestDelegateQueuesAtCapacityThenSchedulerStarts(t *testing.T) {
	f := newParentFixture(t)
	f.srv.MaxRuns = 1 // the parent occupies the only slot
	rec := f.call(t, &f.ident, http.MethodPost, "/api/intents/"+f.ident.IntentID+"/children", map[string]any{"title": "子方向"})
	if rec.Code != http.StatusCreated || !bytes.Contains(rec.Body.Bytes(), []byte(`"started":false`)) {
		t.Fatalf("delegate: %d %s", rec.Code, rec.Body.String())
	}
	child := f.p.Store.Children(f.ident.IntentID)[0]
	if child.Status != model.StatusPending {
		t.Fatalf("child status = %s", child.Status)
	}
	// Free the slot: the scheduler must pick the queued child up.
	if _, err := f.p.Store.UpdateRun(f.run.ID, func(r *model.Run) {
		now := time.Now()
		r.Status = model.RunDone
		r.EndedAt = &now
	}); err != nil {
		t.Fatal(err)
	}
	f.srv.pump()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		cur, _ := f.p.Store.GetIntent(child.ID)
		if cur.Status != model.StatusPending {
			waitNoRunning(t, f.p)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("queued child was never started after a slot freed")
}

func TestWaitReadyAndWaitRegistered(t *testing.T) {
	f := newParentFixture(t)
	f.srv.MaxRuns = 1 // keep the delegated child unstarted so the wait registers
	rec := f.call(t, &f.ident, http.MethodPost, "/api/intents/"+f.ident.IntentID+"/children", map[string]any{"title": "子方向"})
	var resp struct {
		Child childIntentView `json:"child"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	// Child still open: the wait is registered.
	rec = f.call(t, &f.ident, http.MethodPost, "/api/intents/"+f.ident.IntentID+"/wait", map[string]any{
		"children": []string{resp.Child.ID}, "mode": "all", "timeout_seconds": 60,
	})
	if rec.Code != http.StatusAccepted || !bytes.Contains(rec.Body.Bytes(), []byte(`"waiting":true`)) {
		t.Fatalf("wait: %d %s", rec.Code, rec.Body.String())
	}
	parent := f.parent(t)
	if parent.Wait == nil || len(parent.Wait.Children) != 1 {
		t.Fatalf("parent wait = %+v", parent.Wait)
	}

	// A child that settles before registration makes the wait ready immediately.
	f2 := newParentFixture(t)
	f2.srv.MaxRuns = 1
	rec = f2.call(t, &f2.ident, http.MethodPost, "/api/intents/"+f2.ident.IntentID+"/children", map[string]any{"title": "已完成子任务"})
	var resp2 struct {
		Child childIntentView `json:"child"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp2); err != nil {
		t.Fatal(err)
	}
	if _, err := f2.p.Store.UpdateIntent(resp2.Child.ID, func(i *model.Intent) { i.Status = model.StatusDone }); err != nil {
		t.Fatal(err)
	}
	rec = f2.call(t, &f2.ident, http.MethodPost, "/api/intents/"+f2.ident.IntentID+"/wait", map[string]any{
		"children": []string{resp2.Child.ID}, "mode": "all",
	})
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"ready":true`)) {
		t.Fatalf("ready wait: %d %s", rec.Code, rec.Body.String())
	}
	if parent := f2.parent(t); parent.Wait != nil {
		t.Fatalf("ready wait was stored: %+v", parent.Wait)
	}
}

func TestWaitRejectsForeignChild(t *testing.T) {
	f := newParentFixture(t)
	other, err := f.p.Store.CreateIntent(model.Intent{Title: "别的"})
	if err != nil {
		t.Fatal(err)
	}
	rec := f.call(t, &f.ident, http.MethodPost, "/api/intents/"+f.ident.IntentID+"/wait", map[string]any{
		"children": []string{other.ID}, "mode": "all",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("foreign wait: %d %s", rec.Code, rec.Body.String())
	}
}

func TestFinishTurnsParentIntoWaitingThenWakes(t *testing.T) {
	f := newParentFixture(t)
	f.srv.MaxRuns = 1 // keep the child open until the parent yields
	rec := f.call(t, &f.ident, http.MethodPost, "/api/intents/"+f.ident.IntentID+"/children", map[string]any{"title": "子方向"})
	var resp struct {
		Child childIntentView `json:"child"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if rec := f.call(t, &f.ident, http.MethodPost, "/api/intents/"+f.ident.IntentID+"/wait", map[string]any{
		"children": []string{resp.Child.ID}, "mode": "all",
	}); rec.Code != http.StatusAccepted {
		t.Fatalf("wait: %d %s", rec.Code, rec.Body.String())
	}
	// The parent's turn ends while the child is open: it must become waiting,
	// not done.
	f.srv.finish(f.run.ID, f.ident.IntentID, model.RunDone, "", executor.Result{}, 0)
	parent := f.parent(t)
	if parent.Status != model.StatusWaiting {
		t.Fatalf("parent status = %s, want waiting", parent.Status)
	}
	// The child settles: the scheduler wakes the parent with a new run.
	if _, err := f.p.Store.UpdateIntent(resp.Child.ID, func(i *model.Intent) { i.Status = model.StatusDone }); err != nil {
		t.Fatal(err)
	}
	child, _ := f.p.Store.GetIntent(resp.Child.ID)
	f.srv.afterIntentSettled(resp.Child.ID, child.RunID, model.RunDone)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		cur, _ := f.p.Store.GetIntent(f.ident.IntentID)
		if cur.Wait == nil && cur.Status != model.StatusWaiting {
			waitNoRunning(t, f.p)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("parent not woken: %+v", parent)
}

// Capacity 1: the parent must yield (waiting has no run) so the child can run,
// and the child's completion must wake the parent. This is the deadlock case
// docs/98 §17 names explicitly.
func TestSingleSlotParentWaitDoesNotDeadlock(t *testing.T) {
	f := newParentFixture(t)
	f.srv.MaxRuns = 1
	rec := f.call(t, &f.ident, http.MethodPost, "/api/intents/"+f.ident.IntentID+"/children", map[string]any{"title": "子方向"})
	var resp struct {
		Child childIntentView `json:"child"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if rec := f.call(t, &f.ident, http.MethodPost, "/api/intents/"+f.ident.IntentID+"/wait", map[string]any{
		"children": []string{resp.Child.ID}, "mode": "all",
	}); rec.Code != http.StatusAccepted {
		t.Fatalf("wait: %d %s", rec.Code, rec.Body.String())
	}
	f.srv.finish(f.run.ID, f.ident.IntentID, model.RunDone, "", executor.Result{}, 0)
	if cur := f.parent(t); cur.Status != model.StatusWaiting {
		t.Fatalf("parent status = %s", cur.Status)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		parent, _ := f.p.Store.GetIntent(f.ident.IntentID)
		runs := 0
		for _, r := range f.p.Store.Runs() {
			if r.IntentID == f.ident.IntentID {
				runs++
			}
		}
		if runs >= 2 && parent.Wait == nil && parent.Status != model.StatusWaiting {
			waitNoRunning(t, f.p)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	parent, _ := f.p.Store.GetIntent(f.ident.IntentID)
	t.Fatalf("parent was not woken after the child ran: %+v", parent)
}

// After a restart, a waiting parent whose children already settled must be
// woken from the ledger alone; the wait (and results) are durable.
func TestRecoverWakesSatisfiedWait(t *testing.T) {
	f := newParentFixture(t)
	child, _, err := f.p.Store.CreateChildIntent(f.ident.IntentID, model.Intent{Title: "已完成", CreatedBy: f.ident.ParticipantID})
	if err != nil {
		t.Fatal(err)
	}
	// Register while the child is still open, then let it settle: the wait must
	// be stored first, otherwise RegisterWait correctly reports ready and stores
	// nothing (there is nothing to recover).
	if _, err := f.srv.Store.RegisterWait(f.ident.IntentID, f.run.ID, model.Wait{
		Children: []string{child.ID}, Mode: model.WaitAll,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Store.UpdateIntent(child.ID, func(i *model.Intent) { i.Status = model.StatusDone }); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Store.UpdateIntent(f.ident.IntentID, func(i *model.Intent) {
		i.Status = model.StatusWaiting
		i.RunID = f.run.ID
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Store.UpdateRun(f.run.ID, func(r *model.Run) {
		now := time.Now()
		r.Status = model.RunDone
		r.EndedAt = &now
	}); err != nil {
		t.Fatal(err)
	}
	f.srv.Recover()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		parent, _ := f.p.Store.GetIntent(f.ident.IntentID)
		runs := 0
		for _, r := range f.p.Store.Runs() {
			if r.IntentID == f.ident.IntentID {
				runs++
			}
		}
		if runs >= 2 && parent.Wait == nil && parent.Status != model.StatusWaiting {
			waitNoRunning(t, f.p)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("satisfied wait was not recovered")
}

// The doc's completion case: two children on two profiles, one slot, parent
// waits, both results land, parent resumes.
func TestTwoChildrenOnDifferentProfilesWithOneSlot(t *testing.T) {
	f := newParentFixture(t)
	f.srv.MaxRuns = 1
	f.srv.Profiles = func() ([]ProfileInfo, string, error) {
		return []ProfileInfo{
			{ID: "fast", Provider: "openai", Model: "fast-model", Revision: "rev-fast", Runnable: true},
			{ID: "slow", Provider: "anthropic", Model: "slow-model", Revision: "rev-slow", Runnable: true},
		}, "fast", nil
	}
	var ids []string
	for i, profile := range []string{"fast", "slow"} {
		rec := f.call(t, &f.ident, http.MethodPost, "/api/intents/"+f.ident.IntentID+"/children", map[string]any{
			"title": "子任务" + profile, "profile": profile,
			"idempotency_key": "child-" + string(rune('a'+i)),
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("delegate %s: %d %s", profile, rec.Code, rec.Body.String())
		}
		var resp struct {
			Child childIntentView `json:"child"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, resp.Child.ID)
	}
	for i, want := range []string{"fast", "slow"} {
		got, _ := f.p.Store.GetIntent(ids[i])
		if got.Profile != want {
			t.Fatalf("child %d profile = %q, want %q", i, got.Profile, want)
		}
	}
	if rec := f.call(t, &f.ident, http.MethodPost, "/api/intents/"+f.ident.IntentID+"/wait", map[string]any{
		"children": ids, "mode": "all",
	}); rec.Code != http.StatusAccepted {
		t.Fatalf("wait: %d %s", rec.Code, rec.Body.String())
	}
	f.srv.finish(f.run.ID, f.ident.IntentID, model.RunDone, "", executor.Result{}, 0)
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		done := true
		for _, id := range ids {
			child, _ := f.p.Store.GetIntent(id)
			if child.Status != model.StatusDone {
				done = false
				break
			}
		}
		parent, _ := f.p.Store.GetIntent(f.ident.IntentID)
		if done && parent.Wait == nil && parent.Status != model.StatusWaiting {
			waitNoRunning(t, f.p)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, id := range ids {
		child, _ := f.p.Store.GetIntent(id)
		t.Logf("child %s: %s", child.Title, child.Status)
	}
	parent, _ := f.p.Store.GetIntent(f.ident.IntentID)
	t.Fatalf("parent not resumed: %+v", parent)
}

func TestCancelParentStopsSubtree(t *testing.T) {
	f := newParentFixture(t)
	// Cancel is a pending-intent operation; the fixture's run was never
	// registered with the dispatcher, so put the parent back to pending first.
	if _, err := f.p.Store.UpdateIntent(f.ident.IntentID, func(i *model.Intent) {
		i.Status = model.StatusPending
		i.RunID = ""
	}); err != nil {
		t.Fatal(err)
	}
	child, _, err := f.p.Store.CreateChildIntent(f.ident.IntentID, model.Intent{Title: "子方向", CreatedBy: f.ident.ParticipantID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.srv.CancelIntent(f.ident.IntentID, "测试取消"); err != nil {
		t.Fatal(err)
	}
	got, _ := f.p.Store.GetIntent(child.ID)
	if got.Status != model.StatusFailed || !got.Aborted || !got.Hold {
		t.Fatalf("child = %+v", got)
	}
	parent := f.parent(t)
	if !parent.Hold || parent.Status != model.StatusPending {
		t.Fatalf("parent = %+v", parent)
	}
}

func TestRecoverFailsChildRunningAtCrash(t *testing.T) {
	f := newParentFixture(t)
	child, _, err := f.p.Store.CreateChildIntent(f.ident.IntentID, model.Intent{Title: "子方向", CreatedBy: f.ident.ParticipantID})
	if err != nil {
		t.Fatal(err)
	}
	childRun, err := f.p.Store.AddRun(model.Run{IntentID: child.ID, Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Store.UpdateIntent(child.ID, func(i *model.Intent) {
		i.Status = model.StatusRunning
		i.RunID = childRun.ID
		i.Attempts = 1
	}); err != nil {
		t.Fatal(err)
	}
	f.srv.Recover()
	got, _ := f.p.Store.GetIntent(child.ID)
	if got.Status != model.StatusFailed {
		t.Fatalf("child after recover = %s, want failed", got.Status)
	}
}
