package store

import (
	"errors"
	"testing"

	"github.com/kksty/tianwu/internal/model"
)

func TestIntentCreationSnapshotsPremiseClaims(t *testing.T) {
	s := open(t)
	producer, _ := s.CreateIntent(model.Intent{Title: "producer"})
	premise, err := s.AddFact(model.Fact{IntentID: producer.ID, Kind: "vuln", Title: "premise"})
	if err != nil {
		t.Fatal(err)
	}
	in, err := s.CreateIntent(model.Intent{Title: "consumer", FromFactIDs: []string{premise.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if got := in.FromFactClaims[premise.ID]; got != premise.ClaimVersion {
		t.Fatalf("from_fact_claims[%s] = %d, want %d", premise.ID, got, premise.ClaimVersion)
	}
	// A claim revision is visible as "changed" without touching the snapshot.
	updated, err := s.UpdateFact(premise.ID, func(f *model.Fact) { f.Summary = "revised" })
	if err != nil {
		t.Fatal(err)
	}
	kept, _ := s.GetIntent(in.ID)
	if kept.FromFactClaims[premise.ID] != premise.ClaimVersion {
		t.Fatal("the creation snapshot moved with the fact")
	}
	facts := map[string]model.Fact{premise.ID: updated}
	if changed := PremiseClaimsChanged(kept, facts); len(changed) != 1 || changed[0] != premise.ID {
		t.Fatalf("premise change = %v", changed)
	}
	// Updating the premise list re-snapshots at the new versions.
	other, _ := s.AddFact(model.Fact{IntentID: producer.ID, Kind: "host", Title: "10.0.0.5"})
	relisted, err := s.UpdateIntent(in.ID, func(i *model.Intent) { i.FromFactIDs = []string{other.ID} })
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := relisted.FromFactClaims[premise.ID]; ok {
		t.Fatal("stale premise claim snapshot survived a premise edit")
	}
	if relisted.FromFactClaims[other.ID] != other.ClaimVersion {
		t.Fatalf("new premise snapshot = %v", relisted.FromFactClaims)
	}
}

func TestUpdateIntentRejectsAStructureCycle(t *testing.T) {
	s := open(t)
	producer, _ := s.CreateIntent(model.Intent{Title: "producer"})
	fact, err := s.AddFact(model.Fact{IntentID: producer.ID, Kind: "vuln", Title: "produced"})
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := s.CreateIntent(model.Intent{Title: "consumer", FromFactIDs: []string{fact.ID}})
	if err != nil {
		t.Fatal(err)
	}
	// producer -> fact -> consumer -> producer would be a cycle: the producer
	// cannot become a child of the intent that consumes its own product.
	if _, err := s.UpdateIntent(producer.ID, func(i *model.Intent) { i.ParentID = consumer.ID }); !errors.Is(err, ErrIntentGraphCycle) {
		t.Fatalf("cycle error = %v, want ErrIntentGraphCycle", err)
	}
	kept, _ := s.GetIntent(producer.ID)
	if kept.ParentID != "" {
		t.Fatalf("rejected write changed the parent: %q", kept.ParentID)
	}
	// A self-premise is the shorter cycle and is refused the same way.
	if _, err := s.UpdateIntent(producer.ID, func(i *model.Intent) { i.FromFactIDs = []string{fact.ID} }); !errors.Is(err, ErrIntentGraphCycle) {
		t.Fatalf("self-cycle error = %v, want ErrIntentGraphCycle", err)
	}
	kept, _ = s.GetIntent(producer.ID)
	if len(kept.FromFactIDs) != 0 {
		t.Fatalf("rejected write changed premises: %v", kept.FromFactIDs)
	}
}

// A structural edit that does not close a cycle is accepted and re-snapshots the
// premise claims; unrelated status/attempt writes pay no graph check.
func TestUpdateIntentAcceptsValidStructureEdits(t *testing.T) {
	s := open(t)
	producer, _ := s.CreateIntent(model.Intent{Title: "producer"})
	premise, _ := s.AddFact(model.Fact{IntentID: producer.ID, Kind: "host", Title: "10.0.0.5"})
	consumer, _ := s.CreateIntent(model.Intent{Title: "consumer"})
	updated, err := s.UpdateIntent(consumer.ID, func(i *model.Intent) { i.FromFactIDs = []string{premise.ID} })
	if err != nil {
		t.Fatal(err)
	}
	if updated.FromFactClaims[premise.ID] != premise.ClaimVersion {
		t.Fatalf("premise snapshot after edit = %v", updated.FromFactClaims)
	}
	if _, err := s.UpdateIntent(consumer.ID, func(i *model.Intent) { i.Hold = true }); err != nil {
		t.Fatal(err)
	}
}
