package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/model"
)

func testMessage(from, to, key, text string) model.Message {
	return model.Message{
		FromParticipantID: from, FromRunID: "r_1", FromGeneration: 1,
		ToParticipantID: to, ToGeneration: 1, Kind: model.MessageQuestion,
		Text: text, IdempotencyKey: key,
	}
}

func TestAddMessageAssignsIDsAndSequence(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	first, created, err := s.AddMessage(testMessage("pt_a", "pt_b", "k1", "hello"))
	if err != nil || !created {
		t.Fatalf("add: %v created=%v", err, created)
	}
	if first.ID == "" || first.Seq != 1 || first.Status != model.MessageQueued || first.CreatedAt.IsZero() {
		t.Fatalf("first = %+v", first)
	}
	second, created, err := s.AddMessage(testMessage("pt_b", "pt_a", "k2", "hi"))
	if err != nil || !created || second.Seq != 2 {
		t.Fatalf("second: %+v %v %v", second, created, err)
	}

	// Each participant sees what it sent and what was addressed to it.
	if got := s.MessagesFor("pt_a", 0, 10); len(got) != 2 {
		t.Fatalf("pt_a messages = %+v", got)
	}
	if got := s.MessagesFor("pt_a", 1, 10); len(got) != 1 || got[0].Seq != 2 {
		t.Fatalf("after cursor = %+v", got)
	}
	if got := s.MessagesFor("pt_c", 0, 10); len(got) != 0 {
		t.Fatalf("stranger sees %+v", got)
	}
	if got := s.Messages(0); len(got) != 2 {
		t.Fatalf("console messages = %+v", got)
	}
}

func TestAddMessageIsIdempotentPerSender(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	first, created, err := s.AddMessage(testMessage("pt_a", "pt_b", "same", "hello"))
	if err != nil || !created {
		t.Fatal(err)
	}
	again, created, err := s.AddMessage(testMessage("pt_a", "pt_b", "same", "hello"))
	if err != nil || created {
		t.Fatalf("retry: %v created=%v", err, created)
	}
	if again.ID != first.ID || again.Seq != first.Seq {
		t.Fatalf("retry returned a different row: %+v vs %+v", again, first)
	}
	// The same key with different content is a caller bug, not a retry.
	if _, _, err := s.AddMessage(testMessage("pt_a", "pt_b", "same", "different")); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflict err = %v", err)
	}
	// Another sender may reuse the key: idempotency is per sender.
	if _, created, err := s.AddMessage(testMessage("pt_c", "pt_b", "same", "hello")); err != nil || !created {
		t.Fatalf("other sender: %v created=%v", err, created)
	}
	// An empty key never dedupes.
	if _, created, err := s.AddMessage(testMessage("pt_a", "pt_b", "", "hello")); err != nil || !created {
		t.Fatalf("empty key: %v created=%v", err, created)
	}
}

func TestMessageRetryCannotChangeInterrupt(t *testing.T) {
	s := open(t)
	request := testMessage("pt_a", "pt_b", "same", "hello")
	first, _, err := s.AddMessage(request)
	if err != nil {
		t.Fatal(err)
	}
	request.Interrupt = true
	if _, _, err := s.AddMessage(request); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed interrupt must conflict: %v", err)
	}
	got, _ := s.MessageByID(first.ID)
	if got.Interrupt || len(s.Messages(10)) != 1 {
		t.Fatal("conflicting retry changed the original message")
	}
}

func TestFailedRunRequeuesMessagesDeliveredAfterTurnStarted(t *testing.T) {
	s := open(t)
	request := testMessage("pt_a", "pt_b", "", "queued before the turn")
	m, _, err := s.AddMessage(request)
	if err != nil {
		t.Fatal(err)
	}
	boundary := time.Now()
	if _, claimed, err := s.BeginMessageDelivery(m.ID); err != nil || !claimed {
		t.Fatalf("claim: %v, %v", claimed, err)
	}
	if _, err := s.FinishMessageDelivery(m.ID, "ioa_1", "r_1", "ioa", ""); err != nil {
		t.Fatal(err)
	}
	// A restart must preserve the delivery boundary, including for an old
	// queued message or a redelivery from an earlier failed run.
	reopened, err := Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if n, err := reopened.RedeliverPossiblyUnread("pt_b", "r_1", boundary); err != nil || n != 1 {
		t.Fatalf("late delivery was lost: requeued=%d, err=%v", n, err)
	}
}

func TestMessageUpdatesPersistAcrossOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	m, _, err := s.AddMessage(testMessage("pt_a", "pt_b", "k", "hello"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateMessage(m.ID, func(msg *model.Message) {
		msg.Status = model.MessageAccepted
		msg.DeliveryID = "ioa_1"
	}); err != nil {
		t.Fatal(err)
	}
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := again.MessageByID(m.ID)
	if !ok || got.Status != model.MessageAccepted || got.DeliveryID != "ioa_1" {
		t.Fatalf("reloaded = %+v ok=%v", got, ok)
	}
	if got, ok := again.MessageByID("m_missing"); ok || got.ID != "" {
		t.Fatalf("missing message = %+v", got)
	}
}

// Chat is durable project state, but it is not a fact-graph change: waking the
// commander on every line would burn a model round for nothing.
func TestMessagesDoNotBumpDeciderRevision(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	before := s.Revision()
	if _, _, err := s.AddMessage(testMessage("pt_a", "pt_b", "k", "hello")); err != nil {
		t.Fatal(err)
	}
	if s.Revision() != before {
		t.Fatalf("revision moved on a message: %d -> %d", before, s.Revision())
	}
}

// ---- redelivery after a failed run (retry-with-memory) ----

// delivered makes one accepted (delivered) row with fixed provenance: the tests
// need to control creation/delivery time and run identity.
func delivered(t *testing.T, s *Store, to, runID, path string, createdAt time.Time) model.Message {
	t.Helper()
	m, created, err := s.AddMessage(testMessage("pt_src", to, "", "line"))
	if err != nil || !created {
		t.Fatalf("add: %v created=%v", err, created)
	}
	got, err := s.UpdateMessage(m.ID, func(x *model.Message) {
		x.Status = model.MessageAccepted
		x.DeliveredRunID = runID
		x.DeliveryPath = path
		x.CreatedAt = createdAt
	})
	if err != nil {
		t.Fatal(err)
	}
	// The fixture models immediate delivery at createdAt. Real delivery paths
	// stamp UpdatedAt when acceptance is persisted, independently of creation.
	s.mu.Lock()
	for i := range s.db.Messages {
		if s.db.Messages[i].ID == m.ID {
			s.db.Messages[i].UpdatedAt = createdAt
		}
	}
	err = s.flushNoRev()
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	got.UpdatedAt = createdAt
	return got
}

// confirm marks a row confirmed without changing what the redelivery rules see.
func confirm(t *testing.T, s *Store, id string) {
	t.Helper()
	now := time.Now()
	if _, err := s.UpdateMessage(id, func(x *model.Message) {
		x.Status = model.MessageConfirmed
		x.AckedAt = &now
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRedeliverPossiblyUnreadUsesLastTurnBoundary(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	boundary := now.Add(-30 * time.Second)
	drained := delivered(t, s, "pt_x", "r_1", "prompt", now.Add(-2*time.Minute)) // before turn start: may be in transcript
	late := delivered(t, s, "pt_x", "r_1", "ioa", now.Add(-10*time.Second))      // after turn start: may be unread
	acked := delivered(t, s, "pt_x", "r_1", "ioa", now)
	confirm(t, s, acked.ID)
	otherRun := delivered(t, s, "pt_x", "r_2", "ioa", now)
	// A queued row was never delivered and must not be double-counted.
	queued, _, err := s.AddMessage(testMessage("pt_src", "pt_x", "", "pending"))
	if err != nil {
		t.Fatal(err)
	}

	n, err := s.RedeliverPossiblyUnread("pt_x", "r_1", boundary)
	if err != nil || n != 1 {
		t.Fatalf("redelivered = %d, %v; want 1", n, err)
	}
	check := func(id string, wantStatus string, wantRedeliveries int) {
		t.Helper()
		m, ok := s.MessageByID(id)
		if !ok || m.Status != wantStatus || m.Redeliveries != wantRedeliveries {
			t.Fatalf("message %s = %+v", id, m)
		}
	}
	check(late.ID, model.MessageQueued, 1)
	check(drained.ID, model.MessageAccepted, 0)
	check(acked.ID, model.MessageConfirmed, 0)
	check(otherRun.ID, model.MessageAccepted, 0)
	check(queued.ID, model.MessageQueued, 0)
	if m, _ := s.MessageByID(late.ID); m.ToGeneration != 0 {
		t.Fatalf("requeued message kept the old generation: %+v", m)
	}

	// A run that never started a turn (zero boundary) could not have read
	// anything it carried: every accepted row of that run is requeued.
	if n, err := s.RedeliverPossiblyUnread("pt_x", "r_1", time.Time{}); err != nil || n != 1 {
		t.Fatalf("zero boundary redelivered = %d, %v; want 1 (the drained row)", n, err)
	}
	if m, _ := s.MessageByID(drained.ID); m.Status != model.MessageQueued || m.Redeliveries != 1 {
		t.Fatalf("drained row after zero boundary = %+v", m)
	}
	// A second pass finds nothing accepted: redelivery is idempotent per row.
	if n, _ := s.RedeliverPossiblyUnread("pt_x", "r_1", time.Time{}); n != 0 {
		t.Fatalf("second pass redelivered %d rows", n)
	}
}

func TestRedeliverAllUnconfirmedForFreshSession(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	old := delivered(t, s, "pt_x", "r_old", "prompt", now.Add(-time.Hour))
	mid := delivered(t, s, "pt_x", "r_mid", "ioa", now)
	acked := delivered(t, s, "pt_x", "r_mid", "ioa", now)
	confirm(t, s, acked.ID)
	other := delivered(t, s, "pt_y", "r_mid", "ioa", now)

	n, err := s.RedeliverAllUnconfirmed("pt_x")
	if err != nil || n != 2 {
		t.Fatalf("redelivered = %d, %v; want 2", n, err)
	}
	for _, id := range []string{old.ID, mid.ID} {
		m, _ := s.MessageByID(id)
		if m.Status != model.MessageQueued || m.Redeliveries != 1 || m.ToGeneration != 0 {
			t.Fatalf("message %s = %+v", id, m)
		}
	}
	if m, _ := s.MessageByID(acked.ID); m.Status != model.MessageConfirmed {
		t.Fatalf("confirmed row touched: %+v", m)
	}
	if m, _ := s.MessageByID(other.ID); m.Status != model.MessageAccepted {
		t.Fatalf("another participant's row touched: %+v", m)
	}
	if counts := s.RedeliveryCounts("pt_x"); counts.Resume != 2 || counts.Fresh != 2 {
		t.Fatalf("counts = %+v", counts)
	}
}

func TestRedeliveryCountsSeparatesQueuedFromAccepted(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	// One row the failed run already requeued (resume will carry it) and one
	// untouched accepted row (only a fresh session needs it).
	requeued := delivered(t, s, "pt_x", "r_1", "ioa", now)
	if _, err := s.UpdateMessage(requeued.ID, func(x *model.Message) {
		x.Status = model.MessageQueued
		x.Redeliveries = 1
	}); err != nil {
		t.Fatal(err)
	}
	delivered(t, s, "pt_x", "r_1", "prompt", now)

	counts := s.RedeliveryCounts("pt_x")
	if counts.Resume != 1 || counts.Fresh != 2 {
		t.Fatalf("counts = %+v", counts)
	}
}

func TestSweepQueuedJudgesRedeliveredRowsByQueueTime(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	// A message first sent 48h ago but requeued just now must survive the TTL:
	// otherwise redelivery silently deletes the message it was meant to save.
	redelivered := delivered(t, s, "pt_x", "r_1", "ioa", now.Add(-48*time.Hour))
	if _, err := s.UpdateMessage(redelivered.ID, func(x *model.Message) {
		x.Status = model.MessageQueued
		x.Redeliveries = 1
	}); err != nil {
		t.Fatal(err)
	}
	// A normally queued message is as old as it was sent and is swept.
	stale, _, err := s.AddMessage(testMessage("pt_src", "pt_x", "", "stale"))
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	for i := range s.db.Messages {
		if s.db.Messages[i].ID == stale.ID {
			s.db.Messages[i].CreatedAt = now.Add(-48 * time.Hour)
			s.db.Messages[i].UpdatedAt = now.Add(-48 * time.Hour)
		}
	}
	s.mu.Unlock()

	removed, err := s.SweepQueued(now, 24*time.Hour, 50)
	if err != nil || removed != 1 {
		t.Fatalf("swept = %d, %v; want 1", removed, err)
	}
	if _, ok := s.MessageByID(redelivered.ID); !ok {
		t.Fatal("a redelivered message was swept by its original CreatedAt")
	}
	if _, ok := s.MessageByID(stale.ID); ok {
		t.Fatal("a stale queued message survived")
	}
}

func TestSweepQueuedCapKeepsNewestQueueTime(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	var rows []model.Message
	for i := 0; i < 3; i++ {
		m, _, err := s.AddMessage(testMessage("pt_src", "pt_x", "", "m"+string(rune('a'+i))))
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, m)
	}
	// Oldest first: the cap must drop the oldest queue time, not the oldest Seq.
	s.mu.Lock()
	for i := range s.db.Messages {
		for j, row := range rows {
			if s.db.Messages[i].ID == row.ID {
				s.db.Messages[i].UpdatedAt = now.Add(time.Duration(j-3) * time.Minute)
			}
		}
	}
	s.mu.Unlock()

	removed, err := s.SweepQueued(now, 0, 2)
	if err != nil || removed != 1 {
		t.Fatalf("swept = %d, %v; want 1", removed, err)
	}
	if _, ok := s.MessageByID(rows[0].ID); ok {
		t.Fatal("the oldest queue time survived the per-recipient cap")
	}
	for _, row := range rows[1:] {
		if _, ok := s.MessageByID(row.ID); !ok {
			t.Fatalf("row %s was swept, want kept", row.ID)
		}
	}
}
