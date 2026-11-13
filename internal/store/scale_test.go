package store

// Scale measurement for the communication and page/list read paths (audit item
// 9). This is a measurement test, not a performance gate: it builds a synthetic
// large ledger once (thousands of facts, hundreds of intents, thousands of
// messages, a thousand runs), then reports medians for the paths the console,
// the router and the token aggregation use.
//
// The numbers are machine- and disk-dependent; the assertions are deliberately
// loose (catastrophic seconds-level regressions only). Optimise a path only when
// a measured number here is the actual problem — do not claim constant
// complexity from this test: several paths are linear scans by design (a stable
// page over a single-process SQLite ledger).

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/model"
)

const (
	scaleFacts    = 4000
	scaleIntents  = 500
	scaleLive     = 100 // running directions, each with premised facts
	scaleMessages = 5000
	scaleRuns     = 1000
)

func openScaleStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "scale.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// seedScaleLedger plants the synthetic ledger directly under the lock, in one
// flush. Going through the additive API would pay one durable commit per row,
// which measures disk sync, not the read/routing paths this test is about.
func seedScaleLedger(t *testing.T, s *Store) (factIDs, intentIDs []string, threadRoot string) {
	t.Helper()
	now := time.Now()
	s.mu.Lock()
	for i := 0; i < scaleFacts; i++ {
		id := NewID("f")
		factIDs = append(factIDs, id)
		s.db.Facts = append(s.db.Facts, model.Fact{
			ID: id, IntentID: "pending-intent", Kind: "host",
			Title:     fmt.Sprintf("10.0.%d.%d 记录 %d", i/250, i%250, i),
			Summary:   strings.Repeat("观察摘要", 40),
			Status:    "unverified",
			CreatedAt: now,
		})
	}
	for i := 0; i < scaleIntents; i++ {
		id := NewID("in")
		intentIDs = append(intentIDs, id)
		in := model.Intent{ID: id, Title: fmt.Sprintf("方向 %d", i), CreatedAt: now}
		if i < scaleLive {
			in.Status = model.StatusRunning
			in.Attempts = 1
			in.FromFactIDs = []string{factIDs[2*i], factIDs[2*i+1]}
			in.InterestEntityRefs = []string{fmt.Sprintf("host:10.0.%d.%d", i/250, i%250)}
			runID := NewID("r")
			in.RunID = runID
			s.db.Runs = append(s.db.Runs, model.Run{
				ID: runID, IntentID: id, Node: "local", Status: model.RunRunning,
				Generation: 1, StartedAt: now, ContextWindow: 32000, ContextTokens: 12000,
				Usage: model.Usage{InputTokens: 1000, OutputTokens: 200, TotalTokens: 1200, Known: true},
			})
		} else if i%3 == 0 {
			in.Status = model.StatusDone
		} else if i%3 == 1 {
			in.Status = model.StatusFailed
		} else {
			in.Status = model.StatusParked
		}
		s.db.Intents = append(s.db.Intents, in)
	}
	for i := 0; len(s.db.Runs) < scaleRuns; i++ {
		in := intentIDs[i%len(intentIDs)]
		s.db.Runs = append(s.db.Runs, model.Run{
			ID: NewID("r"), IntentID: in, Node: "local", Status: model.RunDone,
			Generation: 1, StartedAt: now.Add(-time.Duration(i) * time.Minute),
			EndedAt: ptrTime(now), Tokens: 900,
			Usage: model.Usage{InputTokens: 800, OutputTokens: 100, TotalTokens: 900},
		})
	}
	for i := 0; i < scaleMessages; i++ {
		to := "pt_" + intentIDs[i%len(intentIDs)]
		from := "pt_" + intentIDs[(i+1)%len(intentIDs)]
		if i%3 == 0 {
			from = "commander"
		}
		id := NewID("m")
		msg := model.Message{
			ID: id, Seq: s.nextMessageSeqLocked(), EventID: id,
			ProjectID: "p_scale", FromParticipantID: from, ToParticipantID: to,
			Kind: "update", Text: strings.Repeat("消息正文", 12),
			Status: model.MessageQueued, CreatedAt: now, UpdatedAt: now,
		}
		if i%5 == 0 {
			// Delivered rows are part of the same table; filtering them is the
			// cost a console/participant page pays.
			msg.Status = model.MessageAccepted
			msg.DeliveredRunID = "r_delivered"
			msg.DeliveryPath = "ioa"
		}
		s.db.Messages = append(s.db.Messages, msg)
	}
	// One request thread inside the message table: a request plus 20 associated
	// replies/results, so the correlation read path is measured on a real (still
	// linear in table size) scan rather than a fresh ledger.
	if len(s.db.Messages) >= 21 {
		threadRoot = s.db.Messages[0].ID
		for i := 1; i <= 20; i++ {
			s.db.Messages[i].InReplyTo = threadRoot
			s.db.Messages[i].Kind = model.MessageResult
			if i%2 == 0 {
				s.db.Messages[i].Kind = "answer"
			}
		}
	}
	err := s.flushNoRev("fact", "intent", "run", "message")
	s.mu.Unlock()
	if err != nil {
		t.Fatalf("seed flush: %v", err)
	}
	return factIDs, intentIDs, threadRoot
}

func ptrTime(t time.Time) *time.Time { return &t }

// medianOf runs fn n times and returns the median duration plus the run count.
func medianOf(t *testing.T, n int, fn func()) time.Duration {
	t.Helper()
	fn() // warm-up/allocation
	times := make([]time.Duration, 0, n)
	for i := 0; i < n; i++ {
		start := time.Now()
		fn()
		times = append(times, time.Since(start))
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	return times[len(times)/2]
}

func TestScaleReadPathsRoutingAndTokenAggregation(t *testing.T) {
	s := openScaleStore(t)
	factIDs, intentIDs, threadRoot := seedScaleLedger(t, s)
	if len(factIDs) != scaleFacts || len(intentIDs) != scaleIntents {
		t.Fatalf("seeded facts=%d intents=%d", len(factIDs), len(intentIDs))
	}
	oneIntent := intentIDs[0]
	oneParticipant := "pt_" + oneIntent

	report := func(name string, d time.Duration, runs int) {
		t.Logf("scale %-28s median %9s (n=%d)", name, d.Round(time.Microsecond), runs)
	}

	// ---- page/list paths (the primitives behind console and participant pages)
	report("Snapshot (full clone)", medianOf(t, 5, func() { _ = s.Snapshot() }), 5)
	report("Messages console page (200)", medianOf(t, 5, func() { _ = s.Messages(200) }), 5)
	report("MessagesForBox page (100)", medianOf(t, 5, func() { _ = s.MessagesForBox(oneParticipant, 0, 100, "") }), 5)
	report("PendingFor inbox", medianOf(t, 5, func() { _ = s.PendingFor(oneParticipant) }), 5)
	report("ListIntents", medianOf(t, 5, func() { _ = s.ListIntents() }), 5)
	report("ListFacts(intent page 50)", medianOf(t, 5, func() { _ = s.ListFacts(oneIntent, 50) }), 5)
	report("SearchFacts(q, 50)", medianOf(t, 5, func() { _ = s.SearchFacts("10.0.1.", "", 50) }), 5)
	report("Runs (all)", medianOf(t, 5, func() { _ = s.Runs() }), 5)
	report("QueueStatus(all)", medianOf(t, 5, func() { _ = s.QueueStatus("") }), 5)
	thread, ok := s.MessageThread(threadRoot, 0)
	if !ok || len(thread.Replies)+len(thread.Results) == 0 {
		t.Fatalf("seeded thread not readable: ok=%v %+v", ok, thread)
	}
	report("MessageThread(seeded)", medianOf(t, 5, func() { _, _ = s.MessageThread(threadRoot, 0) }), 5)

	// Token aggregation: the board/fleet path reads the run rows and sums usage.
	var aggregateRuns int
	tokenAgg := medianOf(t, 5, func() {
		var in, out, total int
		aggregateRuns = 0
		for _, r := range s.Runs() {
			in += r.Usage.InputTokens
			out += r.Usage.OutputTokens
			total += r.Usage.TotalTokens
			aggregateRuns++
		}
		if in < 0 || out < 0 || total < 0 {
			t.Fatalf("impossible usage sum")
		}
	})
	report(fmt.Sprintf("token aggregation (%d runs)", aggregateRuns), tokenAgg, 5)

	// ---- routing pass: first pass drains the seeded backlog, later passes are
	// cheap no-op passes (checkpoint catch-up). This is the fair-delivery cost.
	backlog := time.Now()
	if err := s.RouteFactChanges(64); err != nil {
		t.Fatalf("route backlog: %v", err)
	}
	report("RouteFactChanges (backlog)", time.Since(backlog), 1)
	report("RouteFactChanges (caught up)", medianOf(t, 5, func() {
		if err := s.RouteFactChanges(64); err != nil {
			t.Fatalf("route caught-up: %v", err)
		}
	}), 5)
	queues := s.QueueStatus("")
	queuedNotices := 0
	for _, q := range queues {
		queuedNotices += q.Pending
	}
	t.Logf("scale queues=%d pending=%d", len(queues), queuedNotices)

	// ---- lock contention proxy: a reader's latency while another goroutine is
	// writing durably. The store lock is held across the SQLite commit, so this
	// measures the hold a read path actually waits through.
	writers := 60
	latencies := make([]time.Duration, 0, 64)
	writerDone := make(chan struct{})
	var writeErr error
	go func() {
		defer close(writerDone)
		for i := 0; i < writers; i++ {
			to := "pt_" + intentIDs[(i+7)%len(intentIDs)]
			if _, _, err := s.AddMessage(model.Message{
				FromParticipantID: "pt_writer", ToParticipantID: to,
				Kind: "update", Text: "写入压力", Status: model.MessageQueued,
			}); err != nil {
				writeErr = err
				return
			}
		}
	}()
loop:
	for {
		start := time.Now()
		_, _ = s.GetIntent(oneIntent)
		latencies = append(latencies, time.Since(start))
		select {
		case <-writerDone:
			break loop
		default:
		}
		if len(latencies) > 200000 {
			t.Fatal("reader loop did not observe the writer finishing")
		}
	}
	if writeErr != nil {
		t.Fatalf("writer: %v", writeErr)
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	if len(latencies) == 0 {
		t.Fatal("no reader samples")
	}
	p50 := latencies[len(latencies)*50/100]
	p95 := latencies[min(len(latencies)-1, len(latencies)*95/100)]
	maxLat := latencies[len(latencies)-1]
	t.Logf("scale lock-held read (GetIntent during %d durable writes): n=%d p50=%s p95=%s max=%s",
		writers, len(latencies), p50.Round(time.Microsecond), p95.Round(time.Microsecond), maxLat.Round(time.Microsecond))

	// ---- loose catastrophic-regression bounds only.
	for _, bound := range []struct {
		name string
		d    time.Duration
		upTo time.Duration
	}{
		{"Snapshot", medianOf(t, 1, func() { _ = s.Snapshot() }), 5 * time.Second},
		{"Messages page", medianOf(t, 1, func() { _ = s.Messages(200) }), 3 * time.Second},
		{"RouteFactChanges caught up", medianOf(t, 1, func() { _ = s.RouteFactChanges(64) }), 3 * time.Second},
		{"token aggregation", tokenAgg, 3 * time.Second},
	} {
		if bound.d > bound.upTo {
			t.Errorf("%s took %s, over the loose %s bound", bound.name, bound.d, bound.upTo)
		}
	}
}
