package decider

// Independent verifier regressions for docs/42 §4.1: P0-2 (G1, the one-round
// handoff flag) and P0-3 (C3, delivery attempts/backoff/degradation).
//
// This file is test-only. It is written by the reviewer, not by the fixer: it
// re-derives the contract from state and does not rely on the fixer's own
// assertions. It must never edit production code or the existing tests.

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/state"
	"github.com/kksty/tianwu/internal/store"
)

const verifyNoteMarker = "VERIFIER-ONLY-NOTE-9f3：下一步验 /admin"

func openVerifyStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "verify-ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func openVerifyState(t *testing.T) *state.File {
	t.Helper()
	mem := state.New(filepath.Join(t.TempDir(), "state.md"))
	if err := mem.Save("# 笔记\n" + verifyNoteMarker + "\n"); err != nil {
		t.Fatal(err)
	}
	return mem
}

// verifyRunner builds a runner with a fake clock and an untouched note; the
// caller sets Commander/Session. A nil now means "real clock".
func verifyRunner(t *testing.T, st *store.Store, now *time.Time) *Runner {
	t.Helper()
	r := NewRunner(st, openVerifyState(t), "")
	r.ModelName = "verify-model"
	if now != nil {
		r.now = func() time.Time { return *now }
	}
	return r
}

func verifyGoal(t *testing.T, st *store.Store) {
	t.Helper()
	if _, err := st.AddGoal(model.Goal{Statement: "verify unfinished goal"}); err != nil {
		t.Fatal(err)
	}
}

// countFullNotes counts prompts that carry the whole note. The contract is
// "exactly the round after the handoff".
func countFullNotes(prompts []string) int {
	n := 0
	for _, p := range prompts {
		if strings.Contains(p, StateFullHeader) {
			n++
		}
	}
	return n
}

// G1: the whole note comes back on the round immediately after the handoff and
// never again until compaction/restart/new session. Two and three rounds later
// the prompt is a pointer and must not claim the previous round was the handoff.
func TestVerifyG1FullNoteOnlyOnTheRoundRightAfterTheHandoff(t *testing.T) {
	st := openVerifyStore(t)
	r := verifyRunner(t, st, nil)
	r.Session = func() RoundContext { return RoundContext{Resume: true} }
	var prompts []string
	r.Commander = func(_ context.Context, p string, _ bool) (RoundResult, error) {
		prompts = append(prompts, p)
		return RoundResult{Tokens: 1, ContextTokens: 110000, ContextWindow: 128000}, nil
	}
	for _, wake := range []string{"first", "handoff", "round-1-after", "round-2-after", "round-3-after"} {
		if d := r.Run(context.Background(), wake, true); d == nil {
			t.Fatalf("round %q did not run", wake)
		}
	}
	if len(prompts) != 5 {
		t.Fatalf("rounds = %d, want 5", len(prompts))
	}
	if strings.Contains(prompts[0], StateFullHeader) {
		t.Fatalf("the first round under no pressure got the whole note:\n%s", prompts[0])
	}
	if !strings.Contains(prompts[1], "## 交接（本轮必做）") {
		t.Fatalf("the 85%% round was not the handoff:\n%s", prompts[1])
	}
	if n := countFullNotes(prompts); n != 1 {
		t.Fatalf("full notes = %d, want exactly 1 (the round after the handoff)", n)
	}
	if !strings.Contains(prompts[2], StateFullHeader) || !strings.Contains(prompts[2], verifyNoteMarker) {
		t.Fatalf("the round after the handoff did not get the whole note:\n%s", prompts[2])
	}
	if !strings.Contains(prompts[2], "上一轮是交接轮") {
		t.Fatalf("the round after the handoff does not describe the rewrite:\n%s", prompts[2])
	}
	for i, later := range prompts[3:] {
		if strings.Contains(later, StateFullHeader) || strings.Contains(later, verifyNoteMarker) {
			t.Fatalf("pointer round %d re-pasted the note:\n%s", i+1, later)
		}
		if strings.Contains(later, "上一轮是交接轮") {
			t.Fatalf("pointer round %d still claims the previous round was the handoff:\n%s", i+1, later)
		}
		if !strings.Contains(later, "跑过交接轮") {
			t.Fatalf("pointer round %d does not know the cycle already had its handoff:\n%s", i+1, later)
		}
		if n := StateInjectionBytes(later); n != 0 {
			t.Fatalf("pointer round %d reports %d injected note bytes", i+1, n)
		}
	}
	r.mu.Lock()
	oneRoundFlag, cycleFlag := r.handoffRound, r.handoffDone
	r.mu.Unlock()
	if oneRoundFlag {
		t.Fatal("the one-round handoff flag stayed set after the round that consumed it")
	}
	if !cycleFlag {
		t.Fatal("the pressure-cycle flag was cleared while still above the warn band")
	}
}

// G1 counterexample: a nil round between the handoff and the next model round
// (here: the operator pauses the decider) must not consume the one-round flag;
// the next real round still gets the full note.
func TestVerifyG1PausedRoundDoesNotConsumeTheOneRoundHandoffFlag(t *testing.T) {
	st := openVerifyStore(t)
	r := verifyRunner(t, st, nil)
	r.Session = func() RoundContext { return RoundContext{Resume: true} }
	var prompts []string
	r.Commander = func(_ context.Context, p string, _ bool) (RoundResult, error) {
		prompts = append(prompts, p)
		return RoundResult{Tokens: 1, ContextTokens: 110000, ContextWindow: 128000}, nil
	}
	r.Run(context.Background(), "first", true)
	r.Run(context.Background(), "handoff", true)
	r.Pause()
	if d := r.Run(context.Background(), "heartbeat", false); d != nil {
		t.Fatal("a paused round executed")
	}
	r.Resume()
	if d := r.Run(context.Background(), "after-handoff", true); d == nil {
		t.Fatal("the round after resume did not run")
	}
	if len(prompts) != 3 {
		t.Fatalf("prompts = %d, want 3 (the paused attempt is not a model round)", len(prompts))
	}
	if !strings.Contains(prompts[2], StateFullHeader) || !strings.Contains(prompts[2], verifyNoteMarker) {
		t.Fatalf("the first real round after the handoff lost the whole note:\n%s", prompts[2])
	}
	if !strings.Contains(prompts[2], "上一轮是交接轮") {
		t.Fatalf("the round after the handoff does not claim the rewrite:\n%s", prompts[2])
	}
}

// G1 edge: the handoff round and a harness compaction happen in the same round.
// Compaction resets the pressure cycle (a later climb is a new handoff), while
// the note is still re-injected once for the handoff that just ran.
func TestVerifyG1HandoffAndCompactionInTheSameRound(t *testing.T) {
	st := openVerifyStore(t)
	r := verifyRunner(t, st, nil)
	r.Session = func() RoundContext { return RoundContext{Resume: true} }
	results := []RoundResult{
		{Tokens: 1, ContextTokens: 110000, ContextWindow: 128000},                 // first: climbs into pressure
		{Tokens: 1, ContextTokens: 20000, ContextWindow: 128000, Compacted: true}, // handoff + compaction
		{Tokens: 1, ContextTokens: 110000, ContextWindow: 128000},                 // after compaction: full note
		{Tokens: 1, ContextTokens: 100000, ContextWindow: 128000},                 // handoff again (new cycle)
		{Tokens: 1, ContextTokens: 100000, ContextWindow: 128000},                 // after the new handoff
		{Tokens: 1, ContextTokens: 100000, ContextWindow: 128000},                 // pointer
	}
	var prompts []string
	idx := 0
	r.Commander = func(_ context.Context, p string, _ bool) (RoundResult, error) {
		prompts = append(prompts, p)
		res := RoundResult{Tokens: 1}
		if idx < len(results) {
			res = results[idx]
		}
		idx++
		return res, nil
	}
	for _, wake := range []string{"first", "handoff+compact", "after-compact", "new-cycle-handoff", "after-new-handoff", "pointer"} {
		r.Run(context.Background(), wake, true)
	}
	if len(prompts) != 6 {
		t.Fatalf("rounds = %d, want 6", len(prompts))
	}
	if !strings.Contains(prompts[1], "## 交接（本轮必做）") {
		t.Fatalf("round 2 was not the handoff:\n%s", prompts[1])
	}
	if !strings.Contains(prompts[2], StateFullHeader) || !strings.Contains(prompts[2], "harness 刚压缩过上下文") {
		t.Fatalf("post-compaction round did not re-inject the note as a lossy reset:\n%s", prompts[2])
	}
	if strings.Contains(prompts[2], "上一轮是交接轮") {
		t.Fatalf("the compaction round claims the previous round was the handoff:\n%s", prompts[2])
	}
	if !strings.Contains(prompts[3], "## 交接（本轮必做）") {
		t.Fatalf("compaction did not reset the pressure cycle: a new 85%% round is not a handoff:\n%s", prompts[3])
	}
	if !strings.Contains(prompts[4], StateFullHeader) || !strings.Contains(prompts[4], "上一轮是交接轮") {
		t.Fatalf("the round after the second handoff lost the whole note:\n%s", prompts[4])
	}
	if strings.Contains(prompts[5], StateFullHeader) || strings.Contains(prompts[5], "上一轮是交接轮") {
		t.Fatalf("the round after the post-handoff round is not a pointer:\n%s", prompts[5])
	}
}

// C3: a model that never ACKs must not buy one full global round per heartbeat.
// Ten heartbeats are bounded by the retry schedule (5m, 10m, 20m, 40m then 1h):
// the 4th presentation degrades to delivery_id + summary but keeps the ACK
// requirement, and the delivery identity is stable across retries.
func TestVerifyC3TenHeartbeatsAreBoundedAndFourthPresentationDegrades(t *testing.T) {
	st := openVerifyStore(t)
	verifyGoal(t, st)
	start := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	now := start
	r := verifyRunner(t, st, &now)
	var prompts []string
	r.Commander = func(_ context.Context, p string, _ bool) (RoundResult, error) {
		prompts = append(prompts, p) // never acknowledges
		return RoundResult{Tokens: 1}, nil
	}
	if d := r.Run(context.Background(), "operator", true); d == nil {
		t.Fatal("the initial round did not run")
	}
	pending, ok := st.PendingDelivery("commander")
	if !ok {
		t.Fatal("the initial round did not create a delivery")
	}
	heartbeatRounds := 0
	for i := 0; i < 10; i++ {
		now = now.Add(5 * time.Minute)
		if !r.needsAttention() {
			t.Fatalf("heartbeat %d: an unfinished goal is not attention-worthy", i+1)
		}
		if !r.heartbeatWorthwhile() {
			continue
		}
		if r.Run(context.Background(), "heartbeat", false) != nil {
			heartbeatRounds++
		}
	}
	if heartbeatRounds > 4 {
		t.Fatalf("10 heartbeats opened %d rounds: the delivery schedule does not bound the wake loop", heartbeatRounds)
	}
	if heartbeatRounds != 3 {
		t.Fatalf("10 heartbeats opened %d rounds, want 3 (retries due at +5m, +15m, +35m)", heartbeatRounds)
	}
	if len(prompts) != 4 {
		t.Fatalf("presentations = %d, want 4 (initial + 3 retries)", len(prompts))
	}
	got, ok := st.PendingDelivery("commander")
	if !ok {
		t.Fatal("an unacknowledged delivery was dropped")
	}
	if got.ID != pending.ID {
		t.Fatalf("the delivery identity changed across retries: %s -> %s", pending.ID, got.ID)
	}
	if got.Attempts != 4 || !got.NextRetry.Equal(start.Add(75*time.Minute)) {
		t.Fatalf("attempts=%d next_retry=%s, want 4 and %s", got.Attempts, got.NextRetry, start.Add(75*time.Minute))
	}
	// The full record of retrieval.Context starts with a delivery_id; the
	// degraded form is a summary object. Three full presentations, then summary.
	fullFingerprint := `## 可续读的工作上下文` + "\n" + `{"delivery_id":`
	for i := 0; i < 3; i++ {
		if !strings.Contains(prompts[i], fullFingerprint) {
			t.Fatalf("presentation %d was not the full context:\n%s", i+1, prompts[i])
		}
		if strings.Contains(prompts[i], "只给摘要") {
			t.Fatalf("presentation %d degraded too early", i+1)
		}
		if !strings.Contains(prompts[i], pending.ID) {
			t.Fatalf("presentation %d does not carry the delivery id", i+1)
		}
	}
	fourth := prompts[3]
	if strings.Contains(fourth, fullFingerprint) || strings.Contains(fourth, "with delivery_id after processing") {
		t.Fatalf("the fourth presentation re-pasted the full context:\n%s", fourth)
	}
	for _, want := range []string{"只给摘要", pending.ID, "POST /api/context/ack", "GET /api/context", `"changes"`, `"survey_facts"`} {
		if !strings.Contains(fourth, want) {
			t.Fatalf("degraded presentation lost %q:\n%s", want, fourth)
		}
	}
}

// C3: a heartbeat (or any non-forced wake) inside the retry backoff must not open
// a global round; when the schedule matures the heartbeat must open exactly one.
func TestVerifyC3NoRoundBeforeTheRetryIsDue(t *testing.T) {
	st := openVerifyStore(t)
	verifyGoal(t, st)
	start := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	now := start
	r := verifyRunner(t, st, &now)
	calls := 0
	r.Commander = func(_ context.Context, _ string, _ bool) (RoundResult, error) {
		calls++
		return RoundResult{}, nil // never acknowledges
	}
	r.Run(context.Background(), "operator", true)
	d, ok := st.PendingDelivery("commander")
	if !ok {
		t.Fatal("delivery missing")
	}
	now = start.Add(store.DeliveryRetryBase - time.Second)
	if r.heartbeatWorthwhile() {
		t.Fatal("a heartbeat inside the backoff still found the delivery attention-worthy")
	}
	if r.Run(context.Background(), "heartbeat", false) != nil || calls != 1 {
		t.Fatal("a heartbeat inside the backoff opened a global round")
	}
	// A non-heartbeat automatic wake is gated the same way (the delivery is not
	// due, and it is the only pending demand).
	if r.Run(context.Background(), "fact", false) != nil || calls != 1 {
		t.Fatal("an automatic fact wake bypassed the delivery backoff")
	}
	now = d.NextRetry
	if !r.heartbeatWorthwhile() {
		t.Fatal("the delivery is due but the heartbeat does not consider it attention-worthy")
	}
	if r.Run(context.Background(), "heartbeat", false) == nil || calls != 2 {
		t.Fatal("the due retry did not open a round (delivery would starve)")
	}
}

// C3: a delivery whose retry is due still waits for the heartbeat path: an
// automatic non-heartbeat wake presented with only context_unacknowledged must
// not open a speculative round, but an explicit operator wake still does.
func TestVerifyC3DueDeliveryWaitsForHeartbeatOrExplicitWake(t *testing.T) {
	st := openVerifyStore(t)
	verifyGoal(t, st)
	start := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	now := start
	r := verifyRunner(t, st, &now)
	calls := 0
	r.Commander = func(_ context.Context, _ string, _ bool) (RoundResult, error) {
		calls++
		return RoundResult{}, nil
	}
	r.Run(context.Background(), "operator", true)
	if d, ok := st.PendingDelivery("commander"); ok {
		now = d.NextRetry
	} else {
		t.Fatal("delivery missing")
	}
	a := r.attention()
	if len(a.Reasons) != 1 || a.Reasons[0].Code != "context_unacknowledged" {
		t.Fatalf("due delivery reasons = %+v", a.Reasons)
	}
	if r.Run(context.Background(), "fact", false) != nil || calls != 1 {
		t.Fatal("an automatic non-heartbeat wake opened a round for a due unacknowledged delivery")
	}
	if r.Run(context.Background(), "operator", true) == nil || calls != 2 {
		t.Fatal("an explicit operator wake did not retry the due delivery")
	}
}

// C3: after ACK the heartbeat must go quiet (no further global rounds).
func TestVerifyC3AckSilencesTheHeartbeat(t *testing.T) {
	st := openVerifyStore(t)
	verifyGoal(t, st)
	start := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	now := start
	r := verifyRunner(t, st, &now)
	calls := 0
	r.Commander = func(_ context.Context, _ string, _ bool) (RoundResult, error) {
		calls++
		d, ok := st.PendingDelivery("commander")
		if !ok {
			return RoundResult{}, nil
		}
		return RoundResult{}, st.AcknowledgeDelivery("commander", d.ID)
	}
	if d := r.Run(context.Background(), "operator", true); d == nil || !d.AttentionDelivered {
		t.Fatal("the acknowledging round was not recorded as delivered")
	}
	if _, ok := st.PendingDelivery("commander"); ok {
		t.Fatal("ACK left the delivery pending")
	}
	if r.heartbeatWorthwhile() {
		t.Fatal("ACK left the heartbeat worth a round")
	}
	now = start.Add(24 * time.Hour)
	if r.heartbeatWorthwhile() || r.Run(context.Background(), "heartbeat", false) != nil || calls != 1 {
		t.Fatal("a day after ACK the heartbeat still opened a round")
	}
}

// C3: the degraded (summary-only) round must still be acknowledgeable by its
// delivery_id; ACK then silences the schedule and the next delivery starts from
// attempt 0.
func TestVerifyC3DegradedRoundCanBeAcknowledged(t *testing.T) {
	st := openVerifyStore(t)
	verifyGoal(t, st)
	start := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	now := start
	r := verifyRunner(t, st, &now)
	var prompts []string
	acked := false
	r.Commander = func(_ context.Context, p string, _ bool) (RoundResult, error) {
		prompts = append(prompts, p)
		if strings.Contains(p, "只给摘要") {
			d, ok := st.PendingDelivery("commander")
			if !ok {
				return RoundResult{}, errors.New("degraded prompt with no pending delivery")
			}
			acked = true
			return RoundResult{}, st.AcknowledgeDelivery("commander", d.ID)
		}
		return RoundResult{}, nil
	}
	r.Run(context.Background(), "operator", true)
	for i := 0; i < 10 && !acked; i++ {
		d, ok := st.PendingDelivery("commander")
		if !ok {
			break
		}
		now = d.NextRetry
		dec := r.Run(context.Background(), "heartbeat", false)
		if dec == nil {
			t.Fatalf("retry %d did not run despite being due", i+1)
		}
		if strings.Contains(prompts[len(prompts)-1], "只给摘要") && !dec.AttentionDelivered {
			t.Fatal("the degraded round was not marked delivered")
		}
	}
	if !acked {
		t.Fatalf("never reached the degraded presentation; prompts=%d", len(prompts))
	}
	if _, ok := st.PendingDelivery("commander"); ok {
		t.Fatal("the delivery acknowledged from the degraded round is still pending")
	}
	if r.heartbeatWorthwhile() {
		t.Fatal("a delivery acknowledged from the degraded round still wakes the heartbeat")
	}
	// A later event creates a brand-new delivery with a fresh schedule.
	verifyGoal(t, st)
	now = now.Add(time.Minute)
	d := r.Run(context.Background(), "operator", true)
	if d == nil {
		t.Fatal("the new demand did not open a round")
	}
	if fresh, ok := st.PendingDelivery("commander"); ok {
		if fresh.Attempts != 1 || fresh.ID == "" {
			t.Fatalf("a new delivery did not start a fresh schedule: %+v", fresh)
		}
	}
}

// C3: attempts/next_retry are durable. A new Runner on the same store and a
// reopened store both remember the schedule and do not replay immediately.
func TestVerifyC3RetryScheduleSurvivesRestarts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "verify-ledger.json")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	verifyGoal(t, st)
	start := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	now := start
	r := verifyRunner(t, st, &now)
	calls := 0
	r.Commander = func(_ context.Context, _ string, _ bool) (RoundResult, error) {
		calls++
		return RoundResult{}, nil // never acknowledges
	}
	r.Run(context.Background(), "operator", true)
	d, ok := st.PendingDelivery("commander")
	if !ok || d.Attempts != 1 || !d.NextRetry.Equal(start.Add(store.DeliveryRetryBase)) {
		t.Fatalf("initial schedule = %+v", d)
	}

	// Same store, new Runner (a process restart that keeps the ledger).
	now = start.Add(2 * time.Minute)
	r2 := verifyRunner(t, st, &now)
	r2.Commander = func(_ context.Context, _ string, _ bool) (RoundResult, error) {
		calls++
		return RoundResult{}, nil
	}
	if r2.heartbeatWorthwhile() {
		t.Fatal("the restarted runner forgot the backoff")
	}
	if r2.Run(context.Background(), "heartbeat", false) != nil || calls != 1 {
		t.Fatal("the restarted runner opened a round before the retry was due")
	}
	now = d.NextRetry
	if r2.Run(context.Background(), "heartbeat", false) == nil || calls != 2 {
		t.Fatal("the restarted runner starved the due retry")
	}
	d2, ok := st.PendingDelivery("commander")
	if !ok || d2.Attempts != 2 {
		t.Fatalf("the second presentation was not recorded: %+v", d2)
	}
	if want := d.NextRetry.Add(10 * time.Minute); !d2.NextRetry.Equal(want) {
		t.Fatalf("next_retry = %s, want %s (10m backoff after 2 attempts)", d2.NextRetry, want)
	}

	// Reopened store (a full process restart).
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, ok := reopened.PendingDelivery("commander")
	if !ok || got.ID != d.ID || got.Attempts != 2 || !got.NextRetry.Equal(d2.NextRetry) {
		t.Fatalf("reopening the ledger lost the retry schedule: %+v", got)
	}
	if a := reopened.CommanderAttentionAt(got.NextRetry.Add(-time.Second)); len(a.Reasons) != 0 {
		t.Fatalf("the reopened ledger retried early: %+v", a)
	}
	if a := reopened.CommanderAttentionAt(got.NextRetry); len(a.Reasons) != 1 || a.Reasons[0].Code != "context_unacknowledged" {
		t.Fatalf("the reopened ledger lost the due demand: %+v", a)
	}
}

// C3: pause/resume does not defeat the delivery schedule (resume clears the
// transport backoff, not the retry schedule), and a paused decider spends
// nothing.
func TestVerifyC3PauseResumeKeepsDeliveryBackoff(t *testing.T) {
	st := openVerifyStore(t)
	verifyGoal(t, st)
	start := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	now := start
	r := verifyRunner(t, st, &now)
	calls := 0
	r.Commander = func(_ context.Context, _ string, _ bool) (RoundResult, error) {
		calls++
		return RoundResult{}, nil
	}
	r.Run(context.Background(), "operator", true)
	d, ok := st.PendingDelivery("commander")
	if !ok {
		t.Fatal("delivery missing")
	}
	r.Pause()
	now = start.Add(time.Minute)
	if r.Run(context.Background(), "operator", true) != nil || calls != 1 {
		t.Fatal("a paused decider spent a round")
	}
	r.Resume()
	now = d.NextRetry.Add(-time.Second)
	if r.Run(context.Background(), "heartbeat", false) != nil || calls != 1 {
		t.Fatal("resume bypassed the delivery backoff")
	}
	now = d.NextRetry
	if r.Run(context.Background(), "heartbeat", false) == nil || calls != 2 {
		t.Fatal("the due retry did not run after resume")
	}
}

// C3: a failed transport must not consume events and must not replace or forget
// the pending delivery identity; its 30s recovery backoff gates automatic wakes,
// but the still-undelivered events remain their own demand so a later heartbeat
// is not starved by the delivery's 5m schedule.
func TestVerifyC3TransportFailureKeepsDemandIdentityAndReschedules(t *testing.T) {
	st := openVerifyStore(t)
	verifyGoal(t, st)
	start := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	now := start
	r := verifyRunner(t, st, &now)
	before := st.CommanderAttention()
	calls := 0
	failing := true
	var prompts []string
	r.Commander = func(_ context.Context, p string, _ bool) (RoundResult, error) {
		calls++
		prompts = append(prompts, p)
		if failing {
			return RoundResult{}, errors.New("executor transport reset")
		}
		return RoundResult{}, nil
	}
	failed := r.Run(context.Background(), "operator", true)
	if failed == nil || failed.Error == "" || failed.AttentionDelivered {
		t.Fatalf("the transport failure was recorded as delivered: %+v", failed)
	}
	if st.CommanderAttention().DeliveredThrough != before.DeliveredThrough {
		t.Fatal("a failed transport consumed the event journal")
	}
	pending, ok := st.PendingDelivery("commander")
	if !ok {
		t.Fatal("a failed transport dropped the pending delivery")
	}
	if pending.Attempts != 1 || !pending.NextRetry.Equal(start.Add(store.DeliveryRetryBase)) {
		t.Fatalf("a failed transport did not schedule the retry: %+v", pending)
	}
	now = start.Add(10 * time.Second)
	if r.Run(context.Background(), "heartbeat", false) != nil || calls != 1 {
		t.Fatal("the 30s transport backoff was bypassed")
	}
	failing = false
	now = start.Add(31 * time.Second)
	recovered := r.Run(context.Background(), "heartbeat", false)
	if recovered == nil || calls != 2 || !recovered.AttentionDelivered {
		t.Fatal("the recovered heartbeat did not consume the preserved demand")
	}
	if !strings.Contains(prompts[1], pending.ID) {
		t.Fatal("the recovered round did not present the same delivery identity")
	}
	r.mu.Lock()
	retryAfter := r.retryAfter
	r.mu.Unlock()
	if !retryAfter.IsZero() {
		t.Fatalf("a successful delivery left the transport backoff armed: %s", retryAfter)
	}
	again, ok := st.PendingDelivery("commander")
	if !ok || again.ID != pending.ID || again.Attempts != 2 {
		t.Fatalf("the re-presented delivery = %+v", again)
	}
	if err := st.AcknowledgeDelivery("commander", again.ID); err != nil {
		t.Fatal(err)
	}
	if r.heartbeatWorthwhile() {
		t.Fatal("ACK after recovery still wakes the heartbeat")
	}
}

// C3: a genuine new demand during the delivery backoff must still be able to
// open a round (the gate is per-delivery, not a global freeze), and that round
// counts as a presentation of the pending delivery.
func TestVerifyC3NewDemandDuringBackoffStillRuns(t *testing.T) {
	st := openVerifyStore(t)
	verifyGoal(t, st)
	start := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	now := start
	r := verifyRunner(t, st, &now)
	calls := 0
	r.Commander = func(_ context.Context, _ string, _ bool) (RoundResult, error) {
		calls++
		return RoundResult{}, nil // never acknowledges
	}
	r.Run(context.Background(), "operator", true)
	pending, ok := st.PendingDelivery("commander")
	if !ok {
		t.Fatal("delivery missing")
	}
	verifyGoal(t, st) // a real new global event
	now = start.Add(time.Minute)
	if !r.heartbeatWorthwhile() {
		t.Fatal("a new global event was suppressed by the delivery backoff")
	}
	if r.Run(context.Background(), "fact", false) == nil || calls != 2 {
		t.Fatal("a new global event did not open a round during the delivery backoff")
	}
	got, ok := st.PendingDelivery("commander")
	if !ok || got.ID != pending.ID {
		t.Fatalf("the pending delivery identity changed: %+v", got)
	}
	if got.Attempts != 2 {
		t.Fatalf("the round that presented the pending delivery recorded attempts=%d, want 2", got.Attempts)
	}
}

// C3: DeliveryRetryDelay must double from 5m, cap at 1h, and never overflow or
// go negative, including absurd attempt counts read back from a corrupted ledger.
func TestVerifyC3RetryDelayIsOverflowSafeAndCapped(t *testing.T) {
	cases := map[int]time.Duration{
		-7:         0,
		0:          0,
		1:          5 * time.Minute,
		2:          10 * time.Minute,
		3:          20 * time.Minute,
		4:          40 * time.Minute,
		5:          time.Hour,
		6:          time.Hour,
		63:         time.Hour,
		1 << 20:    time.Hour,
		1<<31 - 2:  time.Hour,
		2147483647: time.Hour,
	}
	for attempts, want := range cases {
		got := store.DeliveryRetryDelay(attempts)
		if got != want {
			t.Fatalf("DeliveryRetryDelay(%d) = %s, want %s", attempts, got, want)
		}
	}
	// The schedule written by NoteDeliveryShown keeps the same contract, even
	// when the stored attempt counter is already past the cap.
	st := openVerifyStore(t)
	at := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	if _, err := st.SaveDelivery("commander", store.Delivery{
		ID:       "delivery_verify_cap",
		Expected: st.ReadCheckpoint("commander"),
		Attempts: 4,
	}); err != nil {
		t.Fatal(err)
	}
	shown, err := st.NoteDeliveryShown("commander", "delivery_verify_cap", at)
	if err != nil {
		t.Fatal(err)
	}
	if shown.Attempts != 5 || !shown.NextRetry.Equal(at.Add(time.Hour)) {
		t.Fatalf("capped schedule = attempts %d next_retry %s, want 5 and %s", shown.Attempts, shown.NextRetry, at.Add(time.Hour))
	}
}

// C3 backward compatibility: a delivery written by a pre-fix ledger has no
// attempts/next_retry. A zero NextRetry must mean "due now", never "never", so
// the upgrade cannot silence an unacknowledged delivery; the first showing then
// starts the schedule.
func TestVerifyC3LegacyDeliveryWithoutScheduleIsDueNow(t *testing.T) {
	st := openVerifyStore(t)
	at := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	if _, err := st.SaveDelivery("commander", store.Delivery{
		ID:       "delivery_legacy",
		Expected: st.ReadCheckpoint("commander"),
	}); err != nil {
		t.Fatal(err)
	}
	a := st.CommanderAttentionAt(at)
	if len(a.Reasons) != 1 || a.Reasons[0].Code != "context_unacknowledged" || a.Reasons[0].ID != "delivery_legacy" {
		t.Fatalf("a legacy delivery is not due now: %+v", a.Reasons)
	}
	shown, err := st.NoteDeliveryShown("commander", "delivery_legacy", at)
	if err != nil {
		t.Fatal(err)
	}
	if shown.Attempts != 1 || !shown.NextRetry.Equal(at.Add(store.DeliveryRetryBase)) {
		t.Fatalf("legacy delivery did not start a schedule: attempts=%d next_retry=%s", shown.Attempts, shown.NextRetry)
	}
	if a := st.CommanderAttentionAt(at.Add(store.DeliveryRetryBase - time.Second)); len(a.Reasons) != 0 {
		t.Fatalf("legacy delivery ignored its new backoff: %+v", a.Reasons)
	}
}
