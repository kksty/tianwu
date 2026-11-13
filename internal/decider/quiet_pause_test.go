package decider

import (
	"context"
	"errors"
	"github.com/kksty/tianwu/internal/model"
	"sync/atomic"
	"testing"
	"time"
)

func TestCompletedWorkNeverStartsAutomaticCommanderRounds(t *testing.T) {
	st := newStore(t)
	in, err := st.CreateIntent(model.Intent{Title: "finished"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.UpdateIntent(in.ID, func(i *model.Intent) { i.Status = model.StatusDone }); err != nil {
		t.Fatal(err)
	}
	g, err := st.AddGoal(model.Goal{Statement: "finished"})
	if err != nil {
		t.Fatal(err)
	}
	e, err := st.AddEvidence(model.Evidence{URI: "fixture.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.PinGoal(g.ID, e.ID, "fixture complete", nil); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(st, nil, "")
	r.ModelName = "fixture"
	calls := 0
	r.Commander = func(context.Context, string, bool) (RoundResult, error) { calls++; return RoundResult{}, nil }
	for n := 0; n < 100; n++ {
		for _, wake := range []string{"heartbeat", "intent_done", "review", "watchdog", "message", "settings", "host_ready"} {
			if d := r.Run(context.Background(), wake, false); d != nil {
				t.Fatalf("completed work ran for %s: %+v", wake, d)
			}
		}
	}
	if calls != 0 || r.Status().Rounds != 0 {
		t.Fatalf("completed work spent %d calls", calls)
	}
	if d := r.Run(context.Background(), "operator", true); d == nil || calls != 1 {
		t.Fatal("operator must still be able to request one round")
	}
	if _, err = st.CreateIntent(model.Intent{Title: "new work"}); err != nil {
		t.Fatal(err)
	}
	if d := r.Run(context.Background(), "intent_pending", false); d == nil || calls != 2 {
		t.Fatal("new pending work did not wake commander")
	}
}

// Only finished work hands the project to the control plane for auto-pause: no
// goal, an unmet goal, or a direction still wanting attention must not signal.
func TestSettledSignalOnlyForFinishedWork(t *testing.T) {
	st := newStore(t)
	r := NewRunner(st, nil, "")
	fired := 0
	r.Settled = func() { fired++ }
	r.settleIfDone()
	if fired != 0 {
		t.Fatalf("a project without goals signalled done: %d", fired)
	}
	g, err := st.AddGoal(model.Goal{Statement: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	r.settleIfDone()
	if fired != 0 {
		t.Fatalf("an unmet goal signalled done: %d", fired)
	}
	in, err := st.CreateIntent(model.Intent{Title: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	e, err := st.AddEvidence(model.Evidence{URI: "fixture.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PinGoal(g.ID, e.ID, "fixture complete", nil); err != nil {
		t.Fatal(err)
	}
	r.settleIfDone()
	if fired != 0 {
		t.Fatalf("a pending direction signalled done: %d", fired)
	}
	if _, err := st.UpdateIntent(in.ID, func(i *model.Intent) { i.Status = model.StatusDone }); err != nil {
		t.Fatal(err)
	}
	r.settleIfDone()
	if fired != 1 {
		t.Fatalf("finished work signalled done %d times, want 1", fired)
	}
	// The control plane pauses the runner while reacting; a paused runner keeps
	// quiet instead of repeating the signal.
	r.PauseFor("done")
	r.settleIfDone()
	if fired != 1 {
		t.Fatalf("settled signal repeated after pause: %d", fired)
	}
}

// The wake loop itself must deliver the settle signal: an idle wake on finished
// work is exactly the moment the operator expects the project to pause.
func TestIdleWakeOnFinishedWorkSignalsSettled(t *testing.T) {
	st := newStore(t)
	g, err := st.AddGoal(model.Goal{Statement: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	in, err := st.CreateIntent(model.Intent{Title: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIntent(in.ID, func(i *model.Intent) { i.Status = model.StatusDone }); err != nil {
		t.Fatal(err)
	}
	e, err := st.AddEvidence(model.Evidence{URI: "fixture.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PinGoal(g.ID, e.ID, "done", nil); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(st, nil, "")
	r.Heartbeat = time.Hour
	r.Debounce = time.Millisecond
	settled := make(chan struct{}, 1)
	r.Settled = func() { settled <- struct{}{} }
	r.Commander = func(context.Context, string, bool) (RoundResult, error) {
		t.Error("finished work called the commander")
		return RoundResult{}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(ctx)
	r.Wake("intent_done")
	select {
	case <-settled:
	case <-time.After(2 * time.Second):
		t.Fatal("idle wake on finished work did not signal settled")
	}
}

func TestUnchangedHeartbeatDoesNotSpendThreeSpeculativeRounds(t *testing.T) {
	st := newStore(t)
	if _, err := st.AddGoal(model.Goal{Statement: "open goal"}); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(st, nil, "")
	r.ModelName = "fixture"
	calls := 0
	r.Commander = func(context.Context, string, bool) (RoundResult, error) {
		calls++
		_, err := st.AddRun(model.Run{Purpose: "commander"})
		if d, ok := st.PendingDelivery("commander"); ok && err == nil {
			err = st.AcknowledgeDelivery("commander", d.ID)
		}
		return RoundResult{}, err
	}
	if r.Run(context.Background(), "heartbeat", false) == nil {
		t.Fatal("initial open goal must get one round")
	}
	for n := 0; n < 100; n++ {
		if r.Run(context.Background(), "heartbeat", false) != nil {
			t.Fatal("unchanged heartbeat spent another round")
		}
	}
	if calls != 1 {
		t.Fatalf("calls=%d", calls)
	}
	if _, err := st.AddFact(model.Fact{Title: "new observation"}); err != nil {
		t.Fatal(err)
	}
	if r.Run(context.Background(), "heartbeat", false) == nil || calls != 2 {
		t.Fatal("new facts must still wake commander")
	}
}

func TestPauseCancelsActiveCommanderAndDropsQueuedWakes(t *testing.T) {
	r := NewRunner(newStore(t), nil, "")
	r.ModelName = "fixture"
	r.Heartbeat = time.Millisecond
	r.Debounce = time.Millisecond
	entered := make(chan struct{}, 1)
	stopped := make(chan error, 1)
	var calls atomic.Int32
	r.Commander = func(ctx context.Context, _ string, _ bool) (RoundResult, error) {
		calls.Add(1)
		entered <- struct{}{}
		<-ctx.Done()
		stopped <- context.Cause(ctx)
		return RoundResult{}, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(ctx)
	r.Wake("operator")
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("commander did not start")
	}
	r.Wake("operator")
	r.Pause()
	select {
	case cause := <-stopped:
		if !errors.Is(cause, ErrPaused) {
			t.Fatalf("cause=%v", cause)
		}
	case <-time.After(time.Second):
		t.Fatal("pause did not cancel commander")
	}
	for n := 0; n < 100; n++ {
		r.Wake("operator")
		r.Wake("message")
	}
	for _, wake := range []string{"heartbeat", "operator", "message", "host_ready"} {
		if r.Run(ctx, wake, true) != nil {
			t.Fatal("paused runner ran")
		}
	}
	if calls.Load() != 1 || len(r.wake) != 0 || r.Status().Activity.Phase != "paused" {
		t.Fatalf("paused calls=%d queued=%d status=%+v", calls.Load(), len(r.wake), r.Status())
	}
}

func TestPauseDuringReadinessPreventsCommanderAdmission(t *testing.T) {
	r := NewRunner(newStore(t), nil, "")
	r.ModelName = "fixture"
	entered, release := make(chan struct{}), make(chan struct{})
	r.DispatchReady = func() error { close(entered); <-release; return nil }
	calls := 0
	r.Commander = func(context.Context, string, bool) (RoundResult, error) { calls++; return RoundResult{}, nil }
	done := make(chan *Decision, 1)
	go func() { done <- r.Run(context.Background(), "operator", true) }()
	<-entered
	r.Pause()
	close(release)
	select {
	case d := <-done:
		if d != nil || calls != 0 {
			t.Fatalf("late admission=%+v calls=%d", d, calls)
		}
	case <-time.After(time.Second):
		t.Fatal("readiness did not return")
	}
}
