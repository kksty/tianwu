package ledger

import (
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
	"path/filepath"
	"testing"
)

func TestRejectedFactCannotCloseGoal(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	e, err := st.AddEvidence(model.Evidence{URI: "in_1/proof.txt"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := st.AddFact(model.Fact{Kind: "vuln", Title: "refuted", Status: "rejected", EvidenceIDs: []string{e.ID}})
	if err != nil {
		t.Fatal(err)
	}
	g, err := st.AddGoal(model.Goal{Statement: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := CompleteGoal(st, g.ID, []string{f.ID}, "claim is enough")
	if err == nil && got.Met {
		t.Fatal("explicitly rejected fact was accepted as the basis for a completed goal")
	}
}
