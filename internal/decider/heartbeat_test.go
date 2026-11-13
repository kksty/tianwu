package decider

import (
	"context"
	"testing"

	"github.com/kksty/tianwu/internal/board"
	"github.com/kksty/tianwu/internal/model"
)

func TestNoNewFactsDoesNotImposeAStrategyOrFeeBreaker(t *testing.T) {
	r := NewRunner(newStore(t), nil, "")
	r.ModelName = "fixture"
	calls := 0
	r.Commander = func(context.Context, string, bool) (RoundResult, error) { calls++; return RoundResult{}, nil }
	for range 12 {
		d := r.Run(context.Background(), "manual", true)
		if d == nil || d.Fallback != "" || d.Error != "" {
			t.Fatalf("unexpected policy decision: %+v", d)
		}
	}
	if calls != 12 || r.stalled != 12 {
		t.Fatalf("calls=%d observation=%d", calls, r.stalled)
	}
}

// Durable event routing ignores commander bookkeeping while preserving a new
// unowned discovery, even if the raw board fingerprint changes in both cases.
func TestHeartbeatIgnoresTheRoundsOwnRunBookkeeping(t *testing.T) {
	st := newStore(t)
	in, err := st.CreateIntent(model.Intent{Title: "已经失败的方向", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Status = model.StatusFailed
	}); err != nil {
		t.Fatal(err)
	}

	before := board.Build(st, 20)
	r := NewRunner(st, nil, "")
	r.stalled = 3
	contentDigest := board.ContentDigest(before)
	// This fixture models a previous successful global delivery. The durable
	// attention cursor, rather than a process-local digest, owns that receipt.
	if err := st.AdvanceCommanderAttention(st.CommanderAttention().Through); err != nil {
		t.Fatal(err)
	}
	wakeDigest := board.Digest(before)

	// The round the heartbeat itself runs: one commander run row, then its
	// settle. This is bookkeeping, not content.
	run, err := st.AddRun(model.Run{IntentID: in.ID, Purpose: "commander"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateRun(run.ID, func(x *model.Run) {
		x.Status = model.RunDone
		x.ExitReason = "completed"
	}); err != nil {
		t.Fatal(err)
	}
	after := board.Build(st, 20)
	if board.ContentDigest(after) != contentDigest {
		t.Fatalf("test setup: run bookkeeping changed the content digest")
	}
	if board.Digest(after) == wakeDigest {
		t.Fatalf("test setup: run bookkeeping did not change the wake digest")
	}
	if r.heartbeatWorthwhile() {
		t.Fatal("a heartbeat woke for its own run bookkeeping: an idle project keeps paying for commander rounds")
	}

	// Content that the commander has not seen yet still deserves a round.
	if _, err := st.AddFact(model.Fact{Title: "新的侦察结果", IntentID: in.ID}); err != nil {
		t.Fatal(err)
	}
	if !r.heartbeatWorthwhile() {
		t.Fatal("a new fact must wake the heartbeat")
	}
}
