package retrieval

import (
	"path/filepath"
	"testing"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

func TestInstructionPagesRetainSnapshotAcrossConcurrentRevocation(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var last model.Instruction
	for i := 0; i < 3; i++ {
		last, _, err = s.CreateInstruction(model.Instruction{CreatedBy: "operator", Text: "original", IdempotencyKey: store.NewID("key")})
		if err != nil {
			t.Fatal(err)
		}
	}
	page, err := Instructions(s, "", "active", "", 1, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateInstruction(last.ID, 1, "changed", model.InstructionRevoked); err != nil {
		t.Fatal(err)
	}
	seen := len(page.Instructions)
	for page.HasMore {
		page, err = Instructions(s, "", "active", page.NextCursor, 1, true)
		if err != nil {
			t.Fatal(err)
		}
		for _, in := range page.Instructions {
			if in.State != "active" || in.Text != "original" {
				t.Fatal("mixed instruction snapshots")
			}
		}
		seen += len(page.Instructions)
	}
	if seen != 3 {
		t.Fatalf("lost snapshot rows: %d", seen)
	}
}
