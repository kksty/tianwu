package decider

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/retrieval"
)

// An unacknowledged delivery is retried, but on its own schedule: a heartbeat
// may not re-open a full global round on every tick for the same page. The
// retry must still happen — dropping it would silently lose the delivery.
func TestHeartbeatRetriesUnacknowledgedFirstDelivery(t *testing.T) {
	for _, failed := range []bool{false, true} {
		s := newStore(t)
		_, _ = s.AddGoal(model.Goal{Statement: "unfinished"})
		r := NewRunner(s, nil, "")
		r.ModelName = "fixture"
		now := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
		r.now = func() time.Time { return now }
		calls := 0
		r.Commander = func(context.Context, string, bool) (RoundResult, error) {
			calls++
			if failed {
				return RoundResult{}, errors.New("transport failed")
			}
			return RoundResult{}, nil
		}
		r.Run(context.Background(), "manual", true)
		d, ok := s.PendingDelivery("commander")
		if !ok {
			t.Fatal("delivery missing")
		}
		r.Pause()
		if r.Run(context.Background(), "heartbeat", false) != nil || calls != 1 {
			t.Fatal("pending delivery bypassed pause")
		}
		r.Resume()
		// The first retry waits DeliveryRetryBase; a heartbeat before that must
		// not spawn a speculative global round. (After a transport failure the
		// undelivered events are their own demand and a resumed operator wake is
		// explicit, so the delivery backoff is not the only gate there.)
		if !failed {
			if r.Run(context.Background(), "heartbeat", false) != nil || calls != 1 {
				t.Fatal("pending delivery retried before its backoff")
			}
		}
		now = now.Add(10 * time.Minute)
		if r.Run(context.Background(), "heartbeat", false) == nil || calls != 2 {
			t.Fatal("pending delivery was starved")
		}
		again, err := retrieval.WorkingContext(s, "commander")
		if err != nil || again.DeliveryID != d.ID {
			t.Fatal("retry replaced pending delivery")
		}
		if err := s.AcknowledgeDelivery("commander", d.ID); err != nil {
			t.Fatal(err)
		}
		if r.heartbeatWorthwhile() {
			t.Fatal("acknowledged, drained context kept waking")
		}
	}
}

func TestDeliveryAttemptWriteFailureDoesNotCallCommander(t *testing.T) {
	s := newStore(t)
	if _, err := s.AddGoal(model.Goal{Statement: "unfinished"}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", s.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Fail the presentation write, while allowing context creation and event
	// binding to succeed. A storage failure must not look like a concurrent ACK.
	if _, err := db.Exec(`CREATE TRIGGER fail_delivery_show BEFORE UPDATE ON meta
		WHEN json_extract(NEW.payload,'$.memory.deliveries.commander.attempts') > 0
		BEGIN SELECT RAISE(ABORT,'fixture delivery attempt failure'); END`); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(s, nil, "")
	r.ModelName = "fixture"
	calls := 0
	r.Commander = func(context.Context, string, bool) (RoundResult, error) {
		calls++
		return RoundResult{}, nil
	}
	d := r.Run(context.Background(), "manual", true)
	if d == nil || !strings.Contains(d.Error, "保存上下文交付尝试失败") || d.AttentionDelivered || calls != 0 {
		t.Fatalf("failed presentation called model or consumed events: %+v, calls=%d", d, calls)
	}
	pending, ok := s.PendingDelivery("commander")
	if !ok || pending.Attempts != 0 {
		t.Fatalf("failed presentation lost page: %+v", pending)
	}
	if _, err := db.Exec("DROP TRIGGER fail_delivery_show"); err != nil {
		t.Fatal(err)
	}
	d = r.Run(context.Background(), "manual", true)
	if d == nil || d.Error != "" || !d.AttentionDelivered || calls != 1 {
		t.Fatalf("retry did not recover: %+v, calls=%d", d, calls)
	}
	got, _ := s.PendingDelivery("commander")
	if got.ID != pending.ID || got.Attempts != 1 {
		t.Fatalf("retry replaced page or counted failed attempt: %+v", got)
	}
}

// A model that never acknowledges its context must not buy one full global round
// per heartbeat forever. The delivery backs off (5m, 10m, 20m, 40m, then 1h) and,
// after DeliveryPointerAfter full showings, degrades to its id plus a summary.
// That is the audit-42 C3 invariant: rounds are bounded by the delivery state
// machine, not by a prompt that asks the model to remember to ACK.
func TestUnacknowledgedDeliveryBacksOffAndDegradesToSummary(t *testing.T) {
	s := newStore(t)
	_, _ = s.AddGoal(model.Goal{Statement: "unfinished"})
	r := NewRunner(s, nil, "")
	r.ModelName = "fixture"
	start := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	now := start
	r.now = func() time.Time { return now }
	var prompts []string
	r.Commander = func(_ context.Context, prompt string, _ bool) (RoundResult, error) {
		prompts = append(prompts, prompt)
		return RoundResult{}, nil // never acks
	}
	r.Run(context.Background(), "operator", true)
	rounds := 1
	for range 10 {
		now = now.Add(5 * time.Minute)
		if !r.heartbeatWorthwhile() {
			continue
		}
		if r.Run(context.Background(), "heartbeat", false) != nil {
			rounds++
		}
	}
	if rounds != 4 || len(prompts) != 4 {
		t.Fatalf("rounds in 10 heartbeats = %d (prompts %d), want the backoff to bound it to 4", rounds, len(prompts))
	}
	delivery, ok := s.PendingDelivery("commander")
	if !ok {
		t.Fatal("unacknowledged delivery was lost")
	}
	if delivery.Attempts != 4 || !delivery.NextRetry.Equal(start.Add(75*time.Minute)) {
		t.Fatalf("attempts=%d next_retry=%s", delivery.Attempts, delivery.NextRetry)
	}
	for i := 0; i < 3; i++ {
		if !strings.Contains(prompts[i], "## 可续读的工作上下文\n") {
			t.Fatalf("presentation %d lost the full context:\n%s", i+1, prompts[i])
		}
	}
	// The fourth presentation degrades to the summary, and it still requires the
	// acknowledgement: only the payload degrades, never the ACK.
	if strings.Contains(prompts[3], "## 可续读的工作上下文\n") {
		t.Fatalf("the fourth presentation re-pasted the full context:\n%s", prompts[3])
	}
	if !strings.Contains(prompts[3], "只给摘要") || !strings.Contains(prompts[3], "context/ack") {
		t.Fatalf("degraded presentation lost the ACK requirement:\n%s", prompts[3])
	}
	// The next heartbeat is still inside the 40-minute backoff: no round.
	now = now.Add(5 * time.Minute)
	if r.heartbeatWorthwhile() || r.Run(context.Background(), "heartbeat", false) != nil {
		t.Fatal("a heartbeat inside the retry backoff opened a round")
	}
}
