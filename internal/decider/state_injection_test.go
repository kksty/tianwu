package decider

// STATE injection timing (D89): a long commander session already carries the
// note in its own context, so re-pasting it every round burns tokens for
// nothing. The full text is reserved for the states where the memory was
// actually lost or rewritten: fresh start, after a lossy compaction, after the
// handoff round, and after a process restart. These tests pin that table.

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/board"
	"github.com/kksty/tianwu/internal/state"
)

const testNote = "唯一标记-NOTE-42：下一步验 /admin"

func promptFor(rc RoundContext) string {
	return CommanderRoundPrompt(board.View{}, testNote, "只测已授权范围", ContextStatus{}, rc)
}

func TestRoundPromptInjectsFullStateOnFreshStart(t *testing.T) {
	p := promptFor(RoundContext{Resume: false, StatePath: "/data/state.md"})
	if !strings.Contains(p, "## 你的笔记（state.md，全文）") || !strings.Contains(p, testNote) {
		t.Fatalf("fresh start did not inject the full note:\n%s", p)
	}
	for _, needle := range []string{"你没有上一轮的会话记忆", "先核对再动手", "历史结论只对当时条件成立"} {
		if !strings.Contains(p, needle) {
			t.Fatalf("fresh prompt missing %q:\n%s", needle, p)
		}
	}
}

func TestRoundPromptInjectsFullStateAfterCompaction(t *testing.T) {
	p := promptFor(RoundContext{Resume: true, Compacted: true, StatePath: "/data/state.md"})
	if !strings.Contains(p, "## 你的笔记（state.md，全文）") || !strings.Contains(p, testNote) {
		t.Fatalf("post-compaction round did not inject the full note:\n%s", p)
	}
	if !strings.Contains(p, "harness 刚压缩过上下文（有损）") || !strings.Contains(p, "历史建议") {
		t.Fatalf("post-compaction prompt does not say the memory was lossy:\n%s", p)
	}
}

// A note is memory, not an instruction (agent.md §3, audit F12.5): the full-note
// preambles must say the current effective instructions, authorization boundary
// and fact record versions win, and must not order the model to "execute" the
// note or treat its next step as the current command. This pins all four
// memory-loss branches, not just one.
func TestStateNoteIsHistoricalAdviceNotInstruction(t *testing.T) {
	cases := []struct {
		name string
		rc   RoundContext
	}{
		{"fresh session", RoundContext{Resume: false}},
		{"after compaction", RoundContext{Resume: true, Compacted: true}},
		{"after handoff round", RoundContext{Resume: true, Handoff: true}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			p := stateSection(testNote, tt.rc)
			if !strings.Contains(p, "历史") {
				t.Fatalf("note preamble does not describe the note as history:\n%s", p)
			}
			for _, want := range []string{"不是指令", "当前有效指令", "授权边界", "事实记录版本"} {
				if !strings.Contains(p, want) {
					t.Fatalf("note preamble missing %q:\n%s", want, p)
				}
			}
			for _, banned := range []string{"照它执行", "以它的「下一步」为准"} {
				if strings.Contains(p, banned) {
					t.Fatalf("note preamble still turns history into an order (%q):\n%s", banned, p)
				}
			}
		})
	}
	// The note-taking contract of the handoff round must carry the same rule.
	if strings.Contains(handoffSection, "照它执行") || !strings.Contains(handoffSection, "不是对下一轮的指令") {
		t.Fatalf("handoff note contract lost the historical-advice boundary:\n%s", handoffSection)
	}
}

func TestRoundPromptInjectsFullStateAfterHandoffRound(t *testing.T) {
	p := promptFor(RoundContext{Resume: true, Handoff: true, StatePath: "/data/state.md"})
	if !strings.Contains(p, "## 你的笔记（state.md，全文）") || !strings.Contains(p, testNote) {
		t.Fatalf("round after handoff did not inject the full note:\n%s", p)
	}
	if !strings.Contains(p, "上一轮是交接轮") {
		t.Fatalf("prompt does not say the note was rewritten:\n%s", p)
	}
}

// The handoff contract is "the note comes back in full on the round after the
// handoff" — once. The pressure-cycle flag that stops a second handoff must not
// keep re-pasting the note for the rest of the badge: those are ordinary pointer
// rounds, and the prompt must stop claiming the previous round was the handoff
// (audit 42 G1).
func TestHandoffNoteIsInjectedOnlyOnTheRoundAfterTheHandoff(t *testing.T) {
	st := newStore(t)
	mem := state.New(filepath.Join(t.TempDir(), "state.md"))
	if err := mem.Save("# 笔记\n" + testNote + "\n"); err != nil {
		t.Fatal(err)
	}
	var prompts []string
	r := NewRunner(st, mem, "")
	r.ModelName = "test-model"
	r.Session = func() RoundContext { return RoundContext{Resume: true} }
	r.Commander = func(ctx context.Context, p string, resume bool) (RoundResult, error) {
		prompts = append(prompts, p)
		return RoundResult{Tokens: 1, ContextTokens: 110000, ContextWindow: 128000}, nil
	}
	r.Run(context.Background(), "first", true)
	r.Run(context.Background(), "handoff", true)
	r.Run(context.Background(), "after-handoff", true)
	r.Run(context.Background(), "later", true)
	r.Run(context.Background(), "later-2", true)
	if len(prompts) != 5 {
		t.Fatalf("rounds = %d, want 5", len(prompts))
	}
	if !strings.Contains(prompts[1], "## 交接（本轮必做）") {
		t.Fatalf("round at 85%% was not the handoff round:\n%s", prompts[1])
	}
	if !strings.Contains(prompts[2], "## 你的笔记（state.md，全文）") || !strings.Contains(prompts[2], testNote) {
		t.Fatalf("the round after the handoff did not get the whole note:\n%s", prompts[2])
	}
	if !strings.Contains(prompts[2], "上一轮是交接轮") {
		t.Fatalf("the round after the handoff does not say the note was rewritten:\n%s", prompts[2])
	}
	for i, later := range prompts[3:] {
		if strings.Contains(later, "## 你的笔记（state.md，全文）") || strings.Contains(later, testNote) {
			t.Fatalf("round %d after the handoff re-pasted the note:\n%s", i+1, later)
		}
		if strings.Contains(later, "上一轮是交接轮") {
			t.Fatalf("round %d after the handoff still claims it was the previous round:\n%s", i+1, later)
		}
		if !strings.Contains(later, "跑过交接轮") {
			t.Fatalf("pressure round %d no longer knows the handoff already ran:\n%s", i+1, later)
		}
		if n := StateInjectionBytes(later); n != 0 {
			t.Fatalf("pointer round %d reported %d injected note bytes", i+1, n)
		}
	}
}

func TestRoundPromptInjectsFullStateAfterRestart(t *testing.T) {
	p := promptFor(RoundContext{Resume: true, Restarted: true, StatePath: "/data/state.md"})
	if !strings.Contains(p, "## 你的笔记（state.md，全文）") || !strings.Contains(p, testNote) {
		t.Fatalf("first round after restart did not inject the full note:\n%s", p)
	}
	if !strings.Contains(p, "进程重启过") {
		t.Fatalf("restart prompt does not warn about settled runs:\n%s", p)
	}
}

func TestPausedHandoffDoesNotCompletePressureCycle(t *testing.T) {
	r := NewRunner(newStore(t), nil, "")
	r.ModelName = "fixture"
	r.Session = func() RoundContext { return RoundContext{Resume: true} }
	r.lastContext, r.ContextWindow = 110000, 128000
	r.Commander = func(context.Context, string, bool) (RoundResult, error) {
		r.Pause()
		return RoundResult{ContextTokens: 110000}, nil
	}
	r.Run(context.Background(), "manual", true)
	if r.handoffDone || r.handoffRound {
		t.Fatal("canceled handoff was recorded as completed")
	}
	r.Resume()
	var prompt string
	r.Commander = func(_ context.Context, p string, _ bool) (RoundResult, error) {
		prompt = p
		return RoundResult{ContextTokens: 110000}, nil
	}
	r.Run(context.Background(), "manual", true)
	if !strings.Contains(prompt, "## 交接（本轮必做）") {
		t.Fatal("resumed pressure cycle skipped the interrupted handoff")
	}
}

func TestRoundPromptInjectsPointerOnlyOnNormalResume(t *testing.T) {
	p := promptFor(RoundContext{Resume: true, StatePath: "/data/projects/default/state.md"})
	if strings.Contains(p, testNote) {
		t.Fatalf("a normal continued round re-pasted the whole note:\n%s", p)
	}
	if !strings.Contains(p, "## 你的笔记（state.md）") {
		t.Fatalf("pointer section missing:\n%s", p)
	}
	for _, needle := range []string{"不要重复粘贴", "/data/projects/default/state.md", "GET /api/state"} {
		if !strings.Contains(p, needle) {
			t.Fatalf("pointer missing %q:\n%s", needle, p)
		}
	}
	// The pointer is a pointer: if it ever grows into a second copy of the note
	// the optimisation is gone.
	if n := len([]rune(stateSection(testNote, RoundContext{Resume: true, StatePath: "/data/projects/default/state.md"}))); n > 300 {
		t.Fatalf("pointer section grew to %d chars", n)
	}
}

func TestRoundPromptOmitsStateSectionWhenEmpty(t *testing.T) {
	p := CommanderRoundPrompt(board.View{}, "  ", "", ContextStatus{}, RoundContext{Resume: false})
	if strings.Contains(p, "## 你的笔记") {
		t.Fatalf("an empty note must not create an empty section:\n%s", p)
	}
}

// The API records "the whole note was injected" from the prompt itself; the
// helper must measure the full section and return 0 for the pointer.
func TestStateInjectionBytesAudit(t *testing.T) {
	full := promptFor(RoundContext{Resume: false, StatePath: "/data/state.md"})
	if n := StateInjectionBytes(full); n < len([]byte(testNote)) {
		t.Fatalf("full-note bytes = %d, want at least the note's %d", n, len([]byte(testNote)))
	}
	pointer := promptFor(RoundContext{Resume: true, StatePath: "/data/state.md"})
	if n := StateInjectionBytes(pointer); n != 0 {
		t.Fatalf("pointer-only prompt reported %d injected bytes", n)
	}
}

// The runner must pass the model-tier default line on ordinary rounds (it is a
// default, not a workflow) and keep it out of the handoff round, whose whole
// contract is "write the note and stop".
func TestRoundPromptCarriesTierDefaultOnOrdinaryRounds(t *testing.T) {
	ordinary := promptFor(RoundContext{Resume: true, StatePath: "/state.md"})
	for _, needle := range []string{"## 模型档位", "项目 worker_profile", "tier=worker", "tier=strong", "GET /api/model-profiles",
		"不决定职责或委派能力", "同一种模型可以承担全部职责", "按需要委派", "不追求层级齐全"} {
		if !strings.Contains(ordinary, needle) {
			t.Fatalf("ordinary prompt missing %q:\n%s", needle, ordinary)
		}
	}
	handoff := CommanderRoundPrompt(board.View{}, testNote, "", ContextStatus{Used: 110000, Window: 128000}, RoundContext{Resume: true})
	if strings.Contains(handoff, "## 模型档位") {
		t.Fatalf("handoff round must not carry the tiering default:\n%s", handoff)
	}
}

// The runner does not guess resume: the caller resolves it (only the executor
// knows whether the AOP session still exists) and the prompt follows.
func TestRunnerPassesSessionContextToPrompt(t *testing.T) {
	st := newStore(t)
	dir := t.TempDir()
	mem := state.New(filepath.Join(dir, "state.md"))
	if err := mem.Save("# 笔记\n" + testNote + "\n"); err != nil {
		t.Fatal(err)
	}
	var prompts []string
	r := NewRunner(st, mem, "")
	r.ModelName = "test-model"
	r.Commander = func(ctx context.Context, round string, resume bool) (RoundResult, error) {
		prompts = append(prompts, round)
		return RoundResult{Tokens: 1}, nil
	}
	// Round 1: a real resume — pointer only.
	r.Session = func() RoundContext { return RoundContext{Resume: true} }
	r.Run(context.Background(), "first", true)
	if strings.Contains(prompts[0], "## 你的笔记（state.md，全文）") || strings.Contains(prompts[0], testNote) {
		t.Fatalf("a resumed round got the full note:\n%s", prompts[0])
	}
	if !strings.Contains(prompts[0], "GET /api/state") {
		t.Fatalf("a resumed round has no pointer:\n%s", prompts[0])
	}
	// Round 2: the session was lost — inject the note in full.
	r.Session = func() RoundContext { return RoundContext{Resume: false} }
	r.Run(context.Background(), "second", true)
	if !strings.Contains(prompts[1], "## 你的笔记（state.md，全文）") || !strings.Contains(prompts[1], testNote) {
		t.Fatalf("a fresh round did not get the full note:\n%s", prompts[1])
	}
}

// A compaction is recorded by the runner itself, so even a caller that keeps
// reporting a live session must get the full note on the next round.
func TestRunnerInjectsFullNoteAfterCompaction(t *testing.T) {
	st := newStore(t)
	mem := state.New(filepath.Join(t.TempDir(), "state.md"))
	if err := mem.Save(testNote); err != nil {
		t.Fatal(err)
	}
	var prompts []string
	round := 0
	r := NewRunner(st, mem, "")
	r.ModelName = "test-model"
	r.Session = func() RoundContext { return RoundContext{Resume: true} }
	r.Commander = func(ctx context.Context, p string, resume bool) (RoundResult, error) {
		prompts = append(prompts, p)
		round++
		if round == 1 {
			return RoundResult{Tokens: 1, Compacted: true}, nil
		}
		return RoundResult{Tokens: 1}, nil
	}
	r.Run(context.Background(), "first", true)
	r.Run(context.Background(), "second", true)
	if strings.Contains(prompts[1], "## 你的笔记（state.md，全文）") == false || !strings.Contains(prompts[1], testNote) {
		t.Fatalf("round after compaction did not inject the full note:\n%s", prompts[1])
	}
}

// A note that has grown into a log is clipped at the injection boundary, not
// stored differently: the next whole-note round tells the commander to rewrite
// it. This is the "keep STATE under 150 lines" discipline applied where it
// matters — the prompt that carries it — without silently editing the file.
func TestLongNoteIsClippedToTheLineBudget(t *testing.T) {
	lines := make([]string, 0, StateFullMaxLines+20)
	for i := 1; i <= StateFullMaxLines+20; i++ {
		lines = append(lines, fmt.Sprintf("L%d", i))
	}
	note := strings.Join(lines, "\n")
	p := CommanderRoundPrompt(board.View{}, note, "", ContextStatus{}, RoundContext{Resume: false})

	if !strings.Contains(p, "L150") {
		t.Fatalf("the last line inside the budget is missing:\n%s", p)
	}
	if strings.Contains(p, "L151") {
		t.Fatalf("the note was injected past the line budget")
	}
	if !strings.Contains(p, "笔记超过 150 行") {
		t.Fatalf("clipped note does not tell the commander to rewrite it")
	}
	// A normal pointer round carries no full note, so it is unaffected.
	pointer := CommanderRoundPrompt(board.View{}, note, "", ContextStatus{}, RoundContext{Resume: true})
	if strings.Contains(pointer, "L150") || strings.Contains(pointer, "## 你的笔记（state.md，全文）") {
		t.Fatalf("pointer round leaked note content:\n%s", pointer)
	}
}
