package board

import (
	"path/filepath"
	"testing"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// The frontier carries the DAG hygiene signals the commander needs: zero-premise
// directions, premises whose assertion changed since creation, and how much open
// work hangs below a direction. None of these change readiness; they are
// judgement inputs.
func TestFrontierMarksPremiseStateWithoutChangingReadiness(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	producer, _ := st.CreateIntent(model.Intent{Title: "producer"})
	premise, err := st.AddFact(model.Fact{IntentID: producer.ID, Kind: "vuln", Title: "premise", Summary: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := st.CreateIntent(model.Intent{Title: "consumer", FromFactIDs: []string{premise.ID}})
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := st.CreateIntent(model.Intent{Title: "bootstrap"})
	if err != nil {
		t.Fatal(err)
	}
	byID := func() map[string]FrontierItem {
		out := map[string]FrontierItem{}
		for _, fi := range Build(st, 20).Frontier {
			out[fi.ID] = fi
		}
		return out
	}
	frontier := byID()
	item := frontier[consumer.ID]
	if item.Prerequisites != 1 || item.NoPremise || len(item.PremiseChanged) != 0 || !item.Ready {
		t.Fatalf("consumer frontier = %+v", item)
	}
	if boot := frontier[bootstrap.ID]; !boot.NoPremise || boot.Prerequisites != 0 {
		t.Fatalf("bootstrap frontier = %+v", boot)
	}

	// A claim revision makes the premise "changed"; it does not block dispatch
	// by itself (the record still exists and is not rejected).
	if _, err := st.UpdateFact(premise.ID, func(f *model.Fact) { f.Summary = "v2" }); err != nil {
		t.Fatal(err)
	}
	item = byID()[consumer.ID]
	if len(item.PremiseChanged) != 1 || item.PremiseChanged[0] != premise.ID || !item.Ready {
		t.Fatalf("changed premise marker = %+v", item)
	}

	// A pending child is open work under the direction.
	if _, err := st.CreateIntent(model.Intent{Title: "child", ParentID: consumer.ID}); err != nil {
		t.Fatal(err)
	}
	item = byID()[consumer.ID]
	if item.Descendants != 1 {
		t.Fatalf("descendants = %d, want 1", item.Descendants)
	}
}
