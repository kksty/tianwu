package decider

import (
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/board"
)

// The commander's long session gets pointer-only entries for the new read
// surfaces; it must not need to guess that focus/resources/context editing exist.
func TestCommanderRoundPromptNamesCurrentReadEntries(t *testing.T) {
	prompt := CommanderRoundPrompt(board.View{}, "", "10.0.0.0/24", ContextStatus{}, RoundContext{Resume: true})
	for _, want := range []string{
		"/api/focus?intent=in_x",
		"/api/resources?intent=in_x",
		"/api/intents/in_x/context",
		"premise_claim_versions",
		"context_fact_versions",
		"## 授权边界（当前口径）",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("commander prompt missing %q:\n%s", want, prompt)
		}
	}
	// The premise discipline is re-anchored every round, not only in the skill.
	if !strings.Contains(prompt, "from_fact_ids") {
		t.Fatalf("commander identity lost the premise contract:\n%s", prompt)
	}
	// Goals are operator-defined: the round prompt must say so and must not
	// invite the commander to create, split or rewrite one.
	if !strings.Contains(prompt, "目标由操作员定义") {
		t.Fatalf("commander identity lost the operator-owned goal contract:\n%s", prompt)
	}
	if strings.Contains(prompt, "POST /api/goals") {
		t.Fatalf("commander identity still invites goal creation:\n%s", prompt)
	}
	// Emergent collaboration: executors may self-organize; the commander sets
	// direction and acceptance, not a role hierarchy.
	for _, want := range []string{"直接互发消息", "不制造层级", "不强制每个方向都先拆分"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("commander identity lost collaboration autonomy %q:\n%s", want, prompt)
		}
	}
	for _, forbidden := range []string{"将军", "士兵", "协调者"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("commander identity reintroduced a role name %q:\n%s", forbidden, prompt)
		}
	}
	// Without injected instructions the fallback insertion must not corrupt the
	// layout: authorization still precedes board.
	if strings.Index(prompt, "## 授权边界（当前口径）") > strings.Index(prompt, "## board\n") {
		t.Fatalf("scope placed after board:\n%s", prompt)
	}
}
