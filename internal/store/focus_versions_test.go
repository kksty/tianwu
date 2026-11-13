package store

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/model"
)

// A page cut at version v must pair each fact body with the record version that
// body had at v, even after later writes moved the live row forward.
func TestFactVersionsAtKeepsTheOldSnapshotVersion(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first, err := s.AddFact(model.Fact{Kind: "note", Title: "first"})
	if err != nil {
		t.Fatal(err)
	}
	_, snapshot := s.ReadView()
	if _, err := s.UpdateFact(first.ID, func(f *model.Fact) { f.Doc = "changed" }); err != nil {
		t.Fatal(err)
	}
	second, err := s.AddFact(model.Fact{Kind: "note", Title: "second"})
	if err != nil {
		t.Fatal(err)
	}
	old, err := s.FactVersionsAt(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if got := old[first.ID]; got != 1 {
		t.Fatalf("old snapshot version of %s = %d, want 1", first.ID, got)
	}
	if _, ok := old[second.ID]; ok {
		t.Fatalf("fact added after the snapshot is visible at it: %s", second.ID)
	}
	_, current := s.ReadView()
	now, err := s.FactVersionsAt(current)
	if err != nil {
		t.Fatal(err)
	}
	if got := now[first.ID]; got != 2 {
		t.Fatalf("current version of %s = %d, want 2", first.ID, got)
	}
	if got := now[second.ID]; got != 1 {
		t.Fatalf("current version of %s = %d, want 1", second.ID, got)
	}
}

func TestFactVersionsAtRejectsForeignOrFutureSnapshots(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.AddFact(model.Fact{Kind: "note", Title: "row"}); err != nil {
		t.Fatal(err)
	}
	_, current := s.ReadView()
	if _, err := s.FactVersionsAt(ReadVersion{Epoch: "ledger_other", Seq: current.Seq}); err == nil ||
		!strings.Contains(err.Error(), "cursor_expired") {
		t.Fatalf("foreign epoch error = %v, want cursor_expired", err)
	}
	if _, err := s.FactVersionsAt(ReadVersion{Epoch: current.Epoch, Seq: current.Seq + 5}); err == nil ||
		!strings.Contains(err.Error(), "cursor_expired") {
		t.Fatalf("future watermark error = %v, want cursor_expired", err)
	}
	// A snapshot that does not move any fact (here: none at all) still reads the
	// same versions; it is a valid cursor, not an error.
	versions, err := s.FactVersionsAt(current)
	if err != nil || len(versions) != 1 {
		t.Fatalf("current versions = %v, %v", versions, err)
	}
}
