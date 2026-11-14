package decider

import (
	"context"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/model"
)

// A commander round that decides the engagement should stop asks for StandbyFor.
// Unlike the operator's PauseFor kill switch, it must NOT cancel the round that
// asked: that round is producing the answer, the state.md update and the delivery
// checkpoint the operator needs, and a canceled round would be replayed after the
// explicit resume. The pause must still take effect for every later wake.
func TestCommanderStandbyFinishesTheAskingRoundAndDropsLaterWakes(t *testing.T) {
	r := NewRunner(newStore(t), nil, "")
	r.ModelName = "fixture"
	calls := 0
	r.Commander = func(ctx context.Context, _ string, _ bool) (RoundResult, error) {
		calls++
		r.StandbyFor("目标不可达，请求停止项目")
		if ctx.Err() != nil {
			t.Error("standby canceled the round that requested it")
		}
		return RoundResult{Tokens: 7}, nil
	}

	d := r.Run(context.Background(), "operator", true)
	if d == nil || d.Tokens != 7 || d.Error != "" {
		t.Fatalf("standby round did not finish normally: %+v", d)
	}
	if len(d.Notes) == 0 || !strings.Contains(d.Notes[0], "由 harness 指挥官会话执行") {
		t.Fatalf("standby round was recorded as canceled: notes=%v", d.Notes)
	}
	if !r.Paused() {
		t.Fatal("standby did not pause the wake loop")
	}
	if reason := r.Status().PausedReason; !strings.Contains(reason, "目标不可达") {
		t.Fatalf("pause reason = %q, want the commander's reason", reason)
	}

	// Six months of heartbeat in a test: no queued wake may buy another round
	// while paused, and a forced wake must hit the same gate.
	for _, wake := range []string{"heartbeat", "message", "operator", "host_ready", "intent_done"} {
		if got := r.Run(context.Background(), wake, true); got != nil {
			t.Fatalf("paused runner ran for %s: %+v", wake, got)
		}
	}
	if calls != 1 || len(r.wake) != 0 {
		t.Fatalf("calls=%d queued=%d", calls, len(r.wake))
	}
	if phase := r.Status().Activity.Phase; phase != "paused" {
		t.Fatalf("activity phase = %q, want paused", phase)
	}

	// Operator resume is explicit and re-arms the loop.
	r.Resume()
	if r.Paused() {
		t.Fatal("resume left the runner paused")
	}
}

// The standby round still finished its input, so its delivery checkpoint must
// advance. Without the selfPaused exception after the resume, the native message
// and unowned_result that the round already handled would be replayed as fresh
// demand and spend another round — exactly the wake that burned 431k tokens on an
// unreachable-target project.
func TestCommanderStandbyRoundStillCheckpointsItsAttention(t *testing.T) {
	st := newStore(t)
	if _, err := st.AddFact(model.Fact{Title: "target unreachable"}); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(st, nil, "")
	r.ModelName = "fixture"
	r.Commander = func(context.Context, string, bool) (RoundResult, error) {
		r.StandbyFor("目标不可达，请求停止项目")
		return RoundResult{Tokens: 5}, nil
	}
	d := r.Run(context.Background(), "fact", true)
	if d == nil || !d.AttentionDelivered {
		t.Fatalf("standby round lost its delivery checkpoint: %+v", d)
	}
	if got := st.CommanderAttention(); len(got.Reasons) != 0 {
		t.Fatalf("handled change was left pending for the next round: %+v", got.Reasons)
	}
}
