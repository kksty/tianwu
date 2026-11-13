package store

import (
	"testing"

	"github.com/kksty/tianwu/internal/model"
)

// A completion receipt must keep saying what was proven when it was stamped. A
// later correction of one cited fact is a signal for a human/model to re-judge,
// not permission to rewrite the historical stamp (F9/D97).
func TestCompletionReceiptFlagsChangedSupportWithoutRewritingTheStamp(t *testing.T) {
	s := open(t)
	e, err := s.AddEvidence(model.Evidence{URI: "in_1/proof.txt"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.AddFact(model.Fact{Kind: "vuln", Title: "support", EvidenceIDs: []string{e.ID}})
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.AddFact(model.Fact{Kind: "note", Title: "unrelated", EvidenceIDs: []string{e.ID}})
	if err != nil {
		t.Fatal(err)
	}
	g, err := s.AddGoal(model.Goal{Statement: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := s.PinGoal(g.ID, e.ID, "the observed evidence satisfies the goal", []string{f.ID})
	if err != nil {
		t.Fatal(err)
	}
	if pinned.ReceiptStale || len(pinned.ReceiptChanged) != 0 {
		t.Fatalf("a fresh stamp is already stale: %+v", pinned)
	}

	// Changing a fact the receipt did not cite must not make the stamp suspect.
	if _, err := s.UpdateFact(other.ID, func(f *model.Fact) { f.Summary = "changed unrelated note" }); err != nil {
		t.Fatal(err)
	}
	if got := s.Goals()[0]; got.ReceiptStale {
		t.Fatalf("unrelated fact change marked the receipt stale: %+v", got)
	}

	// Correcting the cited claim is exactly the case the operator must see.
	if _, err := s.UpdateFact(f.ID, func(f *model.Fact) { f.Summary = "corrected claim" }); err != nil {
		t.Fatal(err)
	}
	got := s.Goals()[0]
	if !got.ReceiptStale || len(got.ReceiptChanged) != 1 || got.ReceiptChanged[0] != f.ID {
		t.Fatalf("changed support not flagged: %+v", got)
	}
	// The historical receipt itself is preserved: met, evidence, note, versions.
	if !got.Met || got.EvidenceID != pinned.EvidenceID || got.Note != pinned.Note ||
		got.FromClaimVersions[f.ID] != pinned.FromClaimVersions[f.ID] || got.CompletedAt == nil {
		t.Fatalf("stamp was rewritten: before=%+v after=%+v", pinned, got)
	}
	if saved := storedGoal(t, s, g.ID); saved.ReceiptStale || len(saved.ReceiptChanged) != 0 {
		t.Fatal("computed stale flag leaked back into the stored receipt")
	}

	// Graph/board read the clone, so the same observation must travel there.
	snap := s.Snapshot()
	var snapGoal *model.Goal
	for i := range snap.Goals {
		if snap.Goals[i].ID == g.ID {
			snapGoal = &snap.Goals[i]
		}
	}
	if snapGoal == nil || !snapGoal.ReceiptStale || snapGoal.FromClaimVersions[f.ID] != pinned.FromClaimVersions[f.ID] {
		t.Fatalf("snapshot lost the stale signal or the historical versions: %+v", snapGoal)
	}

	// Reopening is the explicit human correction: it clears the receipt and the
	// stale observation with it.
	if _, err := s.ReopenGoal(g.ID); err != nil {
		t.Fatal(err)
	}
	if reopened := s.Goals()[0]; reopened.ReceiptStale || reopened.Met || reopened.ReceiptChanged != nil {
		t.Fatalf("reopened goal kept stale state: %+v", reopened)
	}
}

// Computed flags are read-side only: a caller cannot inject them through
// AddGoal and have the ledger store them as if they were evidence.
func TestAddGoalIgnoresComputedReceiptFlags(t *testing.T) {
	s := open(t)
	g, err := s.AddGoal(model.Goal{Statement: "goal", ReceiptStale: true, ReceiptChanged: []string{"f_x"}})
	if err != nil {
		t.Fatal(err)
	}
	if g.ReceiptStale || len(g.ReceiptChanged) != 0 || storedGoal(t, s, g.ID).ReceiptStale {
		t.Fatalf("computed flags were stored: %+v", g)
	}
}

// storedGoal reads the persisted row without the read-side recomputation.
func storedGoal(t *testing.T, s *Store, id string) model.Goal {
	t.Helper()
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, g := range s.db.Goals {
		if g.ID == id {
			return g
		}
	}
	t.Fatalf("goal %s not stored", id)
	return model.Goal{}
}
