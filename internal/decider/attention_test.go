package decider

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/retrieval"
)

func TestLocalProgressDoesNotOpenAnotherGlobalRound(t *testing.T) {
	s := newStore(t)
	_, _ = s.AddGoal(model.Goal{Statement: "goal"})
	p, _ := s.CreateIntent(model.Intent{Title: "parent"})
	p, _ = s.UpdateIntent(p.ID, func(i *model.Intent) { i.Status = model.StatusRunning })
	r := NewRunner(s, nil, "")
	r.ModelName = "fixture"
	calls := 0
	r.Commander = func(context.Context, string, bool) (RoundResult, error) {
		calls++
		d, _ := s.PendingDelivery("commander")
		return RoundResult{}, s.AcknowledgeDelivery("commander", d.ID)
	}
	if r.Run(context.Background(), "operator", true) == nil {
		t.Fatal("no initial round")
	}
	c, _, _ := s.CreateChildIntent(p.ID, model.Intent{Title: "child", IdempotencyKey: "child"})
	_, _ = s.AddFact(model.Fact{IntentID: c.ID, Title: "local result"})
	_, _ = s.UpdateIntent(c.ID, func(i *model.Intent) { i.Status = model.StatusDone })
	if d := r.Run(context.Background(), "intent_done", false); d != nil || calls != 1 {
		t.Fatalf("local work opened global model: %+v calls=%d", d, calls)
	}
	if r.heartbeatWorthwhile() {
		t.Fatal("local context backlog bought a heartbeat round")
	}
}

func TestFailedGlobalCallRetainsEventsAndBacksOffAutomaticCalls(t *testing.T) {
	s := newStore(t)
	_, _ = s.AddGoal(model.Goal{Statement: "goal"})
	r := NewRunner(s, nil, "")
	r.ModelName = "fixture"
	calls := 0
	r.Commander = func(context.Context, string, bool) (RoundResult, error) {
		calls++
		return RoundResult{}, errors.New("API unavailable")
	}
	before := s.CommanderAttention().DeliveredThrough
	d := r.Run(context.Background(), "operator", true)
	if d == nil || d.Error == "" || d.AttentionDelivered {
		t.Fatal("failure was marked delivered")
	}
	if s.CommanderAttention().DeliveredThrough != before {
		t.Fatal("failed API consumed events")
	}
	if r.Run(context.Background(), "fact", false) != nil || calls != 1 {
		t.Fatal("failure storm bypassed recovery backoff")
	}
	r.Pause()
	if r.Run(context.Background(), "operator", true) != nil {
		t.Fatal("forced call bypassed pause")
	}
	r.Resume()
	r.Commander = func(context.Context, string, bool) (RoundResult, error) {
		calls++
		d, _ := s.PendingDelivery("commander")
		return RoundResult{}, s.AcknowledgeDelivery("commander", d.ID)
	}
	if got := r.Run(context.Background(), "operator", true); got == nil || !got.AttentionDelivered || calls != 2 {
		t.Fatal("recovery did not consume original demand")
	}
}

func TestPauseDuringSuccessfulTransportDoesNotConsumeEvents(t *testing.T) {
	s := newStore(t)
	_, _ = s.AddGoal(model.Goal{Statement: "goal"})
	r := NewRunner(s, nil, "")
	r.ModelName = "fixture"
	r.Commander = func(context.Context, string, bool) (RoundResult, error) { r.Pause(); return RoundResult{}, nil }
	d := r.Run(context.Background(), "operator", true)
	if d == nil || d.AttentionDelivered || s.CommanderAttention().DeliveredThrough != 0 {
		t.Fatal("pause consumed pending events")
	}
	ctx, err := retrieval.WorkingContext(s, "commander")
	if err != nil || ctx.DeliveryID == "" {
		t.Fatal("pause lost pending input")
	}
}

func TestEventArrivingDuringRoundRemainsForNextGlobalDecision(t *testing.T) {
	s := newStore(t)
	_, _ = s.AddGoal(model.Goal{Statement: "first"})
	r := NewRunner(s, nil, "")
	r.ModelName = "fixture"
	r.Commander = func(context.Context, string, bool) (RoundResult, error) {
		d, _ := s.PendingDelivery("commander")
		if err := s.AcknowledgeDelivery("commander", d.ID); err != nil {
			return RoundResult{}, err
		}
		_, err := s.AddGoal(model.Goal{Statement: "arrived during round"})
		return RoundResult{}, err
	}
	if d := r.Run(context.Background(), "operator", true); d == nil || !d.AttentionDelivered {
		t.Fatal("first delivery failed")
	}
	if len(s.CommanderAttention().Reasons) == 0 {
		t.Fatal("round consumed an event it did not receive")
	}
}

func TestRestartedRunnerFindsPendingGlobalEventAfterAPIError(t *testing.T) {
	s := newStore(t)
	_, _ = s.AddGoal(model.Goal{Statement: "goal"})
	r := NewRunner(s, nil, "")
	r.ModelName = "fixture"
	r.Commander = func(context.Context, string, bool) (RoundResult, error) {
		return RoundResult{}, errors.New("API unavailable")
	}
	r.Run(context.Background(), "operator", true)
	next := NewRunner(s, nil, "")
	next.ModelName = "fixture"
	next.Heartbeat = time.Millisecond
	next.Commander = func(context.Context, string, bool) (RoundResult, error) {
		d, _ := s.PendingDelivery("commander")
		return RoundResult{}, s.AcknowledgeDelivery("commander", d.ID)
	}
	if d := next.Run(context.Background(), "heartbeat", false); d == nil || !d.AttentionDelivered {
		t.Fatal("runner restart lost durable demand")
	}
}
