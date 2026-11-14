package api

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/verifier"
)

// A successful direction is held in reviewing until its new conclusions pass
// independent review. The test drives the phase directly so it can observe the
// intermediate state without a model turn.
func TestReviewAfterRunBlocksOnNewConclusions(t *testing.T) {
	s, p, source, ex := proofPolicyFixture(t)
	in, err := p.Store.CreateIntent(model.Intent{Title: "producer"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := p.Store.AddRun(model.Run{IntentID: in.ID, Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	fact, err := p.Store.AddFact(model.Fact{
		IntentID: in.ID, SourceRunID: run.ID, Kind: "vuln", Title: "new conclusion",
		Origin: model.OriginDeclared, EvidenceIDs: source.EvidenceIDs,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Store.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Status, i.RunID = model.StatusDone, run.ID
	}); err != nil {
		t.Fatal(err)
	}
	if !s.reviewAfterRun(in.ID, run.ID) {
		t.Fatal("review phase did not claim the completed direction")
	}
	waitFor(t, 5*time.Second, func() bool {
		cur, _ := p.Store.GetIntent(in.ID)
		return cur.Status == model.StatusDone || cur.Status == model.StatusReviewFailed
	})
	cur, _ := p.Store.GetIntent(in.ID)
	if cur.Status != model.StatusDone {
		t.Fatalf("direction = %s, want done", cur.Status)
	}
	got, _ := p.Store.FactByID(fact.ID)
	if got.Status != "verified" {
		t.Fatalf("new conclusion = %s, want verified", got.Status)
	}
	if ex.runs() != 1 {
		t.Fatalf("review runs = %d, want 1", ex.runs())
	}
}

func TestReviewAfterRunRejectedConclusionBecomesReviewFailed(t *testing.T) {
	s, p, source, ex := proofPolicyFixture(t)
	ex.failTitle = "rejected conclusion"
	in, err := p.Store.CreateIntent(model.Intent{Title: "producer"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := p.Store.AddRun(model.Run{IntentID: in.ID, Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	fact, err := p.Store.AddFact(model.Fact{
		IntentID: in.ID, SourceRunID: run.ID, Kind: "vuln", Title: "rejected conclusion",
		Origin: model.OriginDeclared, EvidenceIDs: source.EvidenceIDs,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Store.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Status, i.RunID = model.StatusDone, run.ID
	}); err != nil {
		t.Fatal(err)
	}
	if !s.reviewAfterRun(in.ID, run.ID) {
		t.Fatal("review phase did not claim the completed direction")
	}
	waitFor(t, 5*time.Second, func() bool {
		cur, _ := p.Store.GetIntent(in.ID)
		return cur.Status == model.StatusReviewFailed
	})
	got, _ := p.Store.FactByID(fact.ID)
	if got.Status == "verified" {
		t.Fatal("rejected conclusion was verified")
	}
	cur, _ := p.Store.GetIntent(in.ID)
	if len(cur.Notes) == 0 || cur.Notes[len(cur.Notes)-1].Kind != model.IntentNoteReview {
		t.Fatalf("review failure note missing: %+v", cur.Notes)
	}
}

func TestReviewAfterRunRetriesPriorFailuresEvenWithoutNewFacts(t *testing.T) {
	s, p, _, ex := proofPolicyFixture(t)
	in, err := p.Store.CreateIntent(model.Intent{Title: "producer"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := p.Store.AddRun(model.Run{IntentID: in.ID, Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	fact, err := p.Store.AddFact(model.Fact{
		IntentID: in.ID, SourceRunID: run.ID, Kind: "vuln", Title: "bare conclusion",
		Origin: model.OriginDeclared,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Store.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Status, i.RunID = model.StatusDone, run.ID
		i.ReviewFailedFacts = []string{fact.ID}
	}); err != nil {
		t.Fatal(err)
	}
	if !s.reviewAfterRun(in.ID, run.ID) {
		t.Fatal("review phase skipped the unresolved prior failure")
	}
	waitFor(t, 5*time.Second, func() bool {
		cur, _ := p.Store.GetIntent(in.ID)
		return cur.Status == model.StatusReviewFailed
	})
	cur, _ := p.Store.GetIntent(in.ID)
	if len(cur.ReviewFailedFacts) != 1 || cur.ReviewFailedFacts[0] != fact.ID {
		t.Fatalf("prior failure was not retained: %+v", cur.ReviewFailedFacts)
	}
	if ex.runs() != 0 {
		t.Fatalf("no-evidence prior failure spent %d review runs", ex.runs())
	}
}

func TestReviewAfterRunNoEvidenceDoesNotSpendARun(t *testing.T) {
	s, p, _, ex := proofPolicyFixture(t)
	in, err := p.Store.CreateIntent(model.Intent{Title: "producer"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := p.Store.AddRun(model.Run{IntentID: in.ID, Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	fact, err := p.Store.AddFact(model.Fact{
		IntentID: in.ID, SourceRunID: run.ID, Kind: "vuln", Title: "bare conclusion",
		Origin: model.OriginDeclared,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Store.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Status, i.RunID = model.StatusDone, run.ID
	}); err != nil {
		t.Fatal(err)
	}
	if !s.reviewAfterRun(in.ID, run.ID) {
		t.Fatal("review phase did not claim the completed direction")
	}
	waitFor(t, 5*time.Second, func() bool {
		cur, _ := p.Store.GetIntent(in.ID)
		return cur.Status == model.StatusReviewFailed
	})
	if ex.runs() != 0 {
		t.Fatalf("no-evidence conclusion spent %d review runs", ex.runs())
	}
	got, _ := p.Store.FactByID(fact.ID)
	if got.Status != "unverified" {
		t.Fatalf("no-evidence conclusion = %s, want unverified", got.Status)
	}
	reviews := p.Store.Reviews(fact.ID)
	if len(reviews) != 1 || reviews[0].Verdict != "needs_evidence" {
		t.Fatalf("no-evidence verdict = %+v", reviews)
	}
}

// newSettlementFixture is one settled direction holding a single evidence-backed
// conclusion. Tests change the parts they are about (run id, prior unresolved set,
// verifier) instead of rebuilding the ledger each time.
func newSettlementFixture(t *testing.T) (*Server, model.Intent, model.Run, model.Fact, *reviewProbeExec) {
	t.Helper()
	s, _, source, ex := proofPolicyFixture(t)
	in, err := s.Store.CreateIntent(model.Intent{Title: "producer"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.Store.AddRun(model.Run{IntentID: in.ID, Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	fact, err := s.Store.AddFact(model.Fact{
		IntentID: in.ID, SourceRunID: run.ID, Kind: "vuln", Title: "new conclusion",
		Origin: model.OriginDeclared, EvidenceIDs: source.EvidenceIDs,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Status, i.RunID = model.StatusDone, run.ID
	}); err != nil {
		t.Fatal(err)
	}
	return s, in, run, fact, ex
}

// The settlement review set belongs to the direction, not to the run that
// happened to settle it: a conclusion an earlier run produced before yielding
// (spawn/wait) must still pass the fixed review, or the direction could be done
// with an unproven report-bound finding.
func TestReviewAfterRunCoversConclusionsFromEarlierRuns(t *testing.T) {
	s, p, source, ex := proofPolicyFixture(t)
	in, err := p.Store.CreateIntent(model.Intent{Title: "producer"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := p.Store.AddRun(model.Run{IntentID: in.ID, Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.Store.AddRun(model.Run{IntentID: in.ID, Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	fact, err := p.Store.AddFact(model.Fact{
		IntentID: in.ID, SourceRunID: first.ID, Kind: "vuln", Title: "earlier conclusion",
		Origin: model.OriginDeclared, EvidenceIDs: source.EvidenceIDs,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Store.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Status, i.RunID = model.StatusDone, second.ID
	}); err != nil {
		t.Fatal(err)
	}
	if !s.reviewAfterRun(in.ID, second.ID) {
		t.Fatal("review phase skipped a conclusion an earlier run of the direction declared")
	}
	waitFor(t, 5*time.Second, func() bool {
		cur, _ := p.Store.GetIntent(in.ID)
		return cur.Status == model.StatusDone || cur.Status == model.StatusReviewFailed
	})
	cur, _ := p.Store.GetIntent(in.ID)
	if cur.Status != model.StatusDone {
		t.Fatalf("direction = %s, want done", cur.Status)
	}
	got, _ := p.Store.FactByID(fact.ID)
	if got.Status != "verified" {
		t.Fatalf("earlier conclusion = %s, want verified", got.Status)
	}
	if ex.runs() != 1 {
		t.Fatalf("review runs = %d, want 1", ex.runs())
	}
}

// A review pause suspends the fixed phase, it is not a verdict: the direction
// stays in reviewing and nothing is published. Resuming re-arms the same phase
// and the direction can still reach done.
func TestReviewAfterRunSuspendsOnReviewPause(t *testing.T) {
	s, in, run, fact, ex := newSettlementFixture(t)
	s.SetReviewsPaused(true)
	t.Cleanup(func() { s.SetReviewsPaused(false) })

	if !s.reviewAfterRun(in.ID, run.ID) {
		t.Fatal("review phase did not claim the completed direction")
	}
	waitFor(t, 5*time.Second, func() bool {
		// The suspended goroutine releases its registry entry without publishing.
		_, inflight := s.reviewPhases.Load(in.ID)
		return !inflight
	})
	cur, _ := s.Store.GetIntent(in.ID)
	if cur.Status != model.StatusReviewing {
		t.Fatalf("paused phase published %s, want reviewing", cur.Status)
	}
	if reviews := s.Store.Reviews(fact.ID); len(reviews) != 0 {
		t.Fatalf("paused phase recorded a verdict: %+v", reviews)
	}
	if ex.runs() != 0 {
		t.Fatalf("paused phase spent %d review runs", ex.runs())
	}

	// Resume is the other half of the settlement.
	s.SetReviewsPaused(false)
	s.resumeReviewPhases()
	waitFor(t, 5*time.Second, func() bool {
		cur, _ := s.Store.GetIntent(in.ID)
		return cur.Status == model.StatusDone || cur.Status == model.StatusReviewFailed
	})
	cur, _ = s.Store.GetIntent(in.ID)
	if cur.Status != model.StatusDone {
		t.Fatalf("resumed direction = %s, want done", cur.Status)
	}
	got, _ := s.Store.FactByID(fact.ID)
	if got.Status != "verified" {
		t.Fatalf("resumed conclusion = %s, want verified", got.Status)
	}
	if ex.runs() != 1 {
		t.Fatalf("resumed phase took %d review runs, want 1", ex.runs())
	}
}

// A fixed review that loses the claim race to another reviewer must wait for
// that review's verdict instead of recording a failure for it.
func TestReviewFactForPhaseWaitsOnClaimRace(t *testing.T) {
	s, _, _, fact, _ := newSettlementFixture(t)
	calls := 0
	got, err := s.reviewFactForPhase(context.Background(), fact, func(context.Context, string) (model.Review, error) {
		calls++
		if calls == 1 {
			return model.Review{}, fmt.Errorf("%w: %s", verifier.ErrBusy, fact.ID)
		}
		return model.Review{Verdict: "pass"}, nil
	})
	if err != nil {
		t.Fatalf("claim race became a review failure: %v", err)
	}
	if got.Verdict != "pass" || calls != 2 {
		t.Fatalf("verdict = %q after %d calls, want a pass after retrying the busy claim", got.Verdict, calls)
	}
}

// The on-demand pump may already hold the claim when the fixed phase starts. The
// phase must trust the verdict that review publishes, not fail for the overlap.
func TestReviewAfterRunTrustsConcurrentPumpReview(t *testing.T) {
	s, in, run, fact, _ := newSettlementFixture(t)
	ex := &reviewProbeExec{blockFirst: true, entered: make(chan struct{}, 1), release: make(chan struct{})}
	s.Verifier = verifier.New(s.Store, ex, s.CaseRoot)
	s.Verifier.Policy = s.premisePolicy
	s.Verifier.Track = s.TrackReviewRun
	var once sync.Once
	release := func() { once.Do(func() { close(ex.release) }) }
	t.Cleanup(release)

	// The pump's review claims the fact first and blocks inside the executor.
	if err := s.Verifier.Start(context.Background(), fact.ID, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		select {
		case <-ex.entered:
			return true
		default:
			return false
		}
	})
	if !s.reviewAfterRun(in.ID, run.ID) {
		t.Fatal("review phase did not claim the completed direction")
	}
	release()
	waitFor(t, 5*time.Second, func() bool {
		cur, _ := s.Store.GetIntent(in.ID)
		return cur.Status == model.StatusDone || cur.Status == model.StatusReviewFailed
	})
	cur, _ := s.Store.GetIntent(in.ID)
	if cur.Status != model.StatusDone {
		t.Fatalf("direction = %s, want done: the concurrent review's verdict must be trusted", cur.Status)
	}
	got, _ := s.Store.FactByID(fact.ID)
	if got.Status != "verified" {
		t.Fatalf("conclusion = %s, want verified", got.Status)
	}
	if ex.runs() != 1 {
		t.Fatalf("review runs = %d, want 1 (the fixed phase duplicated the pump's proof)", ex.runs())
	}
}

// A review pause that lands while the phase is already reviewing live facts is
// still a suspension: the canceled verifier runs must not be turned into a
// review_failed verdict for the direction.
func TestReviewAfterRunSuspendsWhenPausedMidReview(t *testing.T) {
	s, in, run, fact, _ := newSettlementFixture(t)
	ex := &reviewProbeExec{blockFirst: true, entered: make(chan struct{}, 1), release: make(chan struct{})}
	s.Verifier = verifier.New(s.Store, ex, s.CaseRoot)
	s.Verifier.Policy = s.premisePolicy
	s.Verifier.Track = s.TrackReviewRun
	var once sync.Once
	release := func() { once.Do(func() { close(ex.release) }) }
	t.Cleanup(release)

	if !s.reviewAfterRun(in.ID, run.ID) {
		t.Fatal("review phase did not claim the completed direction")
	}
	waitFor(t, 5*time.Second, func() bool {
		select {
		case <-ex.entered:
			return true
		default:
			return false
		}
	})
	s.SetReviewsPaused(true)
	t.Cleanup(func() { s.SetReviewsPaused(false) })
	s.CancelReviewsWithCause(model.CancelCauseReviewPause, "复核暂停：取消正在跑的复核")
	release()

	waitFor(t, 5*time.Second, func() bool {
		_, inflight := s.reviewPhases.Load(in.ID)
		return !inflight
	})
	cur, _ := s.Store.GetIntent(in.ID)
	if cur.Status != model.StatusReviewing {
		t.Fatalf("mid-review pause published %s, want reviewing", cur.Status)
	}
	if reviews := s.Store.Reviews(fact.ID); len(reviews) != 0 {
		t.Fatalf("a canceled review wrote a verdict: %+v", reviews)
	}

	s.SetReviewsPaused(false)
	s.resumeReviewPhases()
	waitFor(t, 5*time.Second, func() bool {
		cur, _ := s.Store.GetIntent(in.ID)
		return cur.Status == model.StatusDone || cur.Status == model.StatusReviewFailed
	})
	cur, _ = s.Store.GetIntent(in.ID)
	if cur.Status != model.StatusDone {
		t.Fatalf("resumed direction = %s, want done", cur.Status)
	}
	got, _ := s.Store.FactByID(fact.ID)
	if got.Status != "verified" {
		t.Fatalf("resumed conclusion = %s, want verified", got.Status)
	}
	if ex.runs() != 2 {
		t.Fatalf("review runs = %d, want the canceled one plus the resumed pass", ex.runs())
	}
}

// Readiness and the automatic queue read review history in different orders. A
// needs_evidence verdict on the current assertion must be reported as failed
// even when an older pass review exists for the same version, or the session
// waits forever for a queue that has already given up on it.
func TestReadinessSeesLatestReviewRegardlessOfSliceOrder(t *testing.T) {
	s, _, source, _ := proofPolicyFixture(t)
	owner, err := s.Store.CreateIntent(model.Intent{Title: "producer"})
	if err != nil {
		t.Fatal(err)
	}
	fact, err := s.Store.AddFact(model.Fact{
		IntentID: owner.ID, Kind: "vuln", Title: "put back conclusion",
		Origin: model.OriginDeclared, EvidenceIDs: source.EvidenceIDs,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.ReviewFact(model.Review{FactID: fact.ID, Verdict: "pass", Notes: "独立复核通过"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.ReviewFact(model.Review{FactID: fact.ID, Verdict: "needs_evidence", Notes: "操作员退回：证据不足"}); err != nil {
		t.Fatal(err)
	}
	consumer, err := s.Store.CreateIntent(model.Intent{Title: "consumer", FromFactIDs: []string{fact.ID}})
	if err != nil {
		t.Fatal(err)
	}
	got := s.readiness(s.Store.Snapshot(), consumer, s.premisePolicy())
	if len(got.Premises) != 1 || got.Premises[0].Review == nil {
		t.Fatalf("premise review state missing: %+v", got.Premises)
	}
	if got.Premises[0].Review.State != reviewStateFailed {
		t.Fatalf("review state = %s, want failed: the newer needs_evidence verdict must win", got.Premises[0].Review.State)
	}
}

// A conclusion another reviewer already proved is not a failure of the fixed
// phase: when the phase cannot run (no verifier), it must not be recorded in the
// unresolved set.
func TestReviewAfterRunDoesNotFailAlreadyVerifiedFacts(t *testing.T) {
	s, in, run, fact, _ := newSettlementFixture(t)
	if _, err := s.Store.ReviewFact(model.Review{FactID: fact.ID, Verdict: "pass", Notes: "独立复核通过"}); err != nil {
		t.Fatal(err)
	}
	// The previous phase left the id unresolved; the out-of-band proof fixed it.
	if _, err := s.Store.UpdateIntent(in.ID, func(i *model.Intent) {
		i.ReviewFailedFacts = []string{fact.ID}
	}); err != nil {
		t.Fatal(err)
	}
	s.Verifier = nil
	if !s.reviewAfterRun(in.ID, run.ID) {
		t.Fatal("review phase did not claim the prior unresolved set")
	}
	waitFor(t, 5*time.Second, func() bool {
		cur, _ := s.Store.GetIntent(in.ID)
		return cur.Status == model.StatusDone || cur.Status == model.StatusReviewFailed
	})
	cur, _ := s.Store.GetIntent(in.ID)
	if cur.Status != model.StatusDone {
		t.Fatalf("direction = %s, want done: an already-verified conclusion is not a failure", cur.Status)
	}
	if len(cur.ReviewFailedFacts) != 0 {
		t.Fatalf("verified conclusion kept in the unresolved set: %+v", cur.ReviewFailedFacts)
	}
}
