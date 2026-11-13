package api

// Tianwu does not own window management or compaction: those are the harness's.
// These tests pin the part Tianwu must get right — reusing the sizes the harness
// reported, including the post-compaction size, instead of a stale
// pre-compaction peak.

import (
	"testing"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

func TestContinuationBudgetUsesHarnessPostCompactionSize(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/ledger.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: st}
	in, err := st.CreateIntent(model.Intent{Title: "session"})
	if err != nil {
		t.Fatal(err)
	}
	compacted, err := st.AddRun(model.Run{
		IntentID: in.ID, Purpose: "",
		ContextTokens: 90000, FinalContextTokens: 12000, Compacted: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.continuationHistoryTokens(in.ID, compacted.ID); got != 12000 {
		t.Fatalf("continuation history = %d, want the harness post-compaction 12000", got)
	}

	// An older run that predates the field (or did not report a final size) still
	// falls back to the peak the harness reported.
	legacy, err := st.AddRun(model.Run{IntentID: in.ID, ContextTokens: 7000})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.continuationHistoryTokens(in.ID, legacy.ID); got != 7000 {
		t.Fatalf("legacy continuation history = %d, want the peak 7000", got)
	}
	// No harness report is unknown, not zero-sized proof.
	empty, err := st.AddRun(model.Run{IntentID: in.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.continuationHistoryTokens(in.ID, empty.ID); got != 0 {
		t.Fatalf("unknown history = %d, want 0 (unknown)", got)
	}
}

func TestCommanderBudgetUsesHarnessPostCompactionSize(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/ledger.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: st}
	if _, err := st.AddRun(model.Run{
		Purpose: "commander", ContextTokens: 80000, FinalContextTokens: 9000, Compacted: true,
	}); err != nil {
		t.Fatal(err)
	}
	if got := s.commanderHistoryTokens(""); got != 9000 {
		t.Fatalf("commander history = %d, want the harness post-compaction 9000", got)
	}
}
