package api

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// blockingExec stands in for a running harness: it records the request and then
// waits to be cancelled, which is exactly the state a steer arrives in.
type blockingExec struct {
	mu   sync.Mutex
	reqs []executor.Request
}

func (b *blockingExec) Name() string { return "blocking" }

func (b *blockingExec) Run(ctx context.Context, req executor.Request, emit func(executor.Event)) (executor.Result, error) {
	b.mu.Lock()
	b.reqs = append(b.reqs, req)
	b.mu.Unlock()
	<-ctx.Done()
	return executor.Result{ExitCode: -1, StopReason: "canceled"}, ctx.Err()
}

func (b *blockingExec) requests() []executor.Request {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]executor.Request{}, b.reqs...)
}

// CanResume makes the fake answer the API's "is there something to continue?"
// question the way a host does: the case has a trajectory on disk.
func (b *blockingExec) CanResume(node, workDir string) bool {
	return executor.HasSession(workDir)
}

// newSteerServer starts one intent, waits until its executor call is in flight,
// and returns everything a steer test needs.
func newSteerServer(t *testing.T) (*Server, *blockingExec, model.Intent) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	ex := &blockingExec{}
	s := New(st, ex, dir, filepath.Join(dir, "cases"))
	s.Timeout = 0

	in, err := st.CreateIntent(model.Intent{Title: "查 /admin 越权", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartRun(in.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		got, ok := st.GetIntent(in.ID)
		return ok && got.Status == model.StatusRunning
	})
	// The run is cancellable immediately; these tests also need an executor
	// request to inspect, so wait for the fake to enter Run separately.
	waitFor(t, 5*time.Second, func() bool {
		return len(ex.requests()) == 1
	})
	t.Cleanup(func() {
		s.CancelAll()
		s.WaitIdle(5 * time.Second)
	})
	return s, ex, in
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not reached in time")
}

func TestSteerWithoutTrajectoryIsRefused(t *testing.T) {
	s, _, in := newSteerServer(t)
	if _, err := s.SteerIntent(in.ID, "改用 GET 试试", "f_1"); err == nil {
		t.Fatal("steer succeeded without a trajectory: it would silently lose the note")
	} else if !strings.Contains(err.Error(), "轨迹") {
		t.Fatalf("error = %v", err)
	}
}

func TestSteerContinuesTheExistingRun(t *testing.T) {
	s, ex, in := newSteerServer(t)
	first := ex.requests()
	if len(first) != 1 || first[0].Resume {
		t.Fatalf("first dispatch = %+v", first)
	}
	// The running turn left a trajectory; that file is what makes a resume
	// possible at all.
	workDir := filepath.Join(s.CaseRoot, in.ID)
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executor.SessionPath(workDir), []byte(`{"seq":1}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	prevRun, _ := s.Store.GetIntent(in.ID)
	runID, err := s.SteerIntent(in.ID, "改成先试 GET /admin", "f_42")
	if err != nil {
		t.Fatal(err)
	}
	if runID == prevRun.RunID {
		t.Fatal("steer reused the killed run instead of starting a continuation")
	}
	waitFor(t, 5*time.Second, func() bool { return len(ex.requests()) == 2 })

	second := ex.requests()[1]
	if !second.Resume {
		t.Fatal("the continued turn was dispatched without Resume")
	}
	if !strings.Contains(second.Prompt, "改成先试 GET /admin") || !strings.Contains(second.Prompt, "f_42") {
		t.Fatalf("continuation prompt lost the note:\n%s", second.Prompt)
	}
	if strings.Contains(second.Prompt, "授权范围") {
		t.Fatal("the continuation re-sent the whole brief: that is the cost D42 avoids")
	}

	run, ok := s.Store.GetRun(runID)
	if !ok {
		t.Fatal("continuation run not recorded")
	}
	if run.ContinuedFrom != prevRun.RunID {
		t.Fatalf("continued_from = %q, want %q", run.ContinuedFrom, prevRun.RunID)
	}
	waitFor(t, 5*time.Second, func() bool {
		old, ok := s.Store.GetRun(prevRun.RunID)
		return ok && old.Status == model.RunCanceled
	})
	if old, _ := s.Store.GetRun(prevRun.RunID); old.CancelCause != model.CancelCauseSteer {
		t.Fatalf("steer cancel cause = %q, want %q", old.CancelCause, model.CancelCauseSteer)
	}
	if got, _ := s.Store.GetIntent(in.ID); got.SteerCount != 1 {
		t.Fatalf("steer_count = %d, want 1 (every steer spends the direction's budget)", got.SteerCount)
	}
}

func TestSteerRefusesWhenIntentIsNotRunning(t *testing.T) {
	s, _, _ := newSteerServer(t)
	in, err := s.Store.CreateIntent(model.Intent{Title: "没派过的方向"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SteerIntent(in.ID, "加一句", ""); err == nil {
		t.Fatal("steer accepted an intent that is not running")
	}
}

// A worker kill (or a subtree cancel) marks the direction aborted so its
// cancelled run settles failed for the parent's wait. That marking belongs to
// the cancelled turn, not to the direction: after a retry the next steer must
// work like any other. It used to stay sticky, so the steer's own cancel
// settled failed+Hold and SteerIntent waited 30s for a pending state that never
// came, leaving the direction dead after one kill.
func TestSteerAfterKillAndRetryContinuesTheDirection(t *testing.T) {
	s, ex, in := newSteerServer(t)
	cur, _ := s.Store.GetIntent(in.ID)
	if _, err := s.Store.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Hold = true
		i.Aborted = true
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.CancelRunWithCause(cur.RunID, model.CancelCauseWorkerKill, "test kill"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		got, ok := s.Store.GetIntent(in.ID)
		return ok && got.Status == model.StatusFailed && got.Hold
	})

	// The operator retries the killed direction: a new run starts on a fresh
	// session, and the claim is where the abort marking must end.
	if _, err := s.RetryIntent(in.ID, "", false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool { return len(ex.requests()) == 2 })
	if got, _ := s.Store.GetIntent(in.ID); got.Aborted {
		t.Fatal("the abort marking outlived a deliberate new run")
	}

	// The retried turn left a trajectory, so the direction can now be steered.
	workDir := filepath.Join(s.CaseRoot, in.ID)
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executor.SessionPath(workDir), []byte(`{"seq":1}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Store.GetIntent(in.ID)
	runID, err := s.SteerIntent(in.ID, "换一个入口再试", "")
	if err != nil {
		t.Fatalf("steer after retry: %v", err)
	}
	if runID == before.RunID {
		t.Fatal("steer did not start a continuation run")
	}
	waitFor(t, 5*time.Second, func() bool { return len(ex.requests()) == 3 })
	if got, _ := s.Store.GetIntent(in.ID); got.Status != model.StatusRunning || got.Hold {
		t.Fatalf("steered direction = %s hold=%v, want running", got.Status, got.Hold)
	}
}
