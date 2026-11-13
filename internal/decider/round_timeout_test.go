package decider

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/board"
	"github.com/kksty/tianwu/internal/state"
)

// A round is one decision. The commander runs as a long-lived harness session
// whose tools include "wait", so the contract has to come from the round rather
// than from the tool list: otherwise the session can park itself waiting for
// results that only enter through the next wake (D91).
func TestCommanderRoundPromptStatesTheDecisionContract(t *testing.T) {
	p := CommanderRoundPrompt(board.View{}, "笔记", "", ContextStatus{}, RoundContext{})
	for _, want := range []string{"这一轮的工作就是这一次决定", "本轮就结束了", "把本轮留着等结果"} {
		if !strings.Contains(p, want) {
			t.Fatalf("round prompt missing %q:\n%s", want, p)
		}
	}
	// The handoff round keeps its own instruction; a second, conflicting
	// "decide and dispatch" contract must not appear there.
	handoff := CommanderRoundPrompt(board.View{}, "笔记", "",
		ContextStatus{Used: 110000, Window: 128000}, RoundContext{})
	if strings.Contains(handoff, "这一轮的工作就是这一次决定") {
		t.Fatalf("handoff round duplicated the normal contract:\n%s", handoff)
	}
}

// A round that never ends queues every decision behind it: Run is synchronous.
// The wall-clock limit cuts it, but the cut is policy, not a model failure, so
// it must be recorded as a note (no d.Error) and keep the same session.
func TestRoundTimeoutCutsAParkedRoundWithoutFailingIt(t *testing.T) {
	r := NewRunner(newStore(t), state.New(filepath.Join(t.TempDir(), "state.md")), "")
	r.ModelName = "test-model"
	// The console sets it through this setter, so the test does too: the value
	// must be picked up without a restart and without racing the round.
	r.SetRoundTimeout(20 * time.Millisecond)
	var seen error
	r.Commander = func(ctx context.Context, round string, resume bool) (RoundResult, error) {
		<-ctx.Done()
		seen = context.Cause(ctx)
		return RoundResult{}, ctx.Err()
	}

	done := make(chan *Decision, 1)
	go func() { done <- r.Run(context.Background(), "wake", true) }()
	select {
	case d := <-done:
		if d == nil {
			t.Fatal("the round was skipped")
		}
		if !errors.Is(seen, ErrRoundTimeout) {
			t.Fatalf("round context cause = %v, want ErrRoundTimeout", seen)
		}
		if d.Error != "" {
			t.Fatalf("a cut round was recorded as a model failure: %q", d.Error)
		}
		if !hasNote(d.Notes, "墙钟上限") {
			t.Fatalf("the cut was not explained in the decision log: %v", d.Notes)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the round limit")
	}
}
