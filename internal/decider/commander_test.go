package decider

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/board"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/state"
	"github.com/kksty/tianwu/internal/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// The commander cannot see its own context size; the round prompt is the only
// place that tells it. Without the number the 85% handoff can never fire.
func TestRoundPromptReportsContextPressure(t *testing.T) {
	p := CommanderRoundPrompt(board.View{}, "笔记", "", ContextStatus{Used: 100000, Window: 128000}, RoundContext{})
	if !strings.Contains(p, "100000/128000（78%）") {
		t.Fatalf("prompt does not report pressure:\n%s", p)
	}
	if strings.Contains(p, "## 交接（本轮必做）") {
		t.Fatalf("78%% must not be a handoff round:\n%s", p)
	}
}

// At 85% the round stops dispatching and rewrites the note; the harness then
// compacts, and the note is what survives (D80).
func TestHandoffRoundAtEightyFivePercent(t *testing.T) {
	p := CommanderRoundPrompt(board.View{}, "笔记", "", ContextStatus{Used: 110000, Window: 128000}, RoundContext{})
	for _, needle := range []string{
		"上下文已经到 85%", "不派新方向", "## 交接（本轮必做）",
		"`PUT /api/state`", "整份重写", "留什么、删什么由你定", "你回来后的第一个动作",
		"不是轮次日志",
	} {
		if !strings.Contains(p, needle) {
			t.Fatalf("handoff prompt missing %q:\n%s", needle, p)
		}
	}
}

// The note is the commander's own file, so the prompt has to say what belongs
// in it (facts and judgement, not a round log) and that it may delete/rewrite
// it. Otherwise the model treats state.md as an append-only activity log and
// every handoff carries dead weight (D106).
func TestStatePromptSaysTheNoteIsEditable(t *testing.T) {
	for _, rc := range []RoundContext{
		{Resume: false, StatePath: "/data/state.md"}, // full note
		{Resume: true, StatePath: "/data/state.md"},  // pointer only
	} {
		p := CommanderRoundPrompt(board.View{}, "笔记", "", ContextStatus{}, rc)
		for _, needle := range []string{"不是轮次日志", "只写对下一轮有价值的事实与判断", "直接删掉", "整份重写或删改"} {
			if !strings.Contains(p, needle) {
				t.Fatalf("note prompt (resume=%v) missing %q:\n%s", rc.Resume, needle, p)
			}
		}
	}
}

// 80–85% is the warning band: the note must be kept current before the handoff
// round arrives, but dispatching is still allowed.
func TestWarnRoundBeforeHandoff(t *testing.T) {
	p := CommanderRoundPrompt(board.View{}, "笔记", "", ContextStatus{Used: 104000, Window: 128000}, RoundContext{})
	if !strings.Contains(p, "接近交接线") {
		t.Fatalf("81%% must warn about the handoff line:\n%s", p)
	}
	if strings.Contains(p, "## 交接（本轮必做）") {
		t.Fatalf("81%% must not be a handoff round:\n%s", p)
	}
}

// After a compaction the model's recent memory is gone; the round must say so
// and point at the note instead of letting it re-scout from scratch.
func TestCompactedRoundContinuesFromNote(t *testing.T) {
	p := CommanderRoundPrompt(board.View{}, "笔记", "", ContextStatus{Used: 20000, Window: 128000, Compacted: true}, RoundContext{})
	for _, needle := range []string{"压缩过上下文", "按当前授权和新观察自行决定下一步", "当时条件与判断"} {
		if !strings.Contains(p, needle) {
			t.Fatalf("post-compaction prompt missing %q:\n%s", needle, p)
		}
	}
	if strings.Contains(p, "## 交接（本轮必做）") {
		t.Fatalf("a compacted 16%% session is not a handoff round:\n%s", p)
	}
}

// An unconfigured window is "no signal", not 0% and not a handoff.
func TestUnknownWindowHasNoPressureSection(t *testing.T) {
	p := CommanderRoundPrompt(board.View{}, "笔记", "", ContextStatus{Used: 50000}, RoundContext{})
	if strings.Contains(p, "## 上下文") {
		t.Fatalf("unknown window must not invent pressure:\n%s", p)
	}
	if (ContextStatus{Used: 50000, Window: 0}).Handoff() {
		t.Fatal("unknown window must never schedule a handoff")
	}
}

// Sapper rewrites STATE once and then continues the loop; the 85% line must not
// turn every later round into another handoff.
func TestHandoffHappensOncePerPressureCycle(t *testing.T) {
	c := ContextStatus{Used: 110000, Window: 128000}
	if !c.Handoff() {
		t.Fatal("85% must schedule the first handoff")
	}
	c.HandoffDone = true
	if c.Handoff() {
		t.Fatal("a second round in the same cycle repeated the handoff")
	}
}

// The runner carries the session's size and the compaction flag from one round
// to the next: that is how the 85% round and the post-compaction round happen.
func TestRunnerCarriesContextAcrossRounds(t *testing.T) {
	st := newStore(t)
	mem := state.New(filepath.Join(t.TempDir(), "state.md"))
	var prompts []string
	r := NewRunner(st, mem, "")
	r.ModelName = "test-model"
	r.ContextWindow = 128000
	r.Commander = func(ctx context.Context, round string, resume bool) (RoundResult, error) {
		prompts = append(prompts, round)
		switch len(prompts) {
		case 1:
			return RoundResult{Tokens: 10, ContextTokens: 110000}, nil
		case 2:
			return RoundResult{Tokens: 5, ContextTokens: 20000, Compacted: true,
				Actions: []Action{{Kind: "state", Text: "交接"}}}, nil
		default:
			return RoundResult{Tokens: 1}, nil
		}
	}

	r.Run(context.Background(), "first", true)
	before := r.Status().Stalled
	d := r.Run(context.Background(), "second", true)
	if !strings.Contains(prompts[1], "## 交接（本轮必做）") {
		t.Fatalf("second round is not a handoff round:\n%s", prompts[1])
	}
	if r.Status().Stalled != before {
		t.Fatal("a handoff round must not count as a stalled direction")
	}
	if !hasNote(d.Notes, "交接轮") || hasNote(d.Notes, "没有写笔记") {
		t.Fatalf("handoff notes = %v", d.Notes)
	}

	r.Run(context.Background(), "third", true)
	if !strings.Contains(prompts[2], "压缩过上下文") {
		t.Fatalf("third round does not know it was compacted:\n%s", prompts[2])
	}
}

// A handoff round that never writes state.md is the failure mode the protocol
// exists to prevent; the decision log must say it, not hide it. And the cycle
// hands off once: the next round must dispatch again instead of rewriting the
// same note forever above 85%.
func TestHandoffRoundWithoutNoteIsRecorded(t *testing.T) {
	st := newStore(t)
	r := NewRunner(st, state.New(filepath.Join(t.TempDir(), "state.md")), "")
	r.ModelName = "test-model"
	r.ContextWindow = 128000
	var rounds int
	var prompts []string
	r.Commander = func(ctx context.Context, round string, resume bool) (RoundResult, error) {
		rounds++
		prompts = append(prompts, round)
		if rounds == 1 {
			return RoundResult{Tokens: 1, ContextTokens: 110000}, nil
		}
		return RoundResult{Tokens: 1, ContextTokens: 112000}, nil
	}
	r.Run(context.Background(), "first", true)
	d := r.Run(context.Background(), "handoff", true)
	if !hasNote(d.Notes, "没有写笔记") {
		t.Fatalf("missing handoff was not recorded: %v", d.Notes)
	}
	d = r.Run(context.Background(), "after", true)
	if strings.Contains(prompts[2], "## 交接（本轮必做）") {
		t.Fatalf("the handoff repeated in the same pressure cycle:\n%s", prompts[2])
	}
	if !strings.Contains(prompts[2], "跑过交接轮") {
		t.Fatalf("prompt does not say the handoff already ran:\n%s", prompts[2])
	}
	if hasNote(d.Notes, "交接轮") {
		t.Fatalf("a normal round was labelled a handoff: %v", d.Notes)
	}
}

func hasNote(notes []string, want string) bool {
	for _, n := range notes {
		if strings.Contains(n, want) {
			return true
		}
	}
	return false
}

// A restart resumes the AOP session, so the harness context is not empty even
// though the Runner is new. Without seeding, the first rounds would never see
// the 85% line and the harness would compress without a fresh note (D80).
func TestSeedContextAfterRestart(t *testing.T) {
	st := newStore(t)
	run, err := st.AddRun(model.Run{Purpose: "commander"})
	if err != nil {
		t.Fatal(err)
	}
	st.ProgressRun(run.ID, 900000)
	if _, err := st.UpdateRun(run.ID, func(r *model.Run) { r.ContextWindow = 1000000 }); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(st, state.New(filepath.Join(t.TempDir(), "state.md")), "")
	r.ContextWindow = 128000 // the profile left it "auto"
	r.seedContext()
	if got := r.windowLocked(); got != 1000000 {
		t.Fatalf("seeded window = %d, want the harness-reported 1000000", got)
	}
	status := ContextStatus{Used: r.lastContext, Window: r.windowLocked()}
	if status.Percent() != 90 || !status.Handoff() {
		t.Fatalf("seeded pressure = %d%%, want a 90%% handoff", status.Percent())
	}
}

// The harness resolves the model's real window and reports it with usage; the
// control plane must measure against that, not against its profile fallback
// (D80).
func TestRunnerUsesHarnessReportedWindow(t *testing.T) {
	st := newStore(t)
	var prompts []string
	r := NewRunner(st, state.New(filepath.Join(t.TempDir(), "state.md")), "")
	r.ModelName = "test-model"
	r.ContextWindow = 128000 // profile "auto" fallback
	r.Commander = func(ctx context.Context, round string, resume bool) (RoundResult, error) {
		prompts = append(prompts, round)
		if len(prompts) == 1 {
			return RoundResult{Tokens: 1, ContextTokens: 500000, ContextWindow: 1000000}, nil
		}
		return RoundResult{Tokens: 1}, nil
	}
	r.Run(context.Background(), "first", true)
	r.Run(context.Background(), "second", true)
	if !strings.Contains(prompts[1], "500000/1000000（50%）") {
		t.Fatalf("second round did not use the harness window:\n%s", prompts[1])
	}
	if strings.Contains(prompts[1], "## 交接（本轮必做）") {
		t.Fatal("50% of the harness window must not be a handoff round")
	}
}

// A round hands the board to the harness commander instead of calling a model
// here; the session's token total lands in the decision log (D78).
func TestCommanderRunsTheRound(t *testing.T) {
	st := newStore(t)
	mem := state.New(filepath.Join(t.TempDir(), "state.md"))
	var got string
	var resume bool
	r := NewRunner(st, mem, "")
	r.ModelName = "test-model"
	r.Commander = func(ctx context.Context, round string, res bool) (RoundResult, error) {
		got, resume = round, res
		return RoundResult{Tokens: 1234, Actions: []Action{{Kind: "intent", ID: "in_1", Text: "验证"}}}, nil
	}
	d := r.Run(context.Background(), "test", true)
	if d == nil || d.Error != "" {
		t.Fatalf("decision = %+v", d)
	}
	if !resume {
		t.Fatal("the runner should ask to continue the session")
	}
	for _, needle := range []string{"## board", "## 本轮"} {
		if !strings.Contains(got, needle) {
			t.Fatalf("round prompt missing %q: %q", needle, got)
		}
	}
	if len(d.Actions) != 1 || d.Actions[0].Kind != "intent" {
		t.Fatalf("actions = %+v, want the API-recorded action", d.Actions)
	}
	if d.Tokens != 1234 {
		t.Fatalf("tokens = %d, want the session's total", d.Tokens)
	}
}

// A commander round stopped by the liveness detector must land as the round's
// error in the decision log and in the decider status: that is what the
// console's commander card shows, and what tells the operator the round was cut
// by model_stall rather than completing normally (D107). It must not be
// recorded as the usual "由 harness 指挥官会话执行" note.
func TestCommanderStallIsRecordedAsRoundError(t *testing.T) {
	st := newStore(t)
	r := NewRunner(st, state.New(filepath.Join(t.TempDir(), "state.md")), "")
	r.ModelName = "test-model"
	r.Commander = func(ctx context.Context, round string, resume bool) (RoundResult, error) {
		return RoundResult{Tokens: 7}, ErrCommanderStalled
	}
	d := r.Run(context.Background(), "first", true)
	if d == nil || !strings.Contains(d.Error, "model_stall") {
		t.Fatalf("decision = %+v, want the stall as the round error", d)
	}
	if hasNote(d.Notes, "由 harness 指挥官会话执行") {
		t.Fatalf("a stalled round was recorded as a normal round: %v", d.Notes)
	}
	if got := r.Status().LastError; !strings.Contains(got, "model_stall") {
		t.Fatalf("decider last_error = %q, want the stall reason", got)
	}
}
