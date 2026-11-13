package api

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
	"github.com/kksty/tianwu/internal/verifier"
)

// F11: the automatic review pass walks an enumerable queue with a durable
// cursor, not "the newest 50 facts". These tests cover reachability, the batch
// bound, restart resumption, Busy/pause skips and the failure cooldown.

// reviewProbeExec is a verifier executor without a model. It records which
// finding each run was for (the prompt's 标题 line), can fail on one title and
// can block the first run so a test can pause mid-batch.
type reviewProbeExec struct {
	mu         sync.Mutex
	titles     []string
	failTitle  string
	blockFirst bool
	entered    chan struct{}
	release    chan struct{}
}

func (e *reviewProbeExec) Name() string { return "review-probe" }

func (e *reviewProbeExec) Run(ctx context.Context, req executor.Request, emit func(executor.Event)) (executor.Result, error) {
	title := promptFactTitle(req.Prompt)
	e.mu.Lock()
	index := len(e.titles)
	e.titles = append(e.titles, title)
	block := e.blockFirst && index == 0
	e.mu.Unlock()
	if block {
		if e.entered != nil {
			select {
			case e.entered <- struct{}{}:
			default:
			}
		}
		if e.release != nil {
			select {
			case <-e.release:
			case <-ctx.Done():
				return executor.Result{ExitCode: 1}, ctx.Err()
			}
		}
	}
	if title == e.failTitle {
		return executor.Result{ExitCode: 1, IsError: true, StopReason: "error", Output: "fixture review failure"}, nil
	}
	if err := os.WriteFile(filepath.Join(req.WorkDir, verifier.VerdictPath), []byte(`{"verdict":"pass","notes":"重跑一致","proof":"p"}`), 0o644); err != nil {
		return executor.Result{ExitCode: 1}, err
	}
	return executor.Result{ExitCode: 0}, nil
}

func (e *reviewProbeExec) runs() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.titles)
}

func (e *reviewProbeExec) runsFor(title string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, got := range e.titles {
		if got == title {
			n++
		}
	}
	return n
}

func (e *reviewProbeExec) distinctTitles() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	seen := map[string]bool{}
	for _, got := range e.titles {
		seen[got] = true
	}
	return len(seen)
}

func promptFactTitle(prompt string) string {
	for _, line := range strings.Split(prompt, "\n") {
		if strings.HasPrefix(line, "标题：") {
			return strings.TrimSpace(strings.TrimPrefix(line, "标题："))
		}
	}
	return ""
}

func newAutoVerifyServer(t *testing.T, ex executor.Executor) (*Server, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	cases := filepath.Join(dir, "cases")
	if err := os.MkdirAll(cases, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := New(st, ex, dir, cases)
	s.AutoVerify = true
	s.Verifier = verifier.New(st, ex, cases)
	return s, st
}

func addReviewableFact(t *testing.T, st *store.Store, intentID, kind, title string) model.Fact {
	t.Helper()
	f, err := st.AddFact(model.Fact{IntentID: intentID, Kind: kind, Title: title, Origin: model.OriginDeclared})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func verifiedFindings(t *testing.T, st *store.Store, intentID string) int {
	t.Helper()
	n := 0
	for _, f := range st.ListFacts(intentID, 0) {
		if f.Status == "verified" {
			n++
		}
	}
	return n
}

// More than 50 observations in one direction must not push an old finding out
// of the automatic pass: the newest-50 window was the starvation bug.
func TestAutoVerifyReachesFindingsBeyondTheNewestFifty(t *testing.T) {
	ex := &reviewProbeExec{}
	s, st := newAutoVerifyServer(t, ex)
	in, err := st.CreateIntent(model.Intent{Title: "长方向"})
	if err != nil {
		t.Fatal(err)
	}
	old := addReviewableFact(t, st, in.ID, "vuln", "早期漏洞")
	// Backdate the old finding: ListFacts sorts by CreatedAt, so it is outside
	// the newest 50 and the old top-N loop could never reach it.
	if _, err := st.UpdateFact(old.ID, func(f *model.Fact) { f.CreatedAt = time.Now().Add(-2 * time.Hour) }); err != nil {
		t.Fatal(err)
	}
	const total = 56
	for i := 1; i < total; i++ {
		addReviewableFact(t, st, in.ID, "cred", fmt.Sprintf("结论-%02d", i))
	}
	for _, f := range st.ListFacts(in.ID, 50) {
		if f.ID == old.ID {
			t.Fatal("fixture: the old finding is still inside the newest 50")
		}
	}

	for i := 0; i < 20 && verifiedFindings(t, st, in.ID) < total; i++ {
		s.autoVerify(in.ID)
	}
	if got := verifiedFindings(t, st, in.ID); got != total {
		t.Fatalf("reviewed %d/%d findings; the old finding beyond the newest 50 was starved", got, total)
	}
	got, ok := st.FactByID(old.ID)
	if !ok || got.Status != "verified" {
		t.Fatalf("the early finding was not reviewed: %+v", got)
	}
	if ex.runs() != total || ex.distinctTitles() != total {
		t.Fatalf("executor runs = %d (distinct %d), want each finding exactly once", ex.runs(), ex.distinctTitles())
	}
}

// One pass starts at most autoVerifyBatch reviews, and the cursor is durable:
// after a restart the next pass continues instead of re-reviewing verified
// findings or restarting from the top.
func TestAutoVerifyIsBatchedAndResumesAfterRestart(t *testing.T) {
	dir := t.TempDir()
	cases := filepath.Join(dir, "cases")
	if err := os.MkdirAll(cases, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "ledger.json")
	ex := &reviewProbeExec{}
	const total = 12

	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, ex, dir, cases)
	s.AutoVerify = true
	s.Verifier = verifier.New(st, ex, cases)
	in, err := st.CreateIntent(model.Intent{Title: "重启方向"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < total; i++ {
		addReviewableFact(t, st, in.ID, "vuln", fmt.Sprintf("结论-%02d", i))
	}
	s.autoVerify(in.ID)
	if got := ex.runs(); got != autoVerifyBatch {
		t.Fatalf("first pass ran %d reviews, want the per-call bound %d", got, autoVerifyBatch)
	}
	if cursor := st.ReviewCursor(in.ID); cursor == "" {
		t.Fatal("the first pass did not persist its cursor")
	}
	if got := verifiedFindings(t, st, in.ID); got != autoVerifyBatch {
		t.Fatalf("first pass verified %d, want %d", got, autoVerifyBatch)
	}

	// Restart: the ledger is the only state carried over.
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	s2 := New(reopened, ex, dir, cases)
	s2.AutoVerify = true
	s2.Verifier = verifier.New(reopened, ex, cases)
	for i := 0; i < 4 && verifiedFindings(t, reopened, in.ID) < total; i++ {
		s2.autoVerify(in.ID)
	}
	if got := verifiedFindings(t, reopened, in.ID); got != total {
		t.Fatalf("after restart reviewed %d/%d", got, total)
	}
	if got := ex.runs(); got != total || ex.distinctTitles() != total {
		t.Fatalf("executor runs = %d (distinct %d), want each finding exactly once after restart", got, ex.distinctTitles())
	}
}

// A fact already being reviewed by another path is skipped rather than started
// twice; the rest of the batch still runs.
func TestAutoVerifySkipsBusyFindings(t *testing.T) {
	busyTitle := "正在复核的结论"
	ex := &reviewProbeExec{blockFirst: true, entered: make(chan struct{}, 1), release: make(chan struct{})}
	s, st := newAutoVerifyServer(t, ex)
	in, err := st.CreateIntent(model.Intent{Title: "忙方向"})
	if err != nil {
		t.Fatal(err)
	}
	busy := addReviewableFact(t, st, in.ID, "vuln", busyTitle)
	addReviewableFact(t, st, in.ID, "vuln", "另一个结论一")
	addReviewableFact(t, st, in.ID, "cred", "另一个结论二")
	if err := s.Verifier.Start(context.Background(), busy.ID, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ex.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("background review never started")
	}

	s.autoVerify(in.ID)
	if got := ex.runsFor(busyTitle); got != 1 {
		t.Fatalf("the busy finding was started %d times, want only the in-flight run", got)
	}
	if got := verifiedFindings(t, st, in.ID); got != 2 {
		t.Fatalf("the rest of the batch did not run: %d verified", got)
	}
	close(ex.release)
	waitFor(t, 5*time.Second, func() bool { return !s.Verifier.Busy(busy.ID) })
	if got, _ := st.FactByID(busy.ID); got.Status != "verified" {
		t.Fatalf("the in-flight review did not settle: %+v", got)
	}
}

// A review that ends without a usable verdict leaves the fact unverified. The
// queue must not buy the same failed run on every wake; a corrected assertion
// (new claim version) is retried immediately.
func TestAutoVerifyFailedReviewIsNotRetriedEveryWake(t *testing.T) {
	const badTitle = "复核总失败"
	ex := &reviewProbeExec{failTitle: badTitle}
	s, st := newAutoVerifyServer(t, ex)
	in, err := st.CreateIntent(model.Intent{Title: "失败方向"})
	if err != nil {
		t.Fatal(err)
	}
	bad := addReviewableFact(t, st, in.ID, "vuln", badTitle)
	addReviewableFact(t, st, in.ID, "cred", "能复核的结论")

	s.autoVerify(in.ID)
	if got := ex.runsFor(badTitle); got != 1 {
		t.Fatalf("first pass ran the failing review %d times", got)
	}
	if got, _ := st.FactByID(bad.ID); got.Status != "unverified" {
		t.Fatalf("a review without a usable verdict changed the status: %q", got.Status)
	}
	if wait, cooling := s.reviewCoolingDown(mustFact(t, st, bad.ID), time.Now()); !cooling || wait <= 0 {
		t.Fatal("the failed review did not start a retry cooldown")
	}

	// Repeated wakes in the cooldown window are free: no new model run.
	for i := 0; i < 3; i++ {
		s.autoVerify(in.ID)
	}
	if got := ex.runsFor(badTitle); got != 1 {
		t.Fatalf("the failing finding was retried %d times inside the cooldown", got)
	}

	// A correction is a new assertion version: it gets a fresh attempt now.
	if _, err := st.UpdateFact(bad.ID, func(f *model.Fact) { f.Summary = "更正后的断言" }); err != nil {
		t.Fatal(err)
	}
	s.autoVerify(in.ID)
	if got := ex.runsFor(badTitle); got != 2 {
		t.Fatalf("corrected assertion reviews = %d, want a fresh attempt", got)
	}
}

func mustFact(t *testing.T, st *store.Store, id string) model.Fact {
	t.Helper()
	f, ok := st.FactByID(id)
	if !ok {
		t.Fatalf("fact %s not found", id)
	}
	return f
}

// Pausing mid-batch must not consume the candidates that were not reviewed: the
// cursor stops before the first untouched candidate and a resume does them.
func TestAutoVerifyPauseLeavesUnprocessedCandidatesForLater(t *testing.T) {
	ex := &reviewProbeExec{blockFirst: true, entered: make(chan struct{}, 1), release: make(chan struct{})}
	s, st := newAutoVerifyServer(t, ex)
	in, err := st.CreateIntent(model.Intent{Title: "暂停方向"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		addReviewableFact(t, st, in.ID, "vuln", fmt.Sprintf("结论-%d", i))
	}
	done := make(chan struct{})
	go func() {
		s.autoVerify(in.ID)
		close(done)
	}()
	select {
	case <-ex.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the first review never started")
	}
	s.SetReviewsPaused(true)
	close(ex.release)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("autoVerify did not stop after the pause")
	}
	if got := verifiedFindings(t, st, in.ID); got != 1 {
		t.Fatalf("pause still processed %d findings, want only the in-flight one", got)
	}
	if !s.ReviewsPaused() {
		t.Fatal("review switch did not pause")
	}
	cursor := st.ReviewCursor(in.ID)
	if cursor == "" {
		t.Fatal("pause did not persist a resume cursor")
	}
	remaining, _ := st.ReviewCandidates(in.ID, cursor, 10)
	if len(remaining) != 2 {
		t.Fatalf("candidates after the paused cursor = %d, want the 2 untouched findings", len(remaining))
	}
	for _, f := range remaining {
		if f.Status == "verified" {
			t.Fatalf("cursor %q left an unprocessed candidate behind it: %+v", cursor, f)
		}
	}

	s.SetReviewsPaused(false)
	for i := 0; i < 5 && verifiedFindings(t, st, in.ID) < 3; i++ {
		s.autoVerify(in.ID)
	}
	if got := verifiedFindings(t, st, in.ID); got != 3 {
		t.Fatalf("after resume reviewed %d/3", got)
	}
	if got := ex.runs(); got != 3 {
		t.Fatalf("executor runs = %d, want each finding reviewed exactly once", got)
	}
}
