package retrieval

import (
	"testing"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// Explicit references are a different edge from a required premise. The graph
// must carry both without inventing a causal premise for a reading choice, and
// a reference must be reachable in both directions.
func TestIntentReferencesAreReferenceEdgesNotPremises(t *testing.T) {
	s := ledger(t)
	premise, err := s.AddFact(model.Fact{IntentID: "in_other", Kind: "note", Title: "前提"})
	if err != nil {
		t.Fatal(err)
	}
	ref, err := s.AddFact(model.Fact{IntentID: "in_other", Kind: "note", Title: "参考"})
	if err != nil {
		t.Fatal(err)
	}
	in, err := s.CreateIntent(model.Intent{Title: "task", FromFactIDs: []string{premise.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateTaskContext(in.ID, "", store.TaskContext{Version: 0, References: []string{ref.ID}}); err != nil {
		t.Fatal(err)
	}

	page, err := Graph(s, GraphQuery{Roots: []string{"intent:" + in.ID}, Direction: "both", Depth: 0, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, edge := range page.Edges {
		kinds[edge.Source+"\x00"+edge.Target] = edge.Kind
	}
	if got := kinds["fact:"+premise.ID+"\x00intent:"+in.ID]; got != "premise" {
		t.Fatalf("required premise edge = %q", got)
	}
	if got := kinds["fact:"+ref.ID+"\x00intent:"+in.ID]; got != "reference" {
		t.Fatalf("context reference edge = %q", got)
	}
	found := map[string]bool{}
	for _, f := range page.Facts {
		found[f.ID] = true
	}
	if !found[premise.ID] || !found[ref.ID] {
		t.Fatalf("graph did not reach both facts: %+v", found)
	}

	// From the reference fact, the intent is a descendant through the same
	// recorded edge: the context selection is part of the graph, not a
	// display-only field.
	back, err := Graph(s, GraphQuery{Roots: []string{"fact:" + ref.ID}, Direction: "descendants", Depth: 0, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	foundIntent := false
	for _, other := range back.Intents {
		if other.ID == in.ID {
			foundIntent = true
		}
	}
	if !foundIntent {
		t.Fatal("reference fact did not lead back to the intent")
	}
}
