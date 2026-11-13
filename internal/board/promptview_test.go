package board

import (
	"strings"
	"testing"
)

// The commander prompt gets the summary layer: long model-written prose is
// clipped, while ids, titles, counts and the frontier stay intact so the round
// can still decide and name what it wants to read (docs/17 §3.1).
func TestForPromptClipsSummariesAndKeepsShape(t *testing.T) {
	long := strings.Repeat("细", PromptSummaryRunes+60)
	v := View{
		Intents: Counts{Total: 2, Running: 1},
		Running: []Item{{
			ID: "in_1", Title: "方向一", Summary: long, Status: "running", Attempts: 2,
		}},
		RecentFacts: []Item{{
			ID: "f_1", Title: "事实一", Summary: long, Status: "verified", Evidence: 3,
		}},
		Frontier: []FrontierItem{{
			ID: "in_2", Title: "方向二", Ready: false, Blocked: []string{"f_x（已否定）"},
		}},
	}

	got := ForPrompt(v)

	if n := len([]rune(got.Running[0].Summary)); n != PromptSummaryRunes+1 {
		t.Fatalf("running summary runes = %d, want %d (+ellipsis)", n, PromptSummaryRunes+1)
	}
	if !strings.HasSuffix(got.Running[0].Summary, "…") {
		t.Fatalf("clipped summary lost its truncation marker: %q", got.Running[0].Summary)
	}
	if got.Running[0].ID != "in_1" || got.Running[0].Title != "方向一" ||
		got.Running[0].Status != "running" || got.Running[0].Attempts != 2 {
		t.Fatalf("projection changed identity/state: %+v", got.Running[0])
	}
	if got.Intents != v.Intents {
		t.Fatalf("counts changed: %+v", got.Intents)
	}
	if len(got.RecentFacts) != 1 || got.RecentFacts[0].Evidence != 3 {
		t.Fatalf("fact metadata lost: %+v", got.RecentFacts)
	}
	if got.Frontier[0].Blocked[0] != "f_x（已否定）" {
		t.Fatalf("frontier premise lost: %+v", got.Frontier)
	}

	// The caller's view (built for the API/console) must stay complete.
	if v.Running[0].Summary != long {
		t.Fatal("ForPrompt mutated the input view")
	}
}

func TestForPromptLeavesShortTextAlone(t *testing.T) {
	v := View{Running: []Item{{ID: "in_1", Summary: "短摘要"}}}
	if got := ForPrompt(v).Running[0].Summary; got != "短摘要" {
		t.Fatalf("short summary changed: %q", got)
	}
}
