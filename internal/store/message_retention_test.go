package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/model"
)

func sweepStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A queued message is the only thing a sender may take back, and taking it back
// removes it: there is nothing to audit, because nobody ever received it.
func TestRetractMessageDeletesOnlyWhileQueued(t *testing.T) {
	s := sweepStore(t)
	msg, _, err := s.AddMessage(testMessage("pt_a", "pt_b", "k1", "还没读的那句"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RetractMessage(msg.ID, "pt_b"); !errors.Is(err, ErrMessageNotSender) {
		t.Fatalf("foreign retract: %v", err)
	}
	if _, err := s.RetractMessage(msg.ID, "pt_a"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.MessageByID(msg.ID); ok {
		t.Fatal("retracted message survived")
	}

	// Delivered: the same call must be refused, and the row must stay.
	delivered, _, err := s.AddMessage(testMessage("pt_a", "pt_b", "k2", "已经送出去了"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateMessage(delivered.ID, func(m *model.Message) { m.Status = model.MessageAccepted }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RetractMessage(delivered.ID, "pt_a"); !errors.Is(err, ErrMessageNotQueued) {
		t.Fatalf("retract delivered: %v", err)
	}
	if _, ok := s.MessageByID(delivered.ID); !ok {
		t.Fatal("delivered message was deleted")
	}
}

// The sweep deletes what can no longer be delivered: old queued rows and rows
// over the per-recipient cap. Delivered rows are the conversation and stay.
func TestSweepQueuedDeletesOldAndOverCapOnly(t *testing.T) {
	s := sweepStore(t)
	now := time.Now()

	old, _, err := s.AddMessage(testMessage("pt_a", "pt_b", "old", "太旧了"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateMessage(old.ID, func(m *model.Message) {
		m.CreatedAt = now.Add(-25 * time.Hour)
	}); err != nil {
		t.Fatal(err)
	}

	// Three queued for one recipient with a cap of 2: the oldest goes.
	var ids []string
	for _, key := range []string{"a", "b", "c"} {
		msg, _, err := s.AddMessage(testMessage("pt_a", "pt_c", key, "排队 "+key))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, msg.ID)
		if _, err := s.UpdateMessage(msg.ID, func(m *model.Message) { m.CreatedAt = now }); err != nil {
			t.Fatal(err)
		}
	}
	delivered, _, err := s.AddMessage(testMessage("pt_a", "pt_c", "done", "已经投递"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateMessage(delivered.ID, func(m *model.Message) {
		m.Status = model.MessageAccepted
		m.CreatedAt = now.Add(-48 * time.Hour)
	}); err != nil {
		t.Fatal(err)
	}

	removed, err := s.SweepQueued(now, 24*time.Hour, 2)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Fatalf("removed = %d, want 2 (one old, one over cap)", removed)
	}
	for _, gone := range []string{old.ID, ids[0]} {
		if _, ok := s.MessageByID(gone); ok {
			t.Fatalf("message %s should be gone", gone)
		}
	}
	if _, ok := s.MessageByID(ids[2]); !ok {
		t.Fatal("newest queued message must stay")
	}
	if _, ok := s.MessageByID(delivered.ID); !ok {
		t.Fatal("delivered message must stay")
	}

	// Nothing left to remove: a second sweep is a no-op.
	if again, err := s.SweepQueued(now, 24*time.Hour, 2); err != nil || again != 0 {
		t.Fatalf("second sweep = %d, %v", again, err)
	}
}

// Sequence numbers keep increasing after a delete, so a reader's `after` cursor
// never re-reads a row and never skips one.
func TestSweepKeepsSequenceMonotonic(t *testing.T) {
	s := sweepStore(t)
	first, _, err := s.AddMessage(testMessage("pt_a", "pt_b", "k1", "一"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RetractMessage(first.ID, "pt_a"); err != nil {
		t.Fatal(err)
	}
	second, _, err := s.AddMessage(testMessage("pt_a", "pt_b", "k2", "二"))
	if err != nil {
		t.Fatal(err)
	}
	if second.Seq <= first.Seq {
		t.Fatalf("seq went backwards: %d then %d", first.Seq, second.Seq)
	}
}
