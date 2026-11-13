package board

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// The commander's round prompt carries only the board's bounded top-N; without
// an explicit label a model can read those 20 rows as the whole library. The
// digest has to name the real totals, mark the cut, and point at the paginated
// enumeration entries. It is prompt-only data: the API/console board keeps its
// existing shape.
func TestPromptDigestLabelsTopNAndPointsAtCompleteEnumeration(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	for i := 0; i < 45; i++ {
		if _, err := st.CreateIntent(model.Intent{Title: fmt.Sprintf("方向 %02d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 60; i++ {
		if _, err := st.AddFact(model.Fact{Kind: "note", Title: fmt.Sprintf("观察 %02d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.AddGoal(model.Goal{Statement: "目标一"}); err != nil {
		t.Fatal(err)
	}

	v := Build(st, 20)
	if len(v.Open) != 20 || v.Intents.Pending != 45 || v.Facts.Total != 60 {
		t.Fatalf("fixture shape = open %d pending %d facts %d", len(v.Open), v.Intents.Pending, v.Facts.Total)
	}
	// The operator-facing board must stay byte-identical to before this field.
	apiJSON, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(apiJSON), "prompt_digest") {
		t.Fatalf("API board leaked the prompt-only digest: %s", apiJSON)
	}
	if v.PromptDigest != nil {
		t.Fatal("Build set a prompt digest on the operator view")
	}

	prompt := ForPrompt(v)
	if prompt.PromptDigest == nil {
		t.Fatal("commander prompt copy has no bounded-list digest")
	}
	d := prompt.PromptDigest
	if !strings.Contains(d.Note, "摘要") || !strings.Contains(d.Note, "未列出") {
		t.Fatalf("digest note does not disclaim completeness: %q", d.Note)
	}
	// The complete enumeration entries are read paths; none may be a promise
	// that the top-N list is complete.
	for _, q := range []string{d.IntentsQuery, d.FactsQuery, d.ChangesQuery, d.GraphQuery, d.FocusQuery} {
		if q == "" {
			t.Fatal("digest is missing a complete enumeration entry")
		}
	}
	byList := map[string]PromptBound{}
	for _, b := range d.Bounded {
		byList[b.List] = b
	}
	cases := []struct {
		list  string
		shown int
		total int
		cut   bool
	}{
		{"open_intents", 20, 45, true},
		{"recent_facts", 20, 60, true},
		{"goals.open", 1, 1, false},
	}
	for _, c := range cases {
		got, ok := byList[c.list]
		if !ok {
			t.Fatalf("digest missing list %q", c.list)
		}
		if got.Shown != c.shown || got.Total != c.total || got.Cut != c.cut {
			t.Fatalf("%s digest = %+v, want shown=%d total=%d cut=%v", c.list, got, c.shown, c.total, c.cut)
		}
	}

	// The digest rides the JSON the commander actually receives, and it does not
	// mutate the input view.
	raw, err := json.Marshal(prompt)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"prompt_digest"`) || !strings.Contains(string(raw), `"cut":true`) {
		t.Fatalf("commander prompt JSON does not carry the digest: %s", raw)
	}
	if v.PromptDigest != nil {
		t.Fatal("ForPrompt mutated the input view")
	}
}

// The digest totals come from the same snapshot counts as the board. A held
// direction is counted once in Pending and once in Held: the open total must not
// double-count it.
func TestPromptDigestDoesNotDoubleCountHeldOrClaimed(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	pending, _ := st.CreateIntent(model.Intent{Title: "pending"})
	held, _ := st.CreateIntent(model.Intent{Title: "held"})
	if _, err := st.UpdateIntent(held.ID, func(in *model.Intent) { in.Hold = true }); err != nil {
		t.Fatal(err)
	}
	_, _ = st.CreateIntent(model.Intent{Title: "another pending"})
	_ = pending

	v := ForPrompt(Build(st, 20))
	byList := map[string]PromptBound{}
	for _, b := range v.PromptDigest.Bounded {
		byList[b.List] = b
	}
	if got := byList["held_intents"]; got.Shown != 1 || got.Total != 1 {
		t.Fatalf("held digest = %+v", got)
	}
	// 3 pending intents, one held: 2 open + 0 claimed.
	if got := byList["open_intents"]; got.Shown != 2 || got.Total != 2 {
		t.Fatalf("open digest = %+v, want no double count of the held direction", got)
	}
	// The frontier is not the open set: it includes the held pending direction
	// (Build shows why it is stopped), so its total is 3 shows 3.
	if got := byList["frontier"]; got.Shown != 3 || got.Total != 3 {
		t.Fatalf("frontier digest = %+v, want the held direction included once", got)
	}
}
