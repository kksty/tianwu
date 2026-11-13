package api

import (
	"context"
	"fmt"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
)

type reliabilityLateOverflowExec struct{ calls atomic.Int32 }

func (e *reliabilityLateOverflowExec) Name() string { return "audit-late-overflow" }
func (e *reliabilityLateOverflowExec) Run(_ context.Context, _ executor.Request, emit func(executor.Event)) (executor.Result, error) {
	if e.calls.Add(1) == 1 {
		emit(executor.Event{Kind: executor.KindToolCall, Tool: "fixture_external_operation", Text: "an operation was issued"})
		emit(executor.Event{Kind: executor.KindToolResult, Tool: "fixture_external_operation", Text: "operation outcome unknown"})
		return executor.Result{ExitCode: 1, IsError: true, StopReason: "error", Output: "context_window_exhausted after tool output"}, nil
	}
	return executor.Result{StopReason: "completed"}, nil
}
func TestReliabilityLateContextFailureMustNotReplayWholeIntent(t *testing.T) {
	ex := &reliabilityLateOverflowExec{}
	s, st, _ := newContextLimitServer(t, ex)
	in, err := st.CreateIntent(model.Intent{Title: "fixture", Description: "an operation may already have happened"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.StartRun(in.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		f, _ := st.GetIntent(in.ID)
		return f.Status == model.StatusDone || f.Status == model.StatusFailed
	})
	time.Sleep(250 * time.Millisecond)
	if ex.calls.Load() > 1 {
		t.Fatalf("automatically replayed whole intent %d times after a tool operation with unknown effects", ex.calls.Load())
	}
}

type reliabilityFinishOnlyExec struct{}

func (reliabilityFinishOnlyExec) Name() string { return "audit-finish" }
func (reliabilityFinishOnlyExec) Run(context.Context, executor.Request, func(executor.Event)) (executor.Result, error) {
	return executor.Result{StopReason: "completed"}, nil
}
func TestReliabilityOneFinishedIntentMustDrainItsReviewBacklog(t *testing.T) {
	ex := &reviewProbeExec{}
	s, st := newAutoVerifyServer(t, ex)
	s.Exec = reliabilityFinishOnlyExec{}
	s.AutoVerify = true
	in, err := st.CreateIntent(model.Intent{Title: "single finite producer"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < autoVerifyBatch+1; i++ {
		addReviewableFact(t, st, in.ID, "vuln", fmt.Sprintf("f-%d", i))
	}
	if _, err = s.StartRun(in.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool { return verifiedFindings(t, st, in.ID) == autoVerifyBatch+1 })
	time.Sleep(150 * time.Millisecond)
	if n := verifiedFindings(t, st, in.ID); n != autoVerifyBatch+1 {
		t.Fatalf("finite producer finished; only %d/%d reviewed and no continuation is scheduled", n, autoVerifyBatch+1)
	}
}
func TestReliabilityChildWaitMustNotBeLabeledHumanWait(t *testing.T) {
	s, st, _ := newContextLimitServer(t, reliabilityFinishOnlyExec{})
	in, _ := st.CreateIntent(model.Intent{Title: "parent"})
	_, err := st.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Status = model.StatusWaiting
		i.Wait = &model.Wait{Children: []string{"child"}}
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.projectState(); got.Kind == pauseKindWaitingHuman {
		t.Fatalf("child-only wait incorrectly asks for a human: %+v", got)
	}
}

func TestReviewPumpResumesAndWakesAfterCooldownWithoutAnotherProducer(t *testing.T) {
	ex := &reviewProbeExec{}
	s, st := newAutoVerifyServer(t, ex)
	in, err := st.CreateIntent(model.Intent{Title: "finite paused producer"})
	if err != nil {
		t.Fatal(err)
	}
	f := addReviewableFact(t, st, in.ID, "vuln", "cooling candidate")
	s.SetReviewsPaused(true)
	s.noteReviewFailure(f, time.Now().Add(-reviewRetryCooldown+100*time.Millisecond))
	s.scheduleAutoVerify(in.ID)
	if ex.runs() != 0 {
		t.Fatal("review started while paused")
	}
	rr := httptest.NewRecorder()
	s.verifierResume(rr, httptest.NewRequest("POST", "/api/verifier/resume", nil))
	if rr.Code != 200 {
		t.Fatalf("resume: %d %s", rr.Code, rr.Body.String())
	}
	waitFor(t, 5*time.Second, func() bool { return verifiedFindings(t, st, in.ID) == 1 })
	if ex.runs() != 1 {
		t.Fatalf("cooldown wake issued %d reviews", ex.runs())
	}
}
