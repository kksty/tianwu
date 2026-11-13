package store

import (
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/model"
)

// An unacknowledged delivery is a global demand only when its retry backoff has
// elapsed. The schedule (attempts + next retry) is ledger memory, so it survives
// a restart: otherwise every restart would replay the same page immediately.
func TestDeliveryRetryScheduleGatesGlobalAttention(t *testing.T) {
	s := open(t)
	now := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	d, err := s.SaveDelivery("commander", Delivery{ID: "delivery_retry", Expected: s.ReadCheckpoint("commander")})
	if err != nil {
		t.Fatal(err)
	}
	// A delivery that was never shown is due: it exists precisely because no
	// round has handed it to the model yet.
	if a := s.CommanderAttentionAt(now); len(a.Reasons) != 1 || a.Reasons[0].Code != "context_unacknowledged" {
		t.Fatalf("an unshown delivery was not due: %+v", a)
	}
	d, err = s.NoteDeliveryShown("commander", d.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if d.Attempts != 1 || !d.NextRetry.Equal(now.Add(DeliveryRetryBase)) {
		t.Fatalf("attempts=%d next_retry=%s", d.Attempts, d.NextRetry)
	}
	if a := s.CommanderAttentionAt(now.Add(DeliveryRetryBase - time.Second)); len(a.Reasons) != 0 {
		t.Fatalf("delivery was retried before its backoff: %+v", a)
	}
	if a := s.CommanderAttentionAt(now.Add(DeliveryRetryBase)); len(a.Reasons) != 1 {
		t.Fatalf("due delivery was lost: %+v", a)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, ok := reopened.PendingDelivery("commander")
	if !ok || got.Attempts != 1 || !got.NextRetry.Equal(now.Add(DeliveryRetryBase)) {
		t.Fatalf("restart lost the retry schedule: %+v", got)
	}
	if a := reopened.CommanderAttentionAt(now.Add(DeliveryRetryBase - time.Second)); len(a.Reasons) != 0 {
		t.Fatalf("restart forgot the backoff: %+v", a)
	}
}

func TestDeliveryAttemptFailureRetainsPendingPage(t *testing.T) {
	s := open(t)
	if _, err := s.SaveDelivery("commander", Delivery{ID: "pending"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.sqlite.db.Exec("CREATE TRIGGER fail_delivery BEFORE UPDATE ON meta BEGIN SELECT RAISE(ABORT,'fixture delivery write failure'); END"); err != nil {
		t.Fatal(err)
	}
	if shown, err := s.NoteDeliveryShown("commander", "pending", time.Now()); err == nil || shown.ID != "" {
		t.Fatalf("failed write reported a presentation: %+v, %v", shown, err)
	}
	d, ok := s.PendingDelivery("commander")
	if !ok || d.ID != "pending" || d.Attempts != 0 || !d.NextRetry.IsZero() {
		t.Fatalf("failed write changed pending page: %+v", d)
	}
	if _, err := s.sqlite.db.Exec("DROP TRIGGER fail_delivery"); err != nil {
		t.Fatal(err)
	}
	if shown, err := s.NoteDeliveryShown("commander", d.ID, time.Now()); err != nil || shown.Attempts != 1 {
		t.Fatalf("recovery did not start with first attempt: %+v, %v", shown, err)
	}
}

func TestDeliveryPresentationRejectsReplacedPage(t *testing.T) {
	s := open(t)
	if _, err := s.SaveDelivery("commander", Delivery{ID: "replacement"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NoteDeliveryShown("commander", "old", time.Now()); err == nil {
		t.Fatal("old presentation was applied to replacement")
	}
	d, _ := s.PendingDelivery("commander")
	if d.Attempts != 0 || !d.NextRetry.IsZero() {
		t.Fatalf("replacement changed: %+v", d)
	}
}

func TestDeliveryRunBindingSurvivesRestartAndRequiresCurrentPage(t *testing.T) {
	s := open(t)
	if _, err := s.SaveDelivery("commander", Delivery{ID: "pending"}); err != nil {
		t.Fatal(err)
	}
	worker, err := s.AddRun(model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.AddRun(model.Run{Purpose: "commander"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.BindCommanderDeliveryRun("old", run.ID); err == nil {
		t.Fatal("old delivery was bound to current run")
	}
	if err := s.BindCommanderDeliveryRun("pending", worker.ID); err == nil {
		t.Fatal("worker run was recorded as commander")
	}
	if err := s.BindCommanderDeliveryRun("pending", run.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	d, ok := reopened.PendingDelivery("commander")
	if !ok || d.LastShownRun != run.ID || d.Attempts != 0 {
		t.Fatalf("restart lost run binding or counted a second attempt: %+v", d)
	}
}

// The backoff doubles per unacknowledged showing and caps, so the heartbeat is
// bounded but never abandons the delivery.
func TestDeliveryRetryDelayDoublesAndCaps(t *testing.T) {
	cases := []struct {
		attempts int
		want     time.Duration
	}{
		{0, 0},
		{1, 5 * time.Minute},
		{2, 10 * time.Minute},
		{3, 20 * time.Minute},
		{4, 40 * time.Minute},
		{5, time.Hour},
		{12, time.Hour},
	}
	for _, tt := range cases {
		if got := DeliveryRetryDelay(tt.attempts); got != tt.want {
			t.Fatalf("DeliveryRetryDelay(%d) = %s, want %s", tt.attempts, got, tt.want)
		}
	}
}
