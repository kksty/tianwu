package lifecycle

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
)

func TestChildWaitYieldDoesNotAskForGlobalDecision(t *testing.T) {
	h := newHarness(t)
	var wakes atomic.Int32
	h.svc.o.WakeDecider = func(string) { wakes.Add(1) }
	p := h.createIntent(t, "parent")
	_, run, err := h.store.StartIntentRun(p.ID, "local", "", model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := h.store.CreateChildIntent(p.ID, model.Intent{Title: "child"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.store.RegisterWait(p.ID, run.ID, model.Wait{Children: []string{c.ID}}); err != nil {
		t.Fatal(err)
	}
	h.svc.SetCancelCause(run.ID, model.CancelCauseChildWait)
	h.svc.Finish(run.ID, p.ID, model.RunCanceled, "yield", executor.Result{}, 0)
	if wakes.Load() != 0 {
		t.Fatal("local yield opened a global opportunity")
	}
}

func TestEveryRecoveryPathReturnsIndependentHelperToItsCreator(t *testing.T) {
	for _, path := range []string{"restart", "orphan", "claim", "dangling"} {
		t.Run(path, func(t *testing.T) {
			h := newHarness(t)
			h.paused.Store(true)
			parent := h.createIntent(t, "creator")
			helper, err := h.store.CreateIntent(model.Intent{Title: "independent helper", CreatedBy: collab.ParticipantID(parent.ID)})
			if err != nil {
				t.Fatal(err)
			}
			_, run, err := h.store.StartIntentRun(helper.ID, "local", "", model.Run{})
			if err != nil {
				t.Fatal(err)
			}
			_, err = h.store.UpdateRun(run.ID, func(r *model.Run) {
				r.StartedAt = time.Now().Add(-3 * time.Minute)
				if path == "dangling" || path == "claim" {
					r.Status = model.RunCanceled
					ended := time.Now().Add(-2 * time.Minute)
					r.EndedAt = &ended
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			if path == "claim" {
				_, err = h.store.UpdateIntent(helper.ID, func(i *model.Intent) { i.Status = model.StatusClaimed })
				if err != nil {
					t.Fatal(err)
				}
			}
			switch path {
			case "restart":
				h.svc.Recover()
			case "orphan":
				h.svc.ReapOrphans()
			case "claim":
				h.svc.ReapClaims()
			case "dangling":
				h.svc.ReapDanglingIntents()
			}
			got, _ := h.store.GetIntent(helper.ID)
			if got.Status != model.StatusFailed {
				t.Fatalf("helper would be silently re-dispatched: %+v", got)
			}
		})
	}
}

func TestWatchdogPreservesRegisteredWaitAndIgnoresReplacedRun(t *testing.T) {
	h := newHarness(t)
	h.paused.Store(true)
	p := h.createIntent(t, "parent")
	_, old, err := h.store.StartIntentRun(p.ID, "local", "", model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := h.store.CreateChildIntent(p.ID, model.Intent{Title: "child"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.store.RegisterWait(p.ID, old.ID, model.Wait{Children: []string{c.ID}}); err != nil {
		t.Fatal(err)
	}
	_, _ = h.store.UpdateRun(old.ID, func(r *model.Run) { r.StartedAt = time.Now().Add(-3 * time.Minute) })
	h.svc.ReapOrphans()
	got, _ := h.store.GetIntent(p.ID)
	if got.Status != model.StatusWaiting || got.Wait == nil || len(got.Wait.Children) != 1 {
		t.Fatal("watchdog lost explicit wait")
	}
	// Another stale run row must not roll back the current holder.
	_, _ = h.store.UpdateRun(old.ID, func(r *model.Run) { r.Status = model.RunRunning })
	_, _ = h.store.UpdateIntent(p.ID, func(i *model.Intent) { i.Status = model.StatusRunning; i.RunID = "replacement" })
	h.svc.ReapOrphans()
	got, _ = h.store.GetIntent(p.ID)
	if got.Status != model.StatusRunning || got.RunID != "replacement" {
		t.Fatal("stale orphan mutated replacement")
	}
}

func TestCrashBetweenWaitRegistrationAndYieldPreservesWait(t *testing.T) {
	h := newHarness(t)
	h.paused.Store(true)
	p := h.createIntent(t, "parent")
	_, run, err := h.store.StartIntentRun(p.ID, "local", "", model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := h.store.CreateChildIntent(p.ID, model.Intent{Title: "child"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.store.RegisterWait(p.ID, run.ID, model.Wait{Children: []string{c.ID}}); err != nil {
		t.Fatal(err)
	}
	h.svc.Recover()
	got, _ := h.store.GetIntent(p.ID)
	if got.Status != model.StatusWaiting || got.Wait == nil || len(got.Wait.Children) != 1 || got.Wait.Children[0] != c.ID {
		t.Fatalf("recovery lost persisted wait: %+v", got)
	}
	if len(h.store.Runs()) != 1 {
		t.Fatal("recovery replayed work while paused")
	}
}

func TestCrashOfIndependentlySpawnedHelperReturnsFailureToCreator(t *testing.T) {
	h := newHarness(t)
	p := h.createIntent(t, "creator")
	c, _, err := h.store.CreateSpawnIntent(model.Intent{Title: "independent helper", CreatedBy: collab.ParticipantID(p.ID), IdempotencyKey: "helper"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = h.store.StartIntentRun(c.ID, "local", "", model.Run{}); err != nil {
		t.Fatal(err)
	}
	h.svc.Recover()
	got, _ := h.store.GetIntent(c.ID)
	if got.Status != model.StatusFailed {
		t.Fatal("crashed helper was silently queued for replay")
	}
	if err = h.store.RouteTaskResults(); err != nil {
		t.Fatal(err)
	}
	msgs := h.store.PendingCoordinationNotices()
	if len(msgs) != 1 || msgs[0].ToParticipantID != collab.ParticipantID(p.ID) {
		t.Fatal("creator lost interrupted helper result")
	}
}

func TestCrashRequeuesUnconfirmedCommanderInputButNotUnknownOrAcknowledged(t *testing.T) {
	h := newHarness(t)
	run, _ := h.store.AddRun(model.Run{Purpose: "commander"})
	now := time.Now()
	a, _, _ := h.store.AddMessage(model.Message{FromParticipantID: collab.OperatorID, ToParticipantID: collab.CommanderID, Text: "request", Status: model.MessageAccepted, DeliveredRunID: run.ID})
	u, _, _ := h.store.AddMessage(model.Message{FromParticipantID: collab.OperatorID, ToParticipantID: collab.CommanderID, Text: "uncertain", Status: model.MessageUnknown, DeliveredRunID: run.ID})
	c, _, _ := h.store.AddMessage(model.Message{FromParticipantID: collab.OperatorID, ToParticipantID: collab.CommanderID, Text: "acked", Status: model.MessageConfirmed, AckedAt: &now, DeliveredRunID: run.ID})
	h.svc.Recover()
	got, _ := h.store.MessageByID(a.ID)
	unknown, _ := h.store.MessageByID(u.ID)
	confirmed, _ := h.store.MessageByID(c.ID)
	if got.Status != model.MessageQueued || got.EventID != a.EventID || unknown.Status != model.MessageUnknown || confirmed.Status != model.MessageConfirmed {
		t.Fatal("restart lost input identity or replayed uncertain work")
	}
}
