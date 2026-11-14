package lifecycle

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// harness wires a Service to a real temp ledger. The owner callbacks are
// no-ops or observable channels so these tests assert lifecycle behaviour, not
// prompt/journal/identity code that remains in the API.
type harness struct {
	svc   *Service
	store *store.Store

	closing       atomic.Bool
	paused        atomic.Bool
	deleting      atomic.Bool
	reviewsPaused atomic.Bool

	projectLimit  int
	maxWorkers    int
	instanceLock  *sync.Mutex
	globalRunning func() int
	blocked       []string
	profileErr    error
	planErr       error
	browserActive atomic.Int64

	dispatchOverride func(ctx context.Context, in model.Intent, runID string)
	started          chan string
	stopped          chan string

	pauseDeciderReason atomic.Value
	settled            chan string
}

func newHarness(t *testing.T, configure ...func(*harness)) *harness {
	t.Helper()
	h := &harness{
		started: make(chan string, 32),
		stopped: make(chan string, 32),
		settled: make(chan string, 32),
	}
	for _, fn := range configure {
		fn(h)
	}
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	h.store = st
	options := Options{
		Store: st,
		Gates: Gates{
			Closing:       &h.closing,
			Paused:        &h.paused,
			Deleting:      &h.deleting,
			ReviewsPaused: &h.reviewsPaused,
		},
		ProjectLimit:       func() int { return h.projectLimit },
		RunningIntentCount: h.runningIntentCount,
		MaxWorkers:         func() int { return h.maxWorkers },
		GlobalRunning: func() int {
			if h.globalRunning == nil {
				return 0
			}
			return h.globalRunning()
		},
		InstanceDispatch: func() *sync.Mutex { return h.instanceLock },
		CheckProfileCapacity: func(model.Run) error {
			return h.profileErr
		},
		BlockedPremises: func(model.Intent) []string { return h.blocked },
		PlanRun: func(in model.Intent, continuedFrom, profileOverride, fallbackFrom string) (model.Run, string, error) {
			if h.planErr != nil {
				return model.Run{}, "", h.planErr
			}
			return model.Run{IntentID: in.ID, ContinuedFrom: continuedFrom, FallbackFrom: fallbackFrom}, "local", nil
		},
		Dispatch: func(ctx context.Context, in model.Intent, runID, nodeID string, resume bool, prompt string, keepPrompt bool) {
			h.started <- runID
			if h.dispatchOverride != nil {
				h.dispatchOverride(ctx, in, runID)
				h.stopped <- runID
				return
			}
			// The real dispatcher settles the run before its goroutine cleanup
			// removes the cancel registry entry, so the cause is still readable
			// here. Tests that cancel a run rely on the same ordering.
			<-ctx.Done()
			h.svc.Finish(runID, in.ID, model.RunCanceled, "已取消", executor.Result{}, 0)
			h.stopped <- runID
		},
		RevokeRun:         func(string) {},
		RemoveRunIdentity: func(string, string) {},
		Redact:            func(s string) string { return s },
		AfterSettle: func(intentID, runID, status string) {
			h.settled <- intentID
		},
		WakeDecider: func(string) {},
		Pump:        func() {},
		PauseDecider: func(reason string) {
			h.pauseDeciderReason.Store(reason)
		},
		BrowserActiveCount: func() int { return int(h.browserActive.Load()) },
	}
	h.svc = New(options)
	return h
}

func (h *harness) runningIntentCount() int {
	n := 0
	for _, r := range h.store.Runs() {
		if r.Status == model.RunRunning && r.Purpose == "" {
			n++
		}
	}
	return n
}

func waitFor(t *testing.T, description string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

func waitChan(t *testing.T, ch <-chan string, what string) string {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		return ""
	}
}

func (h *harness) createIntent(t *testing.T, title string) model.Intent {
	t.Helper()
	in, err := h.store.CreateIntent(model.Intent{Title: title})
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func TestStartAdmitsExactlyOneWinnerForTheSameDirection(t *testing.T) {
	h := newHarness(t)
	in := h.createIntent(t, "同一个方向")
	var group sync.WaitGroup
	errs := make([]error, 2)
	ids := make([]string, 2)
	group.Add(2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			defer group.Done()
			ids[i], errs[i] = h.svc.Start(in.ID, "", "", false, false, "", "", "")
		}(i)
	}
	group.Wait()
	success := 0
	for i := range errs {
		if errs[i] == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("start winners = %d (errs=%v), want 1", success, errs)
	}
	winner := ""
	for i := range errs {
		if errs[i] == nil {
			winner = ids[i]
		} else if !strings.Contains(errs[i].Error(), "无法认领") && !strings.Contains(errs[i].Error(), "已在运行") {
			t.Fatalf("loser error = %v, want a claim refusal", errs[i])
		}
	}
	if winner == "" {
		t.Fatal("no winning run id")
	}
	if !h.svc.RunActive(winner) {
		t.Fatal("the admitted run has no cancel handle")
	}
	waitChan(t, h.started, "dispatch to start")
	runs := h.store.Runs()
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want exactly 1 claim", len(runs))
	}
	h.svc.CancelAll()
	waitChan(t, h.stopped, "dispatch to stop")
}

func TestStartRefusesPausedClosingAndDeletingProjects(t *testing.T) {
	cases := []struct {
		name   string
		gate   func(*harness)
		expect string
	}{
		{"paused", func(h *harness) { h.paused.Store(true) }, "暂停"},
		{"closing", func(h *harness) { h.closing.Store(true) }, "关闭"},
		{"deleting", func(h *harness) { h.deleting.Store(true) }, "删除"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			in := h.createIntent(t, "门禁方向")
			tc.gate(h)
			if _, err := h.svc.Start(in.ID, "", "", false, false, "", "", ""); err == nil || !strings.Contains(err.Error(), tc.expect) {
				t.Fatalf("start error = %v, want %q", err, tc.expect)
			}
			if runs := h.store.Runs(); len(runs) != 0 {
				t.Fatalf("refused start created a run: %+v", runs)
			}
		})
	}
}

func TestStartRefusesProjectInstanceAndProfileCapacity(t *testing.T) {
	t.Run("project", func(t *testing.T) {
		h := newHarness(t, func(h *harness) { h.projectLimit = 1 })
		if _, err := h.store.AddRun(model.Run{Node: "local", Status: model.RunRunning}); err != nil {
			t.Fatal(err)
		}
		in := h.createIntent(t, "容量方向")
		if _, err := h.svc.Start(in.ID, "", "", false, false, "", "", ""); err == nil || !strings.Contains(err.Error(), "并发已到上限") {
			t.Fatalf("project cap error = %v", err)
		}
	})
	t.Run("instance", func(t *testing.T) {
		h := newHarness(t, func(h *harness) {
			h.maxWorkers = 2
			h.globalRunning = func() int { return 2 }
		})
		in := h.createIntent(t, "实例容量方向")
		if _, err := h.svc.Start(in.ID, "", "", false, false, "", "", ""); err == nil || !strings.Contains(err.Error(), "实例并发已到上限") {
			t.Fatalf("instance cap error = %v", err)
		}
	})
	t.Run("profile", func(t *testing.T) {
		h := newHarness(t, func(h *harness) { h.profileErr = context.DeadlineExceeded })
		in := h.createIntent(t, "模型容量方向")
		if _, err := h.svc.Start(in.ID, "", "", false, false, "", "", ""); err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
			t.Fatalf("profile cap error = %v", err)
		}
		if runs := h.store.Runs(); len(runs) != 0 {
			t.Fatalf("capacity refusal created a run: %+v", runs)
		}
	})
}

func TestStartRefusesBlockedPremisesBeforeClaiming(t *testing.T) {
	h := newHarness(t, func(h *harness) { h.blocked = []string{"f_1（不存在）"} })
	in := h.createIntent(t, "前提方向")
	if _, err := h.svc.Start(in.ID, "", "", false, false, "", "", ""); err == nil || !strings.Contains(err.Error(), "前提事实不成立") {
		t.Fatalf("blocked premise error = %v", err)
	}
	if runs := h.store.Runs(); len(runs) != 0 {
		t.Fatal("blocked premise created a run")
	}
}

func TestStaleRunCannotCancelOrSettleANewerHolder(t *testing.T) {
	h := newHarness(t)
	in := h.createIntent(t, "换手方向")
	first, err := h.svc.Start(in.ID, "", "", false, false, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	waitChan(t, h.started, "first dispatch")
	// Read the registry's cause while the run still owns it: a real dispatcher
	// settles before its goroutine cleanup, so these are the values finish sees.
	if err := h.svc.CancelRunWithCause(first, model.CancelCauseRetry, "换新轮次"); err != nil {
		t.Fatal(err)
	}
	if !h.svc.WasCanceled(first) {
		t.Fatal("registry lost the cancellation flag")
	}
	if cause := h.svc.CancelCause(first); cause != model.CancelCauseRetry {
		t.Fatalf("cause = %q", cause)
	}
	waitChan(t, h.stopped, "first dispatch to stop")

	second, err := h.svc.Start(in.ID, "", "", false, false, "", "", "")
	if err != nil {
		t.Fatalf("replacement start: %v", err)
	}
	if second == first {
		t.Fatal("replacement reused the old run id")
	}
	waitChan(t, h.started, "second dispatch")
	// The old run id is no longer in the registry: it cannot stop the holder
	// that replaced it, even though the intent's attempt counter may repeat.
	if err := h.svc.CancelRun(first); err == nil || !strings.Contains(err.Error(), "不在运行") {
		t.Fatalf("stale cancel = %v, want a registry miss", err)
	}
	if !h.svc.RunActive(second) {
		t.Fatal("a stale run cancelled the newer holder")
	}
	// A late settle from the old run is stale too: SettleIntentForRun must
	// ignore it instead of turning the new holder's direction terminal.
	h.svc.Finish(first, in.ID, model.RunCanceled, "过时的结算", executor.Result{}, 0)
	if got, _ := h.store.GetIntent(in.ID); got.Status != model.StatusRunning || got.RunID != second {
		t.Fatalf("stale finish rewrote the newer holder: %+v", got)
	}
	if err := h.svc.CancelRunWithCause(second, model.CancelCauseWatchdog, "静默"); err != nil {
		t.Fatal(err)
	}
	if cause := h.svc.CancelCause(second); cause != model.CancelCauseWatchdog {
		t.Fatalf("newer cause = %q", cause)
	}
	waitChan(t, h.stopped, "second dispatch to stop")
}

func TestExplicitlyCanceledRunSettlesFailedAndHeld(t *testing.T) {
	h := newHarness(t)
	in := h.createIntent(t, "人工取消方向")
	runID, err := h.svc.Start(in.ID, "", "", false, false, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	waitChan(t, h.started, "dispatch")
	if err := h.svc.CancelRun(runID); err != nil {
		t.Fatal(err)
	}
	waitChan(t, h.stopped, "dispatch to stop")
	// The dispatcher settled the canceled run before unregistering; the explicit
	// stop path must hold the direction rather than leave it pending.
	settled := waitChan(t, h.settled, "settlement")
	if settled != in.ID {
		t.Fatalf("settled intent = %s", settled)
	}
	got, _ := h.store.GetIntent(in.ID)
	if got.Status != model.StatusFailed || !got.Hold {
		t.Fatalf("explicit cancel settled = %+v, want failed+hold", got)
	}
	if got.Attempts != 0 {
		t.Fatalf("attempts = %d, want the cancellation uncounted", got.Attempts)
	}
	run, _ := h.store.GetRun(runID)
	if run.Status != model.RunCanceled || run.CancelCause != model.CancelCauseOperator {
		t.Fatalf("run = %+v", run)
	}
}

func TestDoneRunSettlesWithoutCancelCause(t *testing.T) {
	h := newHarness(t, func(h *harness) {
		h.dispatchOverride = func(ctx context.Context, in model.Intent, runID string) {
			h.svc.Finish(runID, in.ID, model.RunDone, "completed", executor.Result{Events: 3}, 2)
		}
	})
	in := h.createIntent(t, "完成方向")
	if _, err := h.svc.Start(in.ID, "", "", false, false, "", "", ""); err != nil {
		t.Fatal(err)
	}
	runID := waitChan(t, h.started, "dispatch")
	waitChan(t, h.stopped, "dispatch to return")
	waitFor(t, "intent done", func() bool {
		got, ok := h.store.GetIntent(in.ID)
		return ok && got.Status == model.StatusDone
	})
	run, ok := h.store.GetRun(runID)
	if !ok || run.Status != model.RunDone || run.CancelCause != "" || run.Events != 3 || run.DeclaredFacts != 2 {
		t.Fatalf("done run = %+v", run)
	}
}

// The harness may compact a session mid-turn. The run keeps both numbers: the
// peak (pressure display/observability) and the final post-compaction size,
// which is what the next continuation must budget against.
func TestFinishRecordsHarnessFinalContextAfterCompaction(t *testing.T) {
	h := newHarness(t, func(h *harness) {
		h.dispatchOverride = func(ctx context.Context, in model.Intent, runID string) {
			h.svc.Finish(runID, in.ID, model.RunDone, "completed", executor.Result{
				Events: 3, Compacted: true,
				ContextTokens: 90000, FinalContextTokens: 12000, ContextWindow: 128000,
			}, 0)
		}
	})
	in := h.createIntent(t, "压缩后收尾")
	if _, err := h.svc.Start(in.ID, "", "", false, false, "", "", ""); err != nil {
		t.Fatal(err)
	}
	runID := waitChan(t, h.started, "dispatch")
	waitChan(t, h.stopped, "dispatch to return")
	run, ok := h.store.GetRun(runID)
	if !ok {
		t.Fatal("run missing")
	}
	if !run.Compacted || run.ContextTokens != 90000 || run.FinalContextTokens != 12000 || run.ContextWindow != 128000 {
		t.Fatalf("harness context not preserved: %+v", run)
	}
}

func TestPurposeAndReviewRunsHaveDistinctCancellationScopes(t *testing.T) {
	h := newHarness(t)
	purposeCtx, cancelPurpose := context.WithCancel(context.Background())
	reviewCtx, cancelReview := context.WithCancel(context.Background())
	releasePurpose := h.svc.TrackPurposeRun("r_purpose", cancelPurpose)
	releaseReview := h.svc.TrackReviewRun("r_review", cancelReview)

	// The project pause stops report work but deliberately not reviews (D137).
	if n := h.svc.CancelProjectWorkWithCause(model.CancelCausePause, "暂停"); n != 1 {
		t.Fatalf("project work canceled = %d, want 1", n)
	}
	if purposeCtx.Err() == nil {
		t.Fatal("report run survived the project pause")
	}
	if reviewCtx.Err() != nil {
		t.Fatal("project pause stopped the verifier run")
	}
	if cause := releasePurpose(); cause != model.CancelCausePause {
		t.Fatalf("purpose cause = %q", cause)
	}

	// The review switch stops only reviews.
	if n := h.svc.CancelReviewsWithCause(model.CancelCauseReviewPause, "复核暂停"); n != 1 {
		t.Fatalf("reviews canceled = %d, want 1", n)
	}
	if reviewCtx.Err() == nil {
		t.Fatal("review run survived the review switch")
	}
	if cause := releaseReview(); cause != model.CancelCauseReviewPause {
		t.Fatalf("review cause = %q", cause)
	}
	// Unregistering twice must not duplicate or lose the cause.
	if again := releaseReview(); again != model.CancelCauseReviewPause {
		t.Fatalf("second unregister = %q", again)
	}
}

// The commander's standby round is not work the pause owns: it asked for the stop
// and must still finish its answer and delivery checkpoint. Except is the only
// difference from the operator pause, which cancels every purpose run.
func TestCancelProjectWorkExceptSparesTheAskingCommanderRun(t *testing.T) {
	h := newHarness(t)
	askCtx, cancelAsk := context.WithCancel(context.Background())
	otherCtx, cancelOther := context.WithCancel(context.Background())
	releaseAsk := h.svc.TrackPurposeRun("r_commander", cancelAsk)
	releaseOther := h.svc.TrackPurposeRun("r_report", cancelOther)
	if n := h.svc.CancelProjectWorkExcept(model.CancelCausePause, "指挥官请求停止项目", "r_commander"); n != 1 {
		t.Fatalf("project work canceled = %d, want 1", n)
	}
	if askCtx.Err() != nil {
		t.Fatal("the asking commander round was canceled")
	}
	if otherCtx.Err() == nil {
		t.Fatal("the other purpose run survived the stop")
	}
	if cause := releaseAsk(); cause != "" {
		t.Fatalf("asking round cause = %q, want none", cause)
	}
	if cause := releaseOther(); cause != model.CancelCausePause {
		t.Fatalf("other run cause = %q", cause)
	}
}

func TestCancelAllCoversIntentPurposeAndReviewRuns(t *testing.T) {
	h := newHarness(t)
	in := h.createIntent(t, "全停方向")
	runID, err := h.svc.Start(in.ID, "", "", false, false, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	waitChan(t, h.started, "dispatch")
	_, cancelPurpose := context.WithCancel(context.Background())
	_, cancelReview := context.WithCancel(context.Background())
	h.svc.TrackPurposeRun("r_purpose", cancelPurpose)
	h.svc.TrackReviewRun("r_review", cancelReview)
	if n := h.svc.CancelAllWithCause(model.CancelCauseRestart, "停止"); n != 3 {
		t.Fatalf("cancelled = %d, want 3", n)
	}
	if !h.svc.WasCanceled(runID) || h.svc.CancelCause(runID) != model.CancelCauseRestart {
		t.Fatalf("intent run was not stopped: %v %q", h.svc.WasCanceled(runID), h.svc.CancelCause(runID))
	}
	waitChan(t, h.stopped, "dispatch")
}

func TestBeginShutdownSealsAdmissionAndCancelsEverything(t *testing.T) {
	h := newHarness(t)
	in := h.createIntent(t, "关闭方向")
	if _, err := h.svc.Start(in.ID, "", "", false, false, "", "", ""); err != nil {
		t.Fatal(err)
	}
	waitChan(t, h.started, "dispatch")
	if n := h.svc.BeginShutdown(); n < 1 {
		t.Fatalf("shutdown canceled = %d", n)
	}
	if !h.svc.Closing() {
		t.Fatal("shutdown did not seal admission")
	}
	if reason, _ := h.pauseDeciderReason.Load().(string); !strings.Contains(reason, "关闭") {
		t.Fatalf("decider pause reason = %q", reason)
	}
	if _, err := h.svc.Start(in.ID, "", "", false, false, "", "", ""); err == nil || !strings.Contains(err.Error(), "关闭") {
		t.Fatalf("start after shutdown = %v", err)
	}
	waitChan(t, h.stopped, "dispatch")
}

func TestWaitIdleWaitsForPurposeRunsAndBrowserCommands(t *testing.T) {
	h := newHarness(t)
	if !h.svc.WaitIdle(10 * time.Millisecond) {
		t.Fatal("an idle project is not idle")
	}
	run, err := h.store.AddRun(model.Run{Node: "local", Purpose: "report", Status: model.RunRunning})
	if err != nil {
		t.Fatal(err)
	}
	if h.svc.WaitIdle(30 * time.Millisecond) {
		t.Fatal("a live report run must keep the project busy")
	}
	if _, err := h.store.UpdateRun(run.ID, func(r *model.Run) { r.Status = model.RunDone }); err != nil {
		t.Fatal(err)
	}
	if !h.svc.WaitIdle(500 * time.Millisecond) {
		t.Fatal("project did not become idle")
	}
	h.browserActive.Store(1)
	if h.svc.WaitIdle(30 * time.Millisecond) {
		t.Fatal("an in-flight browser command must keep the project busy")
	}
	h.browserActive.Store(0)
	if !h.svc.WaitIdle(500 * time.Millisecond) {
		t.Fatal("project did not become idle after the browser command ended")
	}
}

func TestInstanceAdmissionLockMakesTheWorkerCapHoldAcrossProjects(t *testing.T) {
	var admission sync.Mutex
	h1 := newHarness(t, func(h *harness) {
		h.instanceLock = &admission
		h.maxWorkers = 1
	})
	h2 := newHarness(t, func(h *harness) {
		h.instanceLock = &admission
		h.maxWorkers = 1
	})
	// The hub's instance-wide count sums live intent runs of every project, as
	// the per-project cap counts its own: here the two real stores are the sum.
	count := func() int { return h1.runningIntentCount() + h2.runningIntentCount() }
	h1.globalRunning, h2.globalRunning = count, count
	first := h1.createIntent(t, "项目一方向")
	second := h2.createIntent(t, "项目二方向")
	var group sync.WaitGroup
	errs := make([]error, 2)
	group.Add(2)
	go func() { defer group.Done(); _, errs[0] = h1.svc.Start(first.ID, "", "", false, false, "", "", "") }()
	go func() { defer group.Done(); _, errs[1] = h2.svc.Start(second.ID, "", "", false, false, "", "", "") }()
	group.Wait()
	// Capacity is an instance cap, not two independent project caps: the shared
	// admission lock plus the hub's running count admits exactly one.
	success := 0
	for _, err := range errs {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("instance cap admitted %d runs (errs=%v), want 1", success, errs)
	}
}

func TestRecoverSettlesOrphanRunsAndConsumesTheRestartFlagOnce(t *testing.T) {
	h := newHarness(t)
	parent := h.createIntent(t, "崩溃父方向")
	claimed, parentRun, err := h.store.StartIntentRun(parent.ID, "local", "", model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	if claimed.Attempts != 1 {
		t.Fatalf("claim attempts = %d", claimed.Attempts)
	}
	child, _, err := h.store.CreateChildIntent(parent.ID, model.Intent{Title: "崩溃子方向", IdempotencyKey: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.store.StartIntentRun(child.ID, "local", "", model.Run{}); err != nil {
		t.Fatal(err)
	}

	h.svc.Recover()

	gotParent, _ := h.store.GetIntent(parent.ID)
	if gotParent.Status != model.StatusPending || gotParent.Attempts != 0 {
		t.Fatalf("recovered parent = %+v, want pending with the attempt uncounted", gotParent)
	}
	gotChild, _ := h.store.GetIntent(child.ID)
	if gotChild.Status != model.StatusFailed {
		t.Fatalf("recovered child = %+v, want failed for its parent to decide", gotChild)
	}
	recoveredRun, _ := h.store.GetRun(parentRun.ID)
	if recoveredRun.Status != model.RunCanceled || recoveredRun.CancelCause != model.CancelCauseRestart {
		t.Fatalf("recovered run = %+v", recoveredRun)
	}
	if !h.svc.Restarted() || !h.svc.TakeRestarted() {
		t.Fatal("the crash sweep did not report a restart")
	}
	if h.svc.TakeRestarted() {
		t.Fatal("the restart flag was not consumed exactly once")
	}
	// A recovered parent is not hanging: its wait is cleared.
	if gotParent.Wait != nil {
		t.Fatalf("recovered parent kept a wait: %+v", gotParent.Wait)
	}
}

func TestReapClaimsReturnsLostClaimToPendingAndFailsLostChild(t *testing.T) {
	h := newHarness(t)
	parent := h.createIntent(t, "认领父方向")
	if _, run, err := h.store.StartIntentRun(parent.ID, "local", "", model.Run{}); err != nil {
		t.Fatal(err)
	} else if _, err := h.store.UpdateRun(run.ID, func(r *model.Run) { r.Status = model.RunDone }); err != nil {
		t.Fatal(err)
	}
	child, _, err := h.store.CreateChildIntent(parent.ID, model.Intent{Title: "认领子方向", IdempotencyKey: "k2"})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate the crash shape: claimed intents whose runs never started.
	for _, id := range []string{parent.ID, child.ID} {
		if _, err := h.store.UpdateIntent(id, func(i *model.Intent) {
			i.Status = model.StatusClaimed
			i.RunID = ""
		}); err != nil {
			t.Fatal(err)
		}
	}
	h.svc.ReapClaims()
	if got, _ := h.store.GetIntent(parent.ID); got.Status != model.StatusPending {
		t.Fatalf("reaped parent = %+v", got)
	}
	if got, _ := h.store.GetIntent(child.ID); got.Status != model.StatusFailed {
		t.Fatalf("reaped child = %+v", got)
	}
	waitChan(t, h.settled, "child settlement")
}
