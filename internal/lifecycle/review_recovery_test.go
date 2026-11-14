package lifecycle

import (
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
)

// A dangling intent whose run already reached RunDone is Finish's crash-recovery
// half: it must hand the direction to the fixed review, never publish done by
// itself, or a crash would bypass the settlement gate.
func TestDanglingDoneRunSettlesThroughFixedReview(t *testing.T) {
	h := newHarness(t)
	reviewed := make(chan [2]string, 1)
	h.svc.o.ReviewAfterRun = func(intentID, runID string) bool {
		reviewed <- [2]string{intentID, runID}
		return true
	}
	var wakes int
	h.svc.o.WakeDecider = func(string) { wakes++ }
	in := h.createIntent(t, "崩溃后对账")
	_, run, err := h.store.StartIntentRun(in.ID, "local", "", model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	ended := time.Now().Add(-2 * time.Minute)
	if _, err := h.store.UpdateRun(run.ID, func(r *model.Run) {
		r.Status, r.EndedAt = model.RunDone, &ended
	}); err != nil {
		t.Fatal(err)
	}

	h.svc.ReapDanglingIntents()

	select {
	case got := <-reviewed:
		if got[0] != in.ID || got[1] != run.ID {
			t.Fatalf("review claimed (%s,%s), want (%s,%s)", got[0], got[1], in.ID, run.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a reconciled RunDone bypassed the fixed review")
	}
	if wakes != 0 {
		t.Fatalf("review-owned settlement also woke the commander %d times", wakes)
	}
	// With no subscriber to the review, the store still shows the reaper's done:
	// the point asserted above is that the settlement was handed to the phase.
	if cur, _ := h.store.GetIntent(in.ID); cur.Status != model.StatusDone {
		t.Fatalf("reaper left %s, want the adopted done state", cur.Status)
	}
}

// A dead review phase is recovered as review_failed, and the conclusions it owed
// stay in ReviewFailedFacts so a bare retry cannot flip it to done unreviewed.
func TestReviewingRecoveryKeepsUnreviewedConclusions(t *testing.T) {
	h := newHarness(t)
	old := reviewPhaseGrace
	reviewPhaseGrace = 0 // exercise the expiry instead of sleeping two minutes
	t.Cleanup(func() { reviewPhaseGrace = old })

	in := h.createIntent(t, "复核中断")
	if _, _, err := h.store.StartIntentRun(in.ID, "local", "", model.Run{}); err != nil {
		t.Fatal(err)
	}
	fact, err := h.store.AddFact(model.Fact{IntentID: in.ID, Kind: "vuln", Title: "未核验结论", Origin: model.OriginDeclared})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Status = model.StatusReviewing
	}); err != nil {
		t.Fatal(err)
	}

	h.svc.ReapDanglingIntents()

	cur, _ := h.store.GetIntent(in.ID)
	if cur.Status != model.StatusReviewFailed {
		t.Fatalf("interrupted review recovered as %s, want review_failed", cur.Status)
	}
	if !slices.Contains(cur.ReviewFailedFacts, fact.ID) {
		t.Fatalf("recovery lost the unreviewed conclusion: %+v", cur.ReviewFailedFacts)
	}

	// The retry run declares nothing new; Finish must still hand the direction to
	// the fixed review instead of publishing done by itself.
	reviewed := make(chan string, 1)
	h.svc.o.ReviewAfterRun = func(intentID, runID string) bool {
		_, _ = h.store.UpdateIntent(intentID, func(i *model.Intent) { i.Status = model.StatusReviewing })
		reviewed <- intentID
		return true
	}
	_, retry, err := h.store.StartIntentRun(in.ID, "local", "", model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	h.svc.Finish(retry.ID, in.ID, model.RunDone, "没有新结论", executor.Result{}, 0)
	select {
	case <-reviewed:
	case <-time.After(5 * time.Second):
		t.Fatal("a bare retry settled done without the fixed review")
	}
	retried, _ := h.store.GetIntent(in.ID)
	if retried.Status != model.StatusReviewing {
		t.Fatalf("bare retry published %s, want reviewing only", retried.Status)
	}
	if !slices.Contains(retried.ReviewFailedFacts, fact.ID) {
		t.Fatalf("the unresolved set was dropped before the retry review: %+v", retried.ReviewFailedFacts)
	}
}

// Provider auth/quota bookkeeping is independent of who owns the settlement
// wake: a successful run on a profile must still end that profile's credential
// failure episode when the fixed review takes over the direction.
func TestReviewOwnedSettlementStillRunsProviderBookkeeping(t *testing.T) {
	h := newHarness(t)
	var calls atomic.Int32
	provider := make(chan string, 4)
	h.svc.o.ReviewAfterRun = func(string, string) bool { return true }
	h.svc.o.AfterProviderFailure = func(runID, intentID, status string, _ executor.Result) {
		calls.Add(1)
		provider <- status
	}
	in := h.createIntent(t, "复核接管结算")
	_, run, err := h.store.StartIntentRun(in.ID, "local", "", model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	h.svc.Finish(run.ID, in.ID, model.RunDone, "completed", executor.Result{}, 0)
	select {
	case status := <-provider:
		if status != model.RunDone {
			t.Fatalf("provider callback status = %q, want done", status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the review-owned settlement skipped provider bookkeeping")
	}
	time.Sleep(50 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("provider callback ran %d times, want exactly 1", got)
	}
}
