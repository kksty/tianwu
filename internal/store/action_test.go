package store

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/model"
)

func actionFixture(t *testing.T) (*Store, model.Intent, model.Run) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	in, err := s.CreateIntent(model.Intent{Title: "action owner"})
	if err != nil {
		t.Fatal(err)
	}
	in, run, err := s.StartIntentRun(in.ID, "local", "", model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	return s, in, run
}

func TestActionWaitModesDeadlineAndRestart(t *testing.T) {
	for _, mode := range []string{model.WaitAll, model.WaitAny} {
		t.Run(mode, func(t *testing.T) {
			s, in, run := actionFixture(t)
			a, _, _ := s.BeginAction(in.ID, run.ID, "one", "operation one", "")
			b, _, _ := s.BeginAction(in.ID, run.ID, "two", "operation two", "")
			if _, err := s.RegisterWait(in.ID, run.ID, model.Wait{Actions: []string{a.ID, b.ID}, Mode: mode}); err != nil {
				t.Fatal(err)
			}
			_, _ = s.UpdateRun(run.ID, func(r *model.Run) { r.Status = model.RunCanceled; r.CancelCause = model.CancelCauseActionWait })
			if _, err := s.SettleIntentForRun(in.ID, run.ID, model.RunCanceled, model.CancelCauseActionWait); err != nil {
				t.Fatal(err)
			}
			var err error
			s, err = Open(s.Path())
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.ResolveAction(a.ID, 1, model.ActionUnknown, "needs reconciliation", "", ""); err != nil {
				t.Fatal(err)
			}
			got, _ := s.GetIntent(in.ID)
			if got.Wait.WakePending != (mode == model.WaitAny) {
				t.Fatalf("wrong mode readiness: %+v", got.Wait)
			}
			if _, err = s.ResolveAction(b.ID, 1, model.ActionCompleted, "confirmed failure result", "", ""); err != nil {
				t.Fatal(err)
			}
			got, _ = s.GetIntent(in.ID)
			if !got.Wait.WakePending {
				t.Fatal("wait did not survive restart")
			}
		})
	}
	s, in, run := actionFixture(t)
	a, _, _ := s.BeginAction(in.ID, run.ID, "timeout", "operation", "")
	deadline := time.Now().Add(-time.Second)
	state, err := s.RegisterWait(in.ID, run.ID, model.Wait{Actions: []string{a.ID}, Deadline: &deadline})
	if err != nil || !state.Ready || state.Reason != model.WaitReasonTimeout {
		t.Fatalf("deadline=%+v %v", state, err)
	}
	if _, err = s.RegisterWait(in.ID, run.ID, model.Wait{Actions: []string{a.ID}, Children: []string{in.ID}}); err == nil {
		t.Fatal("mixed waits accepted")
	}
	if _, err = s.ResolveAction(a.ID, 1, model.ActionCompleted, "late", "", "old-run"); !errors.Is(err, ErrActionConflict) {
		t.Fatalf("stale owner=%v", err)
	}
}

func TestActionIdempotencyAndCompletionCAS(t *testing.T) {
	s, in, run := actionFixture(t)
	var wg sync.WaitGroup
	ids := make(chan string, 16)
	created := make(chan bool, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, fresh, err := s.BeginAction(in.ID, run.ID, "same", "external job", "ref")
			if err != nil {
				t.Error(err)
				return
			}
			ids <- a.ID
			created <- fresh
		}()
	}
	wg.Wait()
	close(ids)
	close(created)
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatal("duplicate action")
		}
	}
	n := 0
	for fresh := range created {
		if fresh {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("created=%d", n)
	}
	if _, _, err := s.BeginAction(in.ID, run.ID, "same", "different", "ref"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflict=%v", err)
	}
	a, err := s.ResolveAction(first, 1, model.ActionUnknown, "timeout", "", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ResolveAction(first, 1, model.ActionCompleted, "late old result", "", run.ID); !errors.Is(err, ErrActionConflict) {
		t.Fatalf("stale receipt=%v", err)
	}
	a, err = s.ResolveAction(first, a.Version, model.ActionCompleted, "result confirmed", "artifact:fixture", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ResolveAction(first, a.Version, model.ActionUnknown, "reset", "", run.ID); !errors.Is(err, ErrActionConflict) {
		t.Fatalf("completed reset=%v", err)
	}
	reopened, err := Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := reopened.GetAction(first); got.State != model.ActionCompleted || got.ResultRef != "artifact:fixture" {
		t.Fatalf("reopened=%+v", got)
	}
	if len(s.ListFacts("", 0)) != 0 {
		t.Fatal("execution receipt became a fact")
	}
}

func TestActionWaitTransfersToReplacementRun(t *testing.T) {
	s, in, run := actionFixture(t)
	a, _, err := s.BeginAction(in.ID, run.ID, "job", "external operation", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RegisterWait(in.ID, run.ID, model.Wait{Actions: []string{a.ID}}); err != nil {
		t.Fatal(err)
	}
	_, _ = s.UpdateRun(run.ID, func(r *model.Run) { r.Status = model.RunCanceled; r.CancelCause = model.CancelCauseActionWait })
	if _, err = s.SettleIntentForRun(in.ID, run.ID, model.RunCanceled, model.CancelCauseActionWait); err != nil {
		t.Fatal(err)
	}
	held, err := s.BeginHandoff(in.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := s.SaveHandoff(in.ID, 0, model.Handoff{ToProfile: "replacement"}, held.HandoffOperation)
	if err != nil || saved.Handoff.Wait == nil || len(saved.Handoff.Wait.Actions) != 1 {
		t.Fatalf("snapshot=%+v err=%v", saved, err)
	}
	_, replacement, err := s.StartIntentRun(in.ID, "local", held.HandoffOperation, model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ResolveAction(a.ID, 1, model.ActionCompleted, "late old-run receipt", "", run.ID); !errors.Is(err, ErrActionConflict) {
		t.Fatalf("old owner accepted: %v", err)
	}
	if _, err = s.ResolveAction(a.ID, 1, model.ActionCompleted, "verified result", "", replacement.ID); err != nil {
		t.Fatal(err)
	}
	cur, _ := s.GetIntent(in.ID)
	if cur.Wait == nil || cur.Wait.RegisteredByRun != replacement.ID || !cur.Wait.WakePending {
		t.Fatalf("wait not transferred: %+v", cur.Wait)
	}
	replayed, created, err := s.BeginAction(in.ID, replacement.ID, "job", "external operation", "")
	if err != nil || created || replayed.ID != a.ID || replayed.State != model.ActionCompleted {
		t.Fatalf("cross-generation duplicate=%+v new=%v err=%v", replayed, created, err)
	}
}

func TestActionWaitDoesNotLoseEarlyReceiptOrWakeUnrelatedWait(t *testing.T) {
	s, in, run := actionFixture(t)
	a, _, _ := s.BeginAction(in.ID, run.ID, "a", "first", "")
	b, _, _ := s.BeginAction(in.ID, run.ID, "b", "second", "")
	if _, err := s.ResolveAction(a.ID, a.Version, model.ActionCompleted, "ready", "", run.ID); err != nil {
		t.Fatal(err)
	}
	state, err := s.RegisterWait(in.ID, run.ID, model.Wait{Actions: []string{a.ID}})
	if err != nil || !state.Ready || state.Results[0].ActionID != a.ID {
		t.Fatalf("early receipt=%+v %v", state, err)
	}
	if _, err = s.RegisterWait(in.ID, run.ID, model.Wait{Actions: []string{b.ID}}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ResolveAction(a.ID, 1, model.ActionCompleted, "duplicate", "", run.ID); err == nil {
		t.Fatal("duplicate accepted")
	}
	if got, _ := s.GetIntent(in.ID); got.Wait.WakePending {
		t.Fatal("unrelated action released wait")
	}
	if _, err = s.ResolveAction(b.ID, 1, model.ActionUnknown, "need reconcile", "", run.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetIntent(in.ID); !got.Wait.WakePending || got.Wait.Results[0].Status != model.ActionUnknown {
		t.Fatalf("unknown was not delivered: %+v", got.Wait)
	}
}

func TestActionWaitYieldRetainsStartedButInterruptedRunBecomesUnknown(t *testing.T) {
	for _, cause := range []string{model.CancelCauseActionWait, model.CancelCausePause} {
		t.Run(cause, func(t *testing.T) {
			s, in, run := actionFixture(t)
			a, _, err := s.BeginAction(in.ID, run.ID, "job", "external job", "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.RegisterWait(in.ID, run.ID, model.Wait{Actions: []string{a.ID}}); err != nil {
				t.Fatal(err)
			}
			if _, err = s.UpdateRun(run.ID, func(r *model.Run) { r.Status = model.RunCanceled; r.CancelCause = cause }); err != nil {
				t.Fatal(err)
			}
			out, err := s.SettleIntentForRun(in.ID, run.ID, model.RunCanceled, cause)
			if err != nil {
				t.Fatal(err)
			}
			got, _ := s.GetAction(a.ID)
			if cause == model.CancelCauseActionWait {
				if got.State != model.ActionStarted || out.Status != model.StatusWaiting {
					t.Fatalf("yield=%+v action=%+v", out, got)
				}
			} else if got.State != model.ActionUnknown {
				t.Fatalf("interrupted=%+v", got)
			}
		})
	}
}
