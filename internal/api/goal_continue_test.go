package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/decider"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// appendGoalCommander is the decider's model call in these tests: it only counts
// rounds, so "the new goal woke the commander once" is observable without a
// model.
type appendGoalCommander struct {
	mu    sync.Mutex
	calls int
}

func (c *appendGoalCommander) run(context.Context, string, bool) (decider.RoundResult, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return decider.RoundResult{}, nil
}

func (c *appendGoalCommander) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// startAppendGoalDecider starts a real decider loop on the fixture store with a
// tiny debounce and no heartbeat, so a round only happens when the endpoint
// wakes it.
func startAppendGoalDecider(t *testing.T, st *store.Store) (*decider.Runner, *appendGoalCommander) {
	t.Helper()
	cmd := &appendGoalCommander{}
	r := decider.NewRunner(st, nil, "")
	r.ModelName = "fixture"
	r.Commander = cmd.run
	r.Debounce = 5 * time.Millisecond
	r.Heartbeat = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r.Start(ctx)
	return r, cmd
}

type appendGoalResponse struct {
	Goal            model.Goal `json:"goal"`
	Created         bool       `json:"created"`
	Resumed         bool       `json:"resumed"`
	ResumedFrom     string     `json:"resumed_from"`
	DeciderWoken    bool       `json:"decider_woken"`
	PausedBefore    bool       `json:"paused_before"`
	PauseKindBefore string     `json:"pause_kind_before"`
	PausedAfter     bool       `json:"paused_after"`
	PauseKindAfter  string     `json:"pause_kind_after"`
	Retryable       bool       `json:"retryable"`
	Note            string     `json:"note"`
}

func decodeAppendGoal(t *testing.T, rec *httptest.ResponseRecorder) appendGoalResponse {
	t.Helper()
	var out appendGoalResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return out
}

// seedStampedGoal writes one completed goal with a full receipt (evidence, note,
// cited fact and versions) so an append can prove it leaves that history alone.
func seedStampedGoal(t *testing.T, st *store.Store) model.Goal {
	t.Helper()
	e, err := st.AddEvidence(model.Evidence{URI: "in_old/proof.txt"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := st.AddFact(model.Fact{Kind: "vuln", Title: "旧目标依据", EvidenceIDs: []string{e.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReviewFact(model.Review{FactID: f.ID, Verdict: "pass", VerifierRun: "run-fixture"}); err != nil {
		t.Fatal(err)
	}
	g, err := st.AddGoal(model.Goal{Statement: "旧目标：拿到一条可复核证据"})
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := st.PinGoal(g.ID, e.ID, "旧目标的盖章理由", []string{f.ID})
	if err != nil {
		t.Fatal(err)
	}
	return pinned
}

// The operator appends a goal while the project sits in completion stand-by:
// the old receipt must survive untouched, the new goal is unmet, the project is
// resumed and the commander is woken exactly once. A duplicate request is a
// no-op that returns the same goal.
func TestAppendGoalAndContinueResumesCompletionStandby(t *testing.T) {
	s, p := newBindingServer(t)
	dec, commander := startAppendGoalDecider(t, s.Store)
	s.Decider = dec
	pinned := seedStampedGoal(t, s.Store)
	s.AutoPause("目标已完成：测试自动暂停")
	if !s.paused.Load() || !p.IsPaused() || !dec.Paused() {
		t.Fatal("fixture is not in completion stand-by")
	}

	body := map[string]any{
		"statement":       "追查新的外网入口",
		"start":           "上一目标已收口：现有门户入口已确认",
		"idempotency_key": "append-standby-1",
		"note":            "客户追加范围",
	}
	rec := call(t, s, http.MethodPost, "/api/project/append-goal", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("append = %d: %s", rec.Code, rec.Body.String())
	}
	out := decodeAppendGoal(t, rec)
	if !out.Created || !out.Resumed || !out.DeciderWoken {
		t.Fatalf("outcome = %+v, want created+resumed+woken", out)
	}
	if out.ResumedFrom != pauseKindCompletionStandby || out.PauseKindBefore != pauseKindCompletionStandby ||
		out.PauseKindAfter != pauseKindRunning || out.PausedAfter {
		t.Fatalf("pause kinds = before %q / after %q (resumed_from %q)", out.PauseKindBefore, out.PauseKindAfter, out.ResumedFrom)
	}
	if out.Goal.ID == "" || out.Goal.Met || out.Goal.Statement != "追查新的外网入口" ||
		out.Goal.Start != "上一目标已收口：现有门户入口已确认" || out.Goal.IdempotencyKey != "append-standby-1" {
		t.Fatalf("new goal = %+v", out.Goal)
	}
	if s.paused.Load() || dec.Paused() || p.IsPaused() {
		t.Fatal("append-and-continue did not clear the persisted and runtime pause")
	}

	// The old stamp is history: met/evidence/note/from/versions/completed_at are
	// exactly what they were, not rewritten by the append.
	goals := s.Store.Goals()
	if len(goals) != 2 {
		t.Fatalf("goals = %d, want 2", len(goals))
	}
	var old model.Goal
	for _, g := range goals {
		if g.ID == pinned.ID {
			old = g
		}
	}
	if old.ID == "" {
		t.Fatalf("old goal %s disappeared", pinned.ID)
	}
	if !old.Met || old.EvidenceID != pinned.EvidenceID || old.Note != pinned.Note ||
		!equalStrings(old.From, pinned.From) ||
		!equalVersions(old.FromClaimVersions, pinned.FromClaimVersions) ||
		old.CompletedAt == nil || !old.CompletedAt.Equal(*pinned.CompletedAt) ||
		old.ReceiptStale || len(old.ReceiptChanged) != 0 {
		t.Fatalf("old receipt changed: before=%+v after=%+v", pinned, old)
	}

	waitFor(t, 5*time.Second, func() bool { return commander.count() == 1 })

	// A retried request (the response may have been lost) finds the same goal and
	// does not append a second one or wake a second round.
	rec = call(t, s, http.MethodPost, "/api/project/append-goal", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("duplicate = %d: %s", rec.Code, rec.Body.String())
	}
	dup := decodeAppendGoal(t, rec)
	if dup.Created || dup.Resumed || dup.DeciderWoken {
		t.Fatalf("duplicate outcome = %+v, want created=false resumed=false woken=false", dup)
	}
	if dup.Goal.ID != out.Goal.ID {
		t.Fatalf("duplicate goal id = %s, want %s", dup.Goal.ID, out.Goal.ID)
	}
	if dup.PauseKindBefore != pauseKindRunning || dup.PauseKindAfter != pauseKindRunning {
		t.Fatalf("duplicate pause kinds = %q/%q, want running/running", dup.PauseKindBefore, dup.PauseKindAfter)
	}
	if got := len(s.Store.Goals()); got != 2 {
		t.Fatalf("duplicate appended a goal: %d goals", got)
	}
	time.Sleep(250 * time.Millisecond)
	if got := commander.count(); got != 1 {
		t.Fatalf("commander rounds = %d after a duplicate request, want 1", got)
	}

	// The same key with different content is a contract error, not a silent
	// no-op that hides the caller's mistake.
	changed := map[string]any{"statement": "另一个目标", "idempotency_key": "append-standby-1"}
	rec = call(t, s, http.MethodPost, "/api/project/append-goal", changed)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "goal_idempotency_conflict") {
		t.Fatalf("changed payload = %d: %s", rec.Code, rec.Body.String())
	}
}

// A credential fault is not ours to clear: the request is refused with a machine
// code before anything is written, so the operator fixes the key/profile and
// retries without a duplicate goal.
func TestAppendGoalAndContinueRefusesProviderFaultWithoutSideEffect(t *testing.T) {
	s, p := newBindingServer(t)
	dec, commander := startAppendGoalDecider(t, s.Store)
	s.Decider = dec
	s.SetPaused(true)
	dec.PauseFor("模型额度/key 不可用（auth：profile=primary，no fallback configured）：换 key / profile 后手动继续")
	if p.IsPaused() {
		t.Fatal("fixture unexpectedly persisted the fault pause")
	}

	rec := call(t, s, http.MethodPost, "/api/project/append-goal", map[string]any{
		"statement": "故障期间的目标", "idempotency_key": "append-fault-1",
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("fault-paused append = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "project_fault_paused") {
		t.Fatalf("refusal body = %s, want code project_fault_paused", rec.Body.String())
	}
	if got := len(s.Store.Goals()); got != 0 {
		t.Fatalf("refused request wrote %d goals", got)
	}
	if !s.paused.Load() || !dec.Paused() {
		t.Fatal("refused request cleared the fault pause")
	}
	time.Sleep(100 * time.Millisecond)
	if got := commander.count(); got != 0 {
		t.Fatalf("refused request woke the commander %d times", got)
	}
}

// A manual operator pause is resumable by this explicit action, and the response
// says so instead of silently flipping the switch.
func TestAppendGoalAndContinueResumesManualPauseWithAcknowledgment(t *testing.T) {
	s, p := newBindingServer(t)
	dec, commander := startAppendGoalDecider(t, s.Store)
	s.Decider = dec
	if rec := call(t, s, http.MethodPost, "/api/project/pause", nil); rec.Code != http.StatusOK {
		t.Fatalf("pause = %d: %s", rec.Code, rec.Body.String())
	}
	if !p.IsPaused() {
		t.Fatal("manual pause was not persisted")
	}

	rec := call(t, s, http.MethodPost, "/api/project/append-goal", map[string]any{
		"statement": "人工暂停后追加的目标", "idempotency_key": "append-manual-1",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("append = %d: %s", rec.Code, rec.Body.String())
	}
	out := decodeAppendGoal(t, rec)
	if !out.Resumed || out.ResumedFrom != pauseKindManualPause ||
		out.PauseKindBefore != pauseKindManualPause || out.PauseKindAfter != pauseKindRunning {
		t.Fatalf("outcome = %+v, want an explicit manual-pause resume", out)
	}
	if !strings.Contains(out.Note, "人工暂停") || !strings.Contains(out.Note, "显式") {
		t.Fatalf("resume note = %q, want it to acknowledge the manual resume", out.Note)
	}
	if s.paused.Load() || dec.Paused() || p.IsPaused() {
		t.Fatal("manual pause was not cleared")
	}
	waitFor(t, 5*time.Second, func() bool { return commander.count() == 1 })
}

// If the goal was written but the manifest's resume write failed, the runtime
// must stay paused (agreement with the persisted flag) and the failure must be
// reported as retryable; a retry with the same key finds the goal and completes
// the resume without duplicating it.
func TestAppendGoalAndContinueRetryCompletesResumeAfterManifestFailure(t *testing.T) {
	s, p := newBindingServer(t)
	dec, commander := startAppendGoalDecider(t, s.Store)
	s.Decider = dec
	s.AutoPause("目标已完成：测试自动暂停")
	if !p.IsPaused() {
		t.Fatal("completion stand-by was not persisted")
	}
	s.persistPaused = func(bool) error { return errors.New("disk full (injected)") }

	body := map[string]any{"statement": "恢复失败后重试的目标", "idempotency_key": "append-retry-1"}
	rec := call(t, s, http.MethodPost, "/api/project/append-goal", body)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "resume_failed") {
		t.Fatalf("failed resume = %d: %s", rec.Code, rec.Body.String())
	}
	var failed struct {
		Goal            model.Goal `json:"goal"`
		Created         bool       `json:"created"`
		Resumed         bool       `json:"resumed"`
		Retryable       bool       `json:"retryable"`
		PauseKindBefore string     `json:"pause_kind_before"`
		PauseKindAfter  string     `json:"pause_kind_after"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &failed); err != nil {
		t.Fatal(err)
	}
	if !failed.Created || failed.Resumed || !failed.Retryable || failed.Goal.ID == "" {
		t.Fatalf("failed-resume body = %+v, want created+retryable and no resume", failed)
	}
	if failed.PauseKindBefore != pauseKindCompletionStandby || failed.PauseKindAfter != pauseKindCompletionStandby {
		t.Fatalf("pause kinds around failed resume = %q/%q", failed.PauseKindBefore, failed.PauseKindAfter)
	}
	// Durable flag and runtime agree: both still paused. The goal itself is
	// durable, so the retry can find it.
	if !s.paused.Load() || !dec.Paused() || !p.IsPaused() {
		t.Fatal("failed resume left runtime and manifest disagreeing")
	}
	if got := len(s.Store.Goals()); got != 1 {
		t.Fatalf("failed resume wrote %d goals, want the one created goal", got)
	}

	s.persistPaused = nil
	rec = call(t, s, http.MethodPost, "/api/project/append-goal", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("retry = %d: %s", rec.Code, rec.Body.String())
	}
	out := decodeAppendGoal(t, rec)
	if out.Created || !out.Resumed || out.Goal.ID != failed.Goal.ID {
		t.Fatalf("retry outcome = %+v, want created=false resumed=true goal=%s", out, failed.Goal.ID)
	}
	if got := len(s.Store.Goals()); got != 1 {
		t.Fatalf("retry duplicated the goal: %d goals", got)
	}
	if s.paused.Load() || dec.Paused() || p.IsPaused() {
		t.Fatal("retry did not complete the resume")
	}
	if s.pauseCause != "" {
		t.Fatalf("resume left a stale pause cause %q", s.pauseCause)
	}
	waitFor(t, 5*time.Second, func() bool { return commander.count() == 1 })
	time.Sleep(150 * time.Millisecond)
	if got := commander.count(); got != 1 {
		t.Fatalf("commander rounds = %d, want exactly one after the retry", got)
	}
}

// Appending while the project is already running must add the goal and leave the
// live run alone.
func TestAppendGoalAndContinueAddsGoalWhileRunningWithoutTouchingRuns(t *testing.T) {
	s, p := newBindingServer(t)
	ex := &blockingExec{}
	s.Exec = ex
	s.Timeout = 0
	in, err := s.Store.CreateIntent(model.Intent{Title: "正在跑的方向", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartRun(in.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		cur, ok := s.Store.GetIntent(in.ID)
		return ok && cur.Status == model.StatusRunning
	})
	waitFor(t, 5*time.Second, func() bool { return len(ex.requests()) == 1 })
	t.Cleanup(func() {
		s.CancelAll()
		s.WaitIdle(5 * time.Second)
	})
	cur, _ := s.Store.GetIntent(in.ID)
	runID := cur.RunID

	rec := call(t, s, http.MethodPost, "/api/project/append-goal", map[string]any{
		"statement": "运行中追加的目标", "idempotency_key": "append-running-1",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("append = %d: %s", rec.Code, rec.Body.String())
	}
	out := decodeAppendGoal(t, rec)
	if !out.Created || out.Resumed || out.PausedBefore || out.PausedAfter {
		t.Fatalf("outcome = %+v, want created without touching the pause", out)
	}
	if out.PauseKindBefore != pauseKindRunning || out.PauseKindAfter != pauseKindRunning {
		t.Fatalf("pause kinds = %q/%q, want running/running", out.PauseKindBefore, out.PauseKindAfter)
	}
	if s.paused.Load() || p.IsPaused() {
		t.Fatal("appending while running changed the pause state")
	}
	live, ok := s.Store.GetRun(runID)
	if !ok || live.Status != model.RunRunning {
		t.Fatalf("live run %s is %+v, want still running", runID, live)
	}
	// One executor call: the append did not dispatch or cancel anything.
	if got := len(ex.requests()); got != 1 {
		t.Fatalf("executor calls = %d, want the original run only", got)
	}
}

// The participant mux is a different authenticated surface (per-run tokens under
// /agent/); this operator action must not exist there at all, so a probe gets 404
// rather than a gate refusal.
func TestAppendGoalRouteIsNotOnTheParticipantSurface(t *testing.T) {
	s, _ := newBindingServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/project/append-goal",
		strings.NewReader(`{"statement":"越权追加","idempotency_key":"surface-1"}`))
	rec := httptest.NewRecorder()
	s.AgentHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("participant surface append = %d, want 404", rec.Code)
	}
	if got := len(s.Store.Goals()); got != 0 {
		t.Fatalf("participant surface append wrote %d goals", got)
	}
	// The operator mux does serve it.
	if rec := call(t, s, http.MethodPost, "/api/project/append-goal", map[string]any{
		"statement": "操作员追加", "idempotency_key": "surface-2",
	}); rec.Code != http.StatusOK {
		t.Fatalf("operator append = %d: %s", rec.Code, rec.Body.String())
	}
}

// A direction yielding to a human is live work, not a park: appending is legal
// and must not resume/cancel/rewrite the waiting direction.
func TestAppendGoalAndContinueLeavesWaitingHumanAlone(t *testing.T) {
	s, p := newBindingServer(t)
	in, err := s.Store.CreateIntent(model.Intent{Title: "等操作员确认", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Status = model.StatusWaiting
		i.Wait = &model.Wait{Operator: &model.OperatorWait{ID: "human-request", Request: "确认访问"}}
	}); err != nil {
		t.Fatal(err)
	}
	rec := call(t, s, http.MethodPost, "/api/project/append-goal", map[string]any{
		"statement": "等待期间追加的目标", "idempotency_key": "append-waiting-1",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("append = %d: %s", rec.Code, rec.Body.String())
	}
	out := decodeAppendGoal(t, rec)
	if !out.Created || out.Resumed || out.PausedBefore ||
		out.PauseKindBefore != pauseKindWaitingHuman || out.PauseKindAfter != pauseKindWaitingHuman {
		t.Fatalf("outcome = %+v, want created in waiting_human without a resume", out)
	}
	got, ok := s.Store.GetIntent(in.ID)
	if !ok || got.Status != model.StatusWaiting {
		t.Fatalf("waiting direction changed to %+v", got)
	}
	if s.paused.Load() || p.IsPaused() {
		t.Fatal("appending while waiting changed the pause state")
	}
}

// A request without the idempotency key is refused: an append-and-continue whose
// retry cannot be matched would risk a duplicate goal on every lost response.
func TestAppendGoalAndContinueRequiresIdempotencyKey(t *testing.T) {
	s, _ := newBindingServer(t)
	rec := call(t, s, http.MethodPost, "/api/project/append-goal", map[string]any{"statement": "没有 key"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "idempotency_key") {
		t.Fatalf("missing key = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := call(t, s, http.MethodPost, "/api/project/append-goal", map[string]any{
		"statement": "", "idempotency_key": "empty-statement",
	}); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty statement = %d: %s", rec.Code, rec.Body.String())
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalVersions(a, b map[string]uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
