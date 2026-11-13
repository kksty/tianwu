package api

import (
	"context"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
)

type failedCommanderInputExec struct{}

func (failedCommanderInputExec) Name() string { return "failed-commander-input" }
func (failedCommanderInputExec) Run(context.Context, executor.Request, func(executor.Event)) (executor.Result, error) {
	return executor.Result{ExitCode: 1, IsError: true, StopReason: "error", Output: "connection unavailable"}, nil
}

func TestFailedCommanderTransportRequeuesItsPromptInput(t *testing.T) {
	s, _, st := newCommanderServer(t)
	s.Exec = failedCommanderInputExec{}
	m, _, err := st.AddMessage(model.Message{FromParticipantID: collab.OperatorID, ToParticipantID: collab.CommanderID, Text: "explicit global request", Durable: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.StartCommander(context.Background(), "round", false); err == nil {
		t.Fatal("fixture did not fail")
	}
	got, _ := st.MessageByID(m.ID)
	if got.Status != model.MessageQueued || got.Redeliveries != 1 || got.EventID != m.EventID {
		t.Fatalf("failed API lost or duplicated request identity: %+v", got)
	}
}

func TestQueuedIndependentHelperStartsLocallyAfterPause(t *testing.T) {
	s, ex := admissionServer(t)
	p, _ := s.Store.CreateIntent(model.Intent{Title: "creator"})
	_, _ = s.Store.UpdateIntent(p.ID, func(i *model.Intent) { i.Status = model.StatusRunning })
	s.SetPaused(true)
	c, _, err := s.Store.CreateSpawnIntent(model.Intent{Title: "helper", CreatedBy: collab.ParticipantID(p.ID), IdempotencyKey: "helper"})
	if err != nil {
		t.Fatal(err)
	}
	s.pump()
	if len(ex.requests()) != 0 {
		t.Fatal("pause dispatched helper")
	}
	s.SetPaused(false)
	s.pump()
	waitFor(t, 5*time.Second, func() bool { return len(ex.requests()) == 1 })
	got, _ := s.Store.GetIntent(c.ID)
	if got.Status != model.StatusRunning {
		t.Fatal("independent helper stayed stranded")
	}
	t.Cleanup(func() { s.CancelAll(); s.WaitIdle(5 * time.Second) })
}
