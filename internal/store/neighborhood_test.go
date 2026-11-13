package store

import (
	"path/filepath"
	"testing"

	"github.com/kksty/tianwu/internal/model"
)

func neighborhoodLedger(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func neighborhoodFact(t *testing.T, s *Store, f model.Fact) model.Fact {
	t.Helper()
	got, err := s.AddFact(f)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// pinNeighborhoodFixture records one intent whose premise, reference and pin
// lists come from three different facts, plus one fact recorded in both the
// context and pin lists.
type pinNeighborhoodFixture struct {
	s                *Store
	premise          model.Fact
	reference        model.Fact
	pinned           model.Fact
	both             model.Fact
	target           model.Intent
	producingIntent  model.Intent
}

func newPinNeighborhoodFixture(t *testing.T) *pinNeighborhoodFixture {
	t.Helper()
	s := neighborhoodLedger(t)
	producer, err := s.CreateIntent(model.Intent{Title: "producer"})
	if err != nil {
		t.Fatal(err)
	}
	premise := neighborhoodFact(t, s, model.Fact{IntentID: producer.ID, Kind: "note", Title: "premise"})
	reference := neighborhoodFact(t, s, model.Fact{IntentID: producer.ID, Kind: "note", Title: "reference"})
	pinned := neighborhoodFact(t, s, model.Fact{IntentID: producer.ID, Kind: "note", Title: "pinned"})
	both := neighborhoodFact(t, s, model.Fact{IntentID: producer.ID, Kind: "note", Title: "both"})
	target, err := s.CreateIntent(model.Intent{
		Title:          "selects",
		FromFactIDs:    []string{premise.ID},
		ContextFactIDs: []string{reference.ID, both.ID},
		PinnedFactIDs:  []string{pinned.ID, both.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &pinNeighborhoodFixture{
		s: s, premise: premise, reference: reference, pinned: pinned, both: both,
		target: target, producingIntent: producer,
	}
}

func neighborhoodEdges(db DB) map[string]int {
	out := map[string]int{}
	for _, edge := range db.ReadEdges {
		out[edge.Source+"\x00"+edge.Target+"\x00"+edge.Kind]++
	}
	return out
}

func neighborhoodIntentLoaded(db DB, id string) bool {
	for _, in := range db.Intents {
		if in.ID == id {
			return true
		}
	}
	return false
}

func neighborhoodFactLoaded(db DB, id string) bool {
	for _, f := range db.Facts {
		if f.ID == id {
			return true
		}
	}
	return false
}

// A pinned fact must lead back to the intent that pinned it: the reverse of the
// reference path is what a root-scoped graph read (root=fact:<id>) relies on.
func TestReadNeighborhoodTraversesPinRelation(t *testing.T) {
	fx := newPinNeighborhoodFixture(t)
	db, used, err := fx.s.ReadNeighborhood(fx.s.Watermark(), []string{"fact:" + fx.pinned.ID}, "descendants", 0)
	if err != nil || !used {
		t.Fatalf("neighborhood read: used=%v err=%v", used, err)
	}
	if !neighborhoodIntentLoaded(db, fx.target.ID) {
		t.Fatal("pin edge did not lead back to the pinning intent")
	}
	key := "fact:" + fx.pinned.ID + "\x00intent:" + fx.target.ID + "\x00pin"
	if got := neighborhoodEdges(db)[key]; got != 1 {
		t.Fatalf("pin edge count from fact root = %d, want 1", got)
	}
}

// Starting from the intent, a pin is an incoming recorded edge like a reference:
// direction "both" loads the pinned fact itself, not just an edge to a ghost.
func TestReadNeighborhoodLoadsPinnedFactFromIntentRoot(t *testing.T) {
	fx := newPinNeighborhoodFixture(t)
	db, used, err := fx.s.ReadNeighborhood(fx.s.Watermark(), []string{"intent:" + fx.target.ID}, "both", 0)
	if err != nil || !used {
		t.Fatalf("neighborhood read: used=%v err=%v", used, err)
	}
	for _, fact := range []model.Fact{fx.pinned, fx.reference, fx.premise, fx.both} {
		if !neighborhoodFactLoaded(db, fact.ID) {
			t.Fatalf("fact %s not reached from the intent root", fact.ID)
		}
	}
	edges := neighborhoodEdges(db)
	for _, kind := range []string{"pin", "reference", "premise"} {
		key := "fact:" + factForKind(fx, kind) + "\x00intent:" + fx.target.ID + "\x00" + kind
		if got := edges[key]; got != 1 {
			t.Fatalf("%s edge count = %d, want 1", kind, got)
		}
	}
}

// One fact recorded in both ContextFactIDs and PinnedFactIDs keeps both edges:
// they are different relation kinds, not one field shadowing the other.
func TestReadNeighborhoodKeepsPinAndReferenceOnTheSameFact(t *testing.T) {
	fx := newPinNeighborhoodFixture(t)
	db, used, err := fx.s.ReadNeighborhood(fx.s.Watermark(), []string{"fact:" + fx.both.ID}, "descendants", 0)
	if err != nil || !used {
		t.Fatalf("neighborhood read: used=%v err=%v", used, err)
	}
	if !neighborhoodIntentLoaded(db, fx.target.ID) {
		t.Fatal("fact recorded as both pin and reference did not reach its intent")
	}
	edges := neighborhoodEdges(db)
	base := "fact:" + fx.both.ID + "\x00intent:" + fx.target.ID + "\x00"
	if got := edges[base+"pin"]; got != 1 {
		t.Fatalf("pin edge on a doubly recorded fact = %d, want 1", got)
	}
	if got := edges[base+"reference"]; got != 1 {
		t.Fatalf("reference edge on a doubly recorded fact = %d, want 1", got)
	}
}

// Duplicate roots must not duplicate derived edges.
func TestReadNeighborhoodDeduplicatesEdges(t *testing.T) {
	fx := newPinNeighborhoodFixture(t)
	roots := []string{"fact:" + fx.pinned.ID, "fact:" + fx.pinned.ID}
	db, used, err := fx.s.ReadNeighborhood(fx.s.Watermark(), roots, "descendants", 0)
	if err != nil || !used {
		t.Fatalf("neighborhood read: used=%v err=%v", used, err)
	}
	key := "fact:" + fx.pinned.ID + "\x00intent:" + fx.target.ID + "\x00pin"
	if got := neighborhoodEdges(db)[key]; got != 1 {
		t.Fatalf("duplicate root produced %d pin edges, want 1", got)
	}
}

// The relations table is an incremental derived index: a ledger created before
// pin support has no pin rows until the owning intent is written again (or an
// explicit `ledger -reindex` rebuild runs). The startup path deliberately does
// not scan history for this, so the test pins the documented behaviour instead
// of adding a silent migration.
func TestPinRelationIndexIsIncrementalNotMigrated(t *testing.T) {
	fx := newPinNeighborhoodFixture(t)
	s := fx.s
	read := func() DB {
		t.Helper()
		db, used, err := s.ReadNeighborhood(s.Watermark(), []string{"fact:" + fx.pinned.ID}, "descendants", 0)
		if err != nil || !used {
			t.Fatalf("neighborhood read: used=%v err=%v", used, err)
		}
		return db
	}
	dropPins := func() {
		t.Helper()
		if _, err := s.sqlite.db.Exec("DELETE FROM relations WHERE relation='pin'"); err != nil {
			t.Fatal(err)
		}
	}
	dropPins()
	dropped := read()
	if neighborhoodIntentLoaded(dropped, fx.target.ID) {
		t.Fatal("fixture still had a pin row after simulating an old index")
	}
	dropPins()

	// A write of the owning intent re-links all of its derived relations, which
	// is the incremental repair path for that row.
	if _, err := s.UpdateIntent(fx.target.ID, func(in *model.Intent) { in.Description = "touched after pin index support" }); err != nil {
		t.Fatal(err)
	}
	relinked := read()
	if !neighborhoodIntentLoaded(relinked, fx.target.ID) {
		t.Fatal("intent write did not re-link the pin relation")
	}

	// Rows nobody has written since the upgrade are repaired only by the
	// explicit maintenance command (`ledger --source <path> --reindex`).
	dropPins()
	if neighborhoodIntentLoaded(read(), fx.target.ID) {
		t.Fatal("deleted pin rows were resurrected without a rebuild")
	}
	if err := s.RebuildIndexes(); err != nil {
		t.Fatal(err)
	}
	if !neighborhoodIntentLoaded(read(), fx.target.ID) {
		t.Fatal("explicit RebuildIndexes did not restore the pin relation")
	}
}

func factForKind(fx *pinNeighborhoodFixture, kind string) string {
	switch kind {
	case "premise":
		return fx.premise.ID
	case "reference":
		return fx.reference.ID
	default:
		return fx.pinned.ID
	}
}
