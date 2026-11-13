package api

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// ---- cancel semantics: explicit stop => failed+Hold (task-31, final-e2e E7) ----

// A run-level cancel (the API's "kill this run") used to settle the intent back
// to pending, and the pump immediately started a replacement run. An explicit
// stop must hold the direction: only retry/run clears it.
func TestCancelRunHoldsIntentAndPreventsRedispatch(t *testing.T) {
	s, _, in := newSteerServer(t)
	live, _ := s.Store.GetIntent(in.ID)
	if err := s.CancelRun(live.RunID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		got, ok := s.Store.GetIntent(in.ID)
		return ok && got.Status == model.StatusFailed && got.Hold
	})
	if runs := s.Store.Runs(); len(runs) != 1 {
		t.Fatalf("cancel produced %d runs, want 1", len(runs))
	}
	// A scheduler tick must not resurrect the canceled direction.
	s.pump()
	time.Sleep(200 * time.Millisecond)
	if runs := s.Store.Runs(); len(runs) != 1 {
		t.Fatalf("pump re-dispatched a canceled direction: %d runs", len(runs))
	}
	// Retry is the explicit way back: it clears Hold and starts exactly one run.
	runID, err := s.RetryIntent(in.ID, "取消后显式重试", false)
	if err != nil || runID == "" {
		t.Fatalf("retry = %q, %v", runID, err)
	}
	got, _ := s.Store.GetIntent(in.ID)
	if got.Hold || got.Status != model.StatusRunning && got.Status != model.StatusClaimed {
		t.Fatalf("intent after retry = %+v", got)
	}
	if runs := s.Store.Runs(); len(runs) != 2 {
		t.Fatalf("retry runs = %d, want 2", len(runs))
	}
	// Let the retried blocking run reach its cancel registration, then stop it
	// and wait for the settle so the case directory is not removed under an
	// open run log (Windows).
	waitFor(t, 5*time.Second, func() bool {
		return s.Lifecycle.RunActive(runID)
	})
	if err := s.CancelRun(runID); err != nil {
		t.Fatalf("cancel retried run: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool {
		r, ok := s.Store.GetRun(runID)
		return ok && r.Status == model.RunCanceled
	})
	time.Sleep(150 * time.Millisecond)
}

// Canceling a child run while its parent waits: the child is held (not silently
// re-dispatched) and the parent's wait still settles so the parent can decide.
func TestCancelWaitingChildSettlesParentWait(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, &blockingExec{}, dir, filepath.Join(dir, "cases"))
	t.Cleanup(func() {
		s.CancelAll()
		s.WaitIdle(5 * time.Second)
		time.Sleep(200 * time.Millisecond)
	})

	parent, err := st.CreateIntent(model.Intent{Title: "等子任务的父方向"})
	if err != nil {
		t.Fatal(err)
	}
	child, _, err := st.CreateChildIntent(parent.ID, model.Intent{Title: "子方向", CreatedBy: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	parentRun, err := st.AddRun(model.Run{IntentID: parent.ID, Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIntent(parent.ID, func(i *model.Intent) {
		i.Status = model.StatusWaiting
		i.RunID = parentRun.ID
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RegisterWait(parent.ID, parentRun.ID, model.Wait{Children: []string{child.ID}}); err != nil {
		t.Fatal(err)
	}

	childRun, err := s.StartRun(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		return s.Lifecycle.RunActive(childRun)
	})
	if err := s.CancelRun(childRun); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		got, ok := st.GetIntent(child.ID)
		return ok && got.Status == model.StatusFailed && got.Hold
	})

	// The parent's wait must not hang on a held child: a failed child counts as
	// settled, so the scheduler wakes the parent with the result.
	s.pump()
	waitFor(t, 5*time.Second, func() bool {
		got, ok := st.GetIntent(parent.ID)
		return ok && got.Wait == nil && got.Status != model.StatusWaiting
	})
	// And the held child is not silently restarted by that same tick.
	runsForChild := 0
	for _, r := range st.Runs() {
		if r.IntentID == child.ID {
			runsForChild++
		}
	}
	if runsForChild != 1 {
		t.Fatalf("canceled child has %d runs, want 1 (no auto re-dispatch)", runsForChild)
	}
}

// Terminal states keep their old answers: canceling a finished direction is a
// 409 (intent API) and killing a finished run is an error, not a state change.
func TestCancelTerminalStatesStayCompatible(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, &blockingExec{}, dir, filepath.Join(dir, "cases"))

	in, err := st.CreateIntent(model.Intent{Title: "已经做完的方向"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIntent(in.ID, func(i *model.Intent) { i.Status = model.StatusDone }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelIntent(in.ID, "再取消一次"); err == nil || !strings.Contains(err.Error(), "已经是 done") {
		t.Fatalf("cancel done intent = %v", err)
	}
	w := call(t, s, http.MethodPost, "/api/intents/"+in.ID+"/cancel", map[string]string{"reason": "再取消一次"})
	if w.Code != http.StatusConflict {
		t.Fatalf("cancel done intent HTTP = %d %s", w.Code, w.Body.String())
	}

	run, err := st.AddRun(model.Run{IntentID: in.ID, Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateRun(run.ID, func(r *model.Run) { r.Status = model.RunDone }); err != nil {
		t.Fatal(err)
	}
	if err := s.CancelRun(run.ID); err == nil {
		t.Fatal("canceling a finished run must be an error")
	}
	if got, _ := st.GetIntent(in.ID); got.Status != model.StatusDone || got.Hold {
		t.Fatalf("terminal intent changed: %+v", got)
	}
}
