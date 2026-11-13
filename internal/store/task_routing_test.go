package store

import (
	"testing"

	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/model"
)

func TestTaskResultBackpressureDoesNotBlockOtherRecipientsAcrossBatchesAndRestart(t *testing.T) {
	s, blocked, child := attentionTasks(t)
	other := mustIntent(t, s, model.Intent{Title: "other coordinator"})
	otherChild := mustIntent(t, s, model.Intent{Title: "other result", CreatedBy: collab.ParticipantID(other.ID)})
	fillRecipientQueue(t, s, collab.ParticipantID(blocked.ID), MaxQueuedMessages)
	if _, err := s.UpdateIntent(child.ID, func(i *model.Intent) { i.Status = model.StatusDone }); err != nil {
		t.Fatal(err)
	}
	// The available recipient is beyond one journal batch, so merely skipping
	// the blocked event while keeping the old shared cursor is insufficient.
	s.mu.Lock()
	for range 520 {
		s.db.Facts = append(s.db.Facts, model.Fact{ID: NewID("f"), Title: "unrelated"})
	}
	err := s.flush("fact")
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateIntent(otherChild.ID, func(i *model.Intent) { i.Status = model.StatusDone }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.sqlite.db.Exec("CREATE TRIGGER fail_deferred_route BEFORE UPDATE ON meta BEGIN SELECT RAISE(ABORT,'fixture deferred route failure'); END"); err != nil {
		t.Fatal(err)
	}
	if err := s.RouteTaskResults(); err == nil {
		t.Fatal("failed transaction reported success")
	}
	if s.db.Memory.TaskRouteThrough != 0 || len(s.db.Memory.TaskRoutePending) != 0 || len(s.PendingCoordinationNotices()) != 0 {
		t.Fatal("failed transaction advanced routing or leaked a notification")
	}
	if _, err := s.sqlite.db.Exec("DROP TRIGGER fail_deferred_route"); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := s.RouteTaskResults(); err != nil {
			t.Fatal(err)
		}
	}
	msgs := s.PendingCoordinationNotices()
	if len(msgs) != 1 || msgs[0].NoticeIntentID != otherChild.ID || msgs[0].ToParticipantID != collab.ParticipantID(other.ID) {
		t.Fatalf("full recipient blocked unrelated result: %+v", msgs)
	}
	path := s.Path()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// Free the blocked queue after reopening. Its deferred result must remain
	// recoverable even though the journal scan has passed it.
	s.mu.Lock()
	for i := range s.db.Messages {
		if s.db.Messages[i].ToParticipantID == collab.ParticipantID(blocked.ID) {
			s.db.Messages[i].Status = model.MessageAccepted
		}
	}
	err = s.flushNoRev("message")
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := s.RouteTaskResults(); err != nil {
			t.Fatal(err)
		}
	}
	msgs = s.PendingCoordinationNotices()
	if len(msgs) != 2 {
		t.Fatalf("deferred result lost or duplicated after restart: %+v", msgs)
	}
}
