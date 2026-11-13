package lifecycle

import (
	"database/sql"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
)

func TestUnknownRunCannotSettleIntent(t *testing.T) {
	h := newHarness(t)
	in := h.createIntent(t, "unknown completion")
	h.svc.Finish("missing", in.ID, model.RunDone, "unknown", executor.Result{}, 0)
	got, _ := h.store.GetIntent(in.ID)
	if got.Status != in.Status {
		t.Fatalf("unknown run settled intent: %s", got.Status)
	}
}

func TestSettlementFailureDoesNotPublishCompletion(t *testing.T) {
	for _, phase := range []string{"run", "intent"} {
		t.Run(phase, func(t *testing.T) {
			h := newHarness(t)
			in := h.createIntent(t, "failed settlement")
			_, run, err := h.store.StartIntentRun(in.ID, "local", "", model.Run{})
			if err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", h.store.Path())
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec("CREATE TRIGGER fail_settle BEFORE UPDATE ON objects WHEN NEW.kind='" + phase + "' BEGIN SELECT RAISE(ABORT,'fixture settlement failure'); END"); err != nil {
				t.Fatal(err)
			}
			var wakes atomic.Int32
			callbacks := make(chan string, 3)
			h.svc.o.WakeDecider = func(string) { wakes.Add(1) }
			h.svc.o.AfterSettle = func(string, string, string) { callbacks <- "settled" }
			h.svc.o.AutoVerify = func(string) { callbacks <- "verify" }
			h.svc.o.AfterProviderFailure = func(string, string, string, executor.Result) { callbacks <- "provider" }
			h.svc.Finish(run.ID, in.ID, model.RunDone, "completed", executor.Result{}, 0)
			got, _ := h.store.GetIntent(in.ID)
			if got.Status != model.StatusRunning {
				t.Fatalf("failed transaction leaked intent status: %s", got.Status)
			}
			if wakes.Load() != 0 {
				t.Fatal("failed settlement published completion")
			}
			select {
			case callback := <-callbacks:
				t.Fatalf("failed settlement triggered %s", callback)
			case <-time.After(30 * time.Millisecond):
			}
			if _, err := db.Exec("DROP TRIGGER fail_settle"); err != nil {
				t.Fatal(err)
			}
			h.svc.Finish(run.ID, in.ID, model.RunDone, "retry completion", executor.Result{}, 0)
			got, _ = h.store.GetIntent(in.ID)
			if got.Status != model.StatusDone || wakes.Load() != 1 {
				t.Fatalf("settlement could not recover: status=%s wakes=%d", got.Status, wakes.Load())
			}
			seen := map[string]bool{}
			for range 3 {
				callback := waitChan(t, callbacks, "successful settlement callback")
				if seen[callback] {
					t.Fatalf("completion callback %s duplicated", callback)
				}
				seen[callback] = true
			}
		})
	}
}

func TestRunCannotSettleAnotherIntent(t *testing.T) {
	h := newHarness(t)
	owner := h.createIntent(t, "run owner")
	other := h.createIntent(t, "other intent")
	_, run, err := h.store.StartIntentRun(owner.ID, "local", "", model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	h.svc.Finish(run.ID, other.ID, model.RunDone, "wrong owner", executor.Result{}, 0)
	got, _ := h.store.GetIntent(other.ID)
	current, _ := h.store.GetRun(run.ID)
	if got.Status != other.Status || current.Status != model.RunRunning {
		t.Fatal("mismatched completion changed a run or intent")
	}
}

func TestStaleFinishDoesNotRunProviderRecovery(t *testing.T) {
	h := newHarness(t)
	in := h.createIntent(t, "stale provider failure")
	_, old, err := h.store.StartIntentRun(in.ID, "local", "", model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.UpdateIntent(in.ID, func(i *model.Intent) { i.RunID = "replacement" }); err != nil {
		t.Fatal(err)
	}
	provider := make(chan struct{}, 1)
	h.svc.o.AfterProviderFailure = func(string, string, string, executor.Result) { provider <- struct{}{} }
	h.svc.Finish(old.ID, in.ID, model.RunFailed, "old auth error", executor.Result{}, 0)
	select {
	case <-provider:
		t.Fatal("old completion started provider recovery for a replacement")
	case <-time.After(30 * time.Millisecond):
	}
}

func TestDanglingExplicitCancellationRemainsHeldAfterRecovery(t *testing.T) {
	for _, cause := range []string{model.CancelCauseOperator, model.CancelCauseStopLoss, model.CancelCauseWorkerKill, model.CancelCauseModelStall} {
		t.Run(cause, func(t *testing.T) {
			h := newHarness(t)
			in := h.createIntent(t, "interrupted stop")
			_, run, err := h.store.StartIntentRun(in.ID, "local", "", model.Run{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := h.store.RegisterOperatorWait(in.ID, run.ID, model.OperatorWait{Request: "obsolete wait"}); err != nil {
				t.Fatal(err)
			}
			if _, err := h.store.UpdateRun(run.ID, func(r *model.Run) {
				r.Status, r.CancelCause = model.RunCanceled, cause
				ended := time.Now().Add(-2 * time.Minute)
				r.EndedAt = &ended
			}); err != nil {
				t.Fatal(err)
			}
			// The process died after persisting the canceled run, before intent
			// settlement. Recovery must preserve the explicit stop's semantics.
			h.svc.ReapDanglingIntents()
			got, _ := h.store.GetIntent(in.ID)
			if got.Status != model.StatusFailed || !got.Hold || got.Wait != nil || got.Attempts != 0 {
				t.Fatalf("recovery lost explicit stop: status=%s hold=%v wait=%v attempts=%d", got.Status, got.Hold, got.Wait != nil, got.Attempts)
			}
		})
	}
}
