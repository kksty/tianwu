package api

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// A direction derived from a fact a review rejected is dead work: dispatch must
// refuse it instead of spending a run on a premise nobody stands behind (D52).
func TestDispatchRefusesARejectedPremise(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	f, err := st.AddFact(model.Fact{Kind: "vuln", Title: "被判不成立"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReviewFact(model.Review{FactID: f.ID, Verdict: "reject", Notes: "复现不了"}); err != nil {
		t.Fatal(err)
	}
	in, err := st.CreateIntent(model.Intent{Title: "沿着它继续", FromFactIDs: []string{f.ID}})
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, &handoffExec{}, dir, filepath.Join(dir, "cases"))
	if _, err := s.StartRun(in.ID); err == nil || !strings.Contains(err.Error(), "前提") {
		t.Fatalf("dispatch = %v, want a premise refusal", err)
	}
}

// A cancel is our decision: it stops the run without spending an attempt
// (D19/D52) and settles the direction as failed+Hold, so neither the pump nor
// dispatchPending immediately starts the same work again (task-31 / final-e2e
// E7). Retry is the explicit way to clear Hold and start a new run.
func TestCancelHoldsTheDirection(t *testing.T) {
	s, _, in := newSteerServer(t)
	// The retry below starts another blocking run; let it finish before the
	// temp dir is removed, or Windows refuses to delete the open run log.
	t.Cleanup(func() {
		s.CancelAll()
		s.WaitIdle(5 * time.Second)
		// WaitIdle drops the cancel entry just before dispatch closes the run
		// log; Windows needs the handle gone before TempDir's RemoveAll.
		time.Sleep(250 * time.Millisecond)
	})
	prevRun := in.RunID
	if _, err := s.CancelIntent(in.ID, "这条不走了"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		got, ok := s.Store.GetIntent(in.ID)
		return ok && got.Status == model.StatusFailed && got.Hold
	})
	if prevRun != "" {
		run, ok := s.Store.GetRun(prevRun)
		if !ok || run.Status != model.RunCanceled || run.CancelCause != model.CancelCauseStopLoss {
			t.Fatalf("stop-loss run = %+v", run)
		}
	}
	// An explicit retry is what clears Hold and starts a new run.
	runID, err := s.RetryIntent(in.ID, "换入口再试", false)
	if err != nil || runID == "" {
		t.Fatalf("retry = %q, %v", runID, err)
	}
	got, _ := s.Store.GetIntent(in.ID)
	if got.Hold {
		t.Fatal("retry did not clear Hold")
	}
	if got.Status != model.StatusRunning && got.Status != model.StatusClaimed {
		t.Fatalf("retry status = %s", got.Status)
	}
}

func TestParkRefusesALiveRun(t *testing.T) {
	s, _, in := newSteerServer(t)
	if _, err := s.ParkIntent(in.ID, "先停一下"); err == nil {
		t.Fatal("parking a running intent would be overwritten by finish()")
	}
}

func TestReopenRefusesALiveRun(t *testing.T) {
	s, _, in := newSteerServer(t)
	if _, err := s.Store.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Status = model.StatusParked
		i.ParkReason = "测试"
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReopenIntent(in.ID); err == nil {
		t.Fatal("reopening while the old run is live would allow a second run")
	}
}

// The retry brief must carry the previous failure; otherwise the new run walks
// the same dead end with no idea why it died last time.
func TestRetryBriefCarriesTheLastFailure(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	ex := &blockingExec{}
	s := New(st, ex, dir, filepath.Join(dir, "cases"))
	in, _ := st.CreateIntent(model.Intent{Title: "失败过", Description: "d"})
	prev, _ := st.AddRun(model.Run{IntentID: in.ID, Node: "local", Status: model.RunFailed, StopReason: "provider_auth"})
	if _, err := st.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Status = model.StatusFailed
		i.RunID = prev.ID
		i.Attempts = 1
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.CancelAll()
		s.WaitIdle(5 * time.Second)
		// The cancel entry disappears just before dispatch closes the ledger and
		// the run log; Windows refuses RemoveAll while those handles are open.
		time.Sleep(150 * time.Millisecond)
	})

	if _, err := s.RetryIntent(in.ID, "换一个 provider 再跑", false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool { return len(ex.requests()) > 0 })
	prompt := ex.requests()[0].Prompt
	for _, want := range []string{"## 本意图历史", "provider_auth", "第 2 次派发"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("retry prompt missing %q:\n%s", want, prompt)
		}
	}
	// The retry note explains the re-dispatch to the operator; it is not an
	// executor constraint, so it must stay out of both Doc and the brief.
	got, _ := st.GetIntent(in.ID)
	if len(got.Notes) != 1 || got.Notes[0].Kind != model.IntentNoteRetry || got.Notes[0].Text != "换一个 provider 再跑" {
		t.Fatalf("retry note not recorded on the intent: %+v", got.Notes)
	}
	if got.Doc != "" {
		t.Fatalf("retry note leaked into Doc: %q", got.Doc)
	}
	if strings.Contains(prompt, "换一个 provider 再跑") {
		t.Fatalf("retry note leaked into the executor brief:\n%s", prompt)
	}
}

// liveFactExec writes one declared fact and then blocks, standing in for a run
// that is still working after it has already recorded a finding.
type liveFactExec struct {
	wrote chan struct{}
}

func (l *liveFactExec) Name() string { return "live" }

func (l *liveFactExec) Run(ctx context.Context, req executor.Request, emit func(executor.Event)) (executor.Result, error) {
	if err := os.MkdirAll(filepath.Join(req.WorkDir, ".tw"), 0o755); err != nil {
		return executor.Result{ExitCode: -1}, err
	}
	line := `{"kind":"vuln","title":"实时事实","summary":"还没结束就写下的结论"}` + "\n"
	if err := os.WriteFile(filepath.Join(req.WorkDir, ".tw", "facts.jsonl"), []byte(line), 0o644); err != nil {
		return executor.Result{ExitCode: -1}, err
	}
	select {
	case l.wrote <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return executor.Result{ExitCode: -1, StopReason: "canceled"}, ctx.Err()
}

// A run's declared fact must reach the ledger before the run ends, and the
// finish path must update that row rather than add a second one.
func TestLiveFactsReachTheLedgerBeforeFinish(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	ex := &liveFactExec{wrote: make(chan struct{}, 1)}
	s := New(st, ex, dir, filepath.Join(dir, "cases"))
	in, _ := st.CreateIntent(model.Intent{Title: "边做边记"})
	if _, err := s.StartRun(in.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ex.wrote:
	case <-time.After(5 * time.Second):
		t.Fatal("executor did not write the fact")
	}
	waitFor(t, 5*time.Second, func() bool {
		for _, f := range st.ListFacts(in.ID, 20) {
			if f.Title == "实时事实" {
				return true
			}
		}
		return false
	})
	s.CancelAll()
	s.WaitIdle(5 * time.Second)
	time.Sleep(200 * time.Millisecond)
	if facts := st.ListFacts(in.ID, 20); len(facts) != 1 {
		t.Fatalf("live fact was duplicated at finish: %+v", facts)
	}
}

func TestPurposeRunDoesNotCountTowardMaxRuns(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddRun(model.Run{Node: "local", Status: model.RunRunning, Purpose: "verifier"}); err != nil {
		t.Fatal(err)
	}
	ex := &blockingExec{}
	s := New(st, ex, dir, filepath.Join(dir, "cases"))
	s.MaxRuns = 1
	defer func() {
		s.CancelAll()
		s.WaitIdle(5 * time.Second)
		time.Sleep(250 * time.Millisecond)
	}()
	in, _ := st.CreateIntent(model.Intent{Title: "攻击方向"})
	if _, err := s.StartRun(in.ID); err != nil {
		t.Fatalf("a verifier/report run must not consume an intent slot: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool { return len(ex.requests()) == 1 })
}

func TestMaxRunsCapsConcurrentDispatch(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	ex := &blockingExec{}
	s := New(st, ex, dir, filepath.Join(dir, "cases"))
	s.MaxRuns = 1
	defer func() {
		s.CancelAll()
		s.WaitIdle(5 * time.Second)
		time.Sleep(100 * time.Millisecond)
	}()

	first, _ := st.CreateIntent(model.Intent{Title: "第一个"})
	second, _ := st.CreateIntent(model.Intent{Title: "第二个"})
	if _, err := s.StartRun(first.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool { return len(ex.requests()) == 1 })
	if _, err := s.StartRun(second.ID); err == nil || !strings.Contains(err.Error(), "并发") {
		t.Fatalf("second dispatch = %v, want a concurrency refusal", err)
	}
}

func TestIdleRunIsCanceledAndHeld(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	ex := &blockingExec{}
	s := New(st, ex, dir, filepath.Join(dir, "cases"))
	s.IdleTimeout = 50 * time.Millisecond
	defer func() {
		s.CancelAll()
		s.WaitIdle(5 * time.Second)
		time.Sleep(100 * time.Millisecond)
	}()

	in, _ := st.CreateIntent(model.Intent{Title: "会卡住"})
	if _, err := s.StartRun(in.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool { return len(ex.requests()) == 1 })
	time.Sleep(80 * time.Millisecond)
	s.reapIdle()
	waitFor(t, 5*time.Second, func() bool {
		got, ok := st.GetIntent(in.ID)
		return ok && got.Status == model.StatusPending && got.Hold
	})
	run, _ := st.GetIntent(in.ID)
	last, ok := st.GetRun(run.RunID)
	if !ok || !strings.Contains(last.ExitReason, "静默") {
		t.Fatalf("idle reason not recorded: %+v", last)
	}
}

func TestReapClaimsReturnsLostIntentToPending(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, &blockingExec{}, dir, filepath.Join(dir, "cases"))
	in, _ := st.CreateIntent(model.Intent{Title: "认领后丢了"})
	if _, err := st.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Status = model.StatusClaimed
		i.ClaimedBy = "local"
		i.UpdatedAt = time.Now().Add(-5 * time.Minute)
	}); err != nil {
		t.Fatal(err)
	}
	s.reapClaims()
	got, _ := st.GetIntent(in.ID)
	if got.Status != model.StatusPending {
		t.Fatalf("claimed intent = %s, want pending", got.Status)
	}
}

func TestCanceledRunDoesNotSpendAnAttempt(t *testing.T) {
	s, _, in := newSteerServer(t)
	if got, _ := s.Store.GetIntent(in.ID); got.Attempts != 1 {
		t.Fatalf("attempts after claim = %d, want 1", got.Attempts)
	}
	live, _ := s.Store.GetIntent(in.ID)
	if err := s.CancelRun(live.RunID); err != nil {
		t.Fatal(err)
	}
	// A run-level cancel is an explicit stop too: failed+Hold, so the pump does
	// not immediately re-dispatch it (task-31 / final-e2e E7).
	waitFor(t, 5*time.Second, func() bool {
		got, ok := s.Store.GetIntent(in.ID)
		return ok && got.Status == model.StatusFailed && got.Hold
	})
	if got, _ := s.Store.GetIntent(in.ID); got.Attempts != 0 {
		t.Fatalf("attempts = %d, want 0 (a cancel must not climb the ladder)", got.Attempts)
	}
}
