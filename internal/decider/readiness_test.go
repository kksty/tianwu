package decider

import (
	"context"
	"errors"
	"testing"
)

func TestDispatchReadinessPreservesFailureWithoutSpendingARound(t *testing.T) {
	r := NewRunner(newStore(t), nil, "")
	r.ModelName = "fixture"
	calls := 0
	r.Commander = func(context.Context, string, bool) (RoundResult, error) { calls++; return RoundResult{}, nil }
	reason := "本机宿主未就绪：API error (402): Insufficient Balance"
	r.DispatchReady = func() error { return errors.New(reason) }
	if got := r.Run(context.Background(), "operator", true); got != nil {
		t.Fatalf("blocked round = %+v", got)
	}
	if st := r.Status(); st.Activity.Detail != reason || st.Rounds != 0 || calls != 0 {
		t.Fatalf("blocked status = %+v; calls=%d", st, calls)
	}
	r.DispatchReady = func() error { return nil }
	if got := r.Run(context.Background(), "operator", true); got == nil || calls != 1 {
		t.Fatalf("recovered round=%+v calls=%d", got, calls)
	}
}
