package decider

import (
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/board"
)

// The role has to be in every round prompt, not only in the first turn: a long
// session can be compacted or resumed without the skill turn still in context.
func TestCommanderRoundPromptStatesTheRoleFirst(t *testing.T) {
	p := CommanderRoundPrompt(board.View{}, "笔记", "10.0.0.0/24", ContextStatus{}, RoundContext{})
	role := strings.Index(p, "你是天吴项目的指挥官")
	if role < 0 {
		t.Fatalf("round prompt does not name the role:\n%s", p)
	}
	if role > strings.Index(p, "## 本轮") {
		t.Fatalf("role must lead the prompt, before the situation section:\n%s", p)
	}
	// A handoff round swaps the decision contract for the handoff one, but it is
	// still the commander reading it.
	h := CommanderRoundPrompt(board.View{}, "笔记", "", ContextStatus{Used: 110000, Window: 128000}, RoundContext{Resume: true})
	if !strings.Contains(h, "你是天吴项目的指挥官") {
		t.Fatalf("handoff round lost the role:\n%s", h)
	}
}

// The board in the prompt is the summary layer: long summaries are clipped and
// the full text is read by id, exactly like sapper/nvwabase's title→summary→doc
// split (docs/17 §3.1). Ids, titles and counts must survive the projection.
func TestCommanderRoundPromptBoardIsTheSummaryLayer(t *testing.T) {
	long := strings.Repeat("细节", 300) // 600 runes, over the prompt budget
	b := board.View{
		Intents: board.Counts{Total: 1, Running: 1},
		Running: []board.Item{{
			ID: "in_x", Title: "在跑的方向", Status: "running", Summary: long,
		}},
		RecentFacts: []board.Item{{
			ID: "f_x", Title: "一条事实", Status: "verified", Summary: long,
		}},
	}

	p := CommanderRoundPrompt(b, "笔记", "", ContextStatus{}, RoundContext{})

	if strings.Contains(p, long) {
		t.Fatal("the round prompt still carries the full summary")
	}
	for _, want := range []string{`"id":"in_x"`, `"title":"在跑的方向"`, `"id":"f_x"`, "…"} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt missing %q:\n%s", want, p)
		}
	}
	if b.Running[0].Summary != long {
		t.Fatal("round prompt mutated the board it was given")
	}
}
