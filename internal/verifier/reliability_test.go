package verifier

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// gatedExec lets a test decide exactly when a verifier run ends: it can write a
// verdict immediately, block until canceled, or mutate the fact mid-run. It is
// the fixture for the "a receipt only counts for the run that produced it"
// contract (F1).
type gatedExec struct {
	started chan struct{}
	once    sync.Once
	block   chan struct{}
	body    string
	result  executor.Result
	hook    func(executor.Request)
}

func (e *gatedExec) Name() string { return "gated" }

func (e *gatedExec) Run(ctx context.Context, req executor.Request, emit func(executor.Event)) (executor.Result, error) {
	if e.hook != nil {
		e.hook(req)
	}
	if e.body != "" {
		if err := os.WriteFile(filepath.Join(req.WorkDir, VerdictPath), []byte(e.body), 0o600); err != nil {
			return executor.Result{ExitCode: -1}, err
		}
	}
	if e.started != nil {
		e.once.Do(func() { close(e.started) })
	}
	if e.block != nil {
		select {
		case <-e.block:
		case <-ctx.Done():
			return executor.Result{ExitCode: -1, StopReason: "canceled"}, ctx.Err()
		}
	}
	return e.result, nil
}

// A timed-out review is a failed run, not a replay of an earlier pass. The old
// review.json sits in a different run directory, and even if it did not, the
// failed run's terminal state must not supply a verdict.
func TestTimedOutReviewCannotReuseAnEarlierPass(t *testing.T) {
	st, f, root := setup(t)
	pass := &stubExec{body: `{"verdict":"pass","notes":"original claim"}`}
	if _, err := New(st, pass, root).Run(context.Background(), f.ID); err != nil {
		t.Fatal(err)
	}
	firstDir := pass.lastReq.WorkDir
	// The claim changes: the old pass no longer speaks for the current row.
	if _, err := st.UpdateFact(f.ID, func(f *model.Fact) { f.Summary = "changed claim" }); err != nil {
		t.Fatal(err)
	}
	ex := &gatedExec{block: make(chan struct{})} // never released; the timeout wins
	v := New(st, ex, root)
	v.Timeout = 20 * time.Millisecond
	review, err := v.Run(context.Background(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if review.Verdict == "pass" {
		t.Fatalf("timed-out review produced a pass: %+v", review)
	}
	if review.ClaimVersion != 2 {
		t.Fatalf("review bound to v%d, want the current v2", review.ClaimVersion)
	}
	got, _ := st.FactByID(f.ID)
	if got.Status != "unverified" {
		t.Fatalf("timed-out review changed the fact to %q", got.Status)
	}
	run, ok := st.GetRun(review.VerifierRun)
	if !ok || run.Status != model.RunFailed {
		t.Fatalf("timed-out review run = %+v", run)
	}
	// The new receipt directory is per-run: it cannot have consumed the old file.
	newDir := filepath.Join(root, f.IntentID, ".verify", f.ID, run.ID)
	if newDir == firstDir {
		t.Fatalf("review reused the old run directory: %q", newDir)
	}
	if _, err := os.Stat(filepath.Join(newDir, VerdictPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("timed-out run left a consumable receipt: %v", err)
	}
}

// When the control plane cancels a review, it is settled as canceled and writes
// no verdict at all: "we stopped it" must not become "the finding failed".
func TestCanceledReviewWritesNoVerdict(t *testing.T) {
	st, f, root := setup(t)
	release := make(chan struct{})
	ex := &gatedExec{block: release, started: make(chan struct{})}
	go func() {
		<-ex.started
		close(release)
	}()
	v := New(st, ex, root)
	v.Track = func(string, context.CancelFunc) func() string {
		return func() string { return model.CancelCauseReviewPause }
	}
	if _, err := v.Run(context.Background(), f.ID); err == nil {
		t.Fatal("a canceled review reported success")
	}
	if reviews := st.Reviews(f.ID); len(reviews) != 0 {
		t.Fatalf("canceled review wrote %d verdict(s): %+v", len(reviews), reviews)
	}
	got, _ := st.FactByID(f.ID)
	if got.Status != "unverified" {
		t.Fatalf("canceled review changed the fact to %q", got.Status)
	}
	var verifierRun model.Run
	for _, r := range st.Runs() {
		if r.Purpose == "verifier" {
			verifierRun = r
		}
	}
	if verifierRun.Status != model.RunCanceled || verifierRun.CancelCause != model.CancelCauseReviewPause {
		t.Fatalf("canceled review run = %+v", verifierRun)
	}
}

// A correction during the review is a new assertion. The verifier carries the
// version it started with, so the store refuses the stale verdict instead of
// stamping the new content with the old proof (F1/F8).
func TestClaimCorrectedDuringReviewRejectsTheVerdict(t *testing.T) {
	st, f, root := setup(t)
	ex := &gatedExec{
		body: `{"verdict":"pass","notes":"proved the old row"}`,
		hook: func(executor.Request) {
			if _, err := st.UpdateFact(f.ID, func(fact *model.Fact) {
				fact.Summary = "corrected while the verifier ran"
			}); err != nil {
				t.Errorf("update fact: %v", err)
			}
		},
	}
	_, err := New(st, ex, root).Run(context.Background(), f.ID)
	if !errors.Is(err, store.ErrReviewConflict) {
		t.Fatalf("verdict for a changed claim = %v, want ErrReviewConflict", err)
	}
	if reviews := st.Reviews(f.ID); len(reviews) != 0 {
		t.Fatalf("stale verdict was stored: %+v", reviews)
	}
	got, _ := st.FactByID(f.ID)
	if got.Status != "unverified" || got.Summary != "corrected while the verifier ran" {
		t.Fatalf("fact after rejected verdict = %+v", got)
	}
}

// The error terminal state must not be read as a pass even when the run did
// write a pass file before it failed. This is the "written then failed" case.
func TestErrorTerminalAfterWritingPassIsNotConsumed(t *testing.T) {
	st, f, root := setup(t)
	ex := &failedReceiptExec{result: executor.Result{IsError: true, StopReason: "error"}}
	review, err := New(st, ex, root).Run(context.Background(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if review.Verdict == "pass" {
		t.Fatal("a failed run's leftover pass file was consumed")
	}
	if got, _ := st.FactByID(f.ID); got.Status != "unverified" {
		t.Fatalf("failed review changed the fact to %q", got.Status)
	}
	run, _ := st.GetRun(review.VerifierRun)
	if run.Status != model.RunFailed {
		t.Fatalf("run status = %q, want failed", run.Status)
	}
	if !strings.Contains(review.Notes, "没有给出可用结论") {
		t.Fatalf("review notes do not explain the refusal: %q", review.Notes)
	}
}

// The persistence side must agree: a stale-version verdict is refused, and the
// current claim stays unverified rather than silently inheriting the proof.
func TestStaleReviewVersionIsRefusedAtTheStore(t *testing.T) {
	st, f, root := setup(t)
	_ = root
	if _, err := st.UpdateFact(f.ID, func(f *model.Fact) { f.Summary = "current" }); err != nil {
		t.Fatal(err)
	}
	stale := model.Review{FactID: f.ID, Verdict: "pass", ClaimVersion: f.ClaimVersion, ClaimHash: f.ClaimHash}
	if _, err := st.UpdateFact(f.ID, func(f *model.Fact) { f.Summary = "moved on" }); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReviewFact(stale); !errors.Is(err, store.ErrReviewConflict) {
		t.Fatalf("stale verdict = %v, want ErrReviewConflict", err)
	}
	if got, _ := st.FactByID(f.ID); got.Status != "unverified" {
		t.Fatalf("stale verdict changed the fact: %+v", got)
	}
}
