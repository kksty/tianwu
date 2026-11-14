package api

// Fixed review settlement: a successful executor run is not done until the new
// report-bound conclusions it produced have been independently re-proven. The
// review phase is deliberately not a separate, optional support run behind the
// commander's back: it owns the direction's transition to done/review_failed and
// the wake that follows, so no downstream direction can act on facts that have
// not passed the gate.
//
// Scope is deliberately narrow: the direction's declared vuln/cred conclusions
// with preserved evidence are reviewed automatically. Other reviewable kinds
// (host/service/loot/artifact) are reviewed on demand when a downstream
// direction cites them as a necessary premise (the existing review pump). notes
// are never reviewable.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/verifier"
)

const reviewFailureNoteCap = 20

// reviewAfterRun claims a completed direction into the reviewing phase and
// reports true when the review phase owns the wake. False means there was
// nothing to review (or the direction was already moved by another actor), so
// the ordinary wake path continues.
func (s *Server) reviewAfterRun(intentID, runID string) bool {
	if s.Project == nil {
		// Bare test servers have no project ledger/policy to settle; the fixed
		// phase is a project guarantee, never a hidden global one.
		return false
	}
	if s.premisePolicy() != model.PremiseVerifiedOnly {
		// There is one mode, but a bare test server can construct a synthetic
		// intent without a project. It is not a production path.
		return false
	}
	facts := s.reviewFactsForRun(intentID, runID)
	if len(facts) == 0 {
		return false
	}
	// Register the phase before the CAS: a resume that lands between the claim
	// and the goroutine start must see the in-flight phase, not start a second.
	if !s.claimReviewPhase(intentID) {
		// A phase already owns this direction (resume raced the finish): it is
		// responsible for the final state and the wake.
		return true
	}
	if _, applied, err := s.Store.UpdateIntentIfCurrent(intentID, model.StatusDone, runID, func(i *model.Intent) {
		i.Status = model.StatusReviewing
		i.Hold = false
		i.Wait = nil
	}); err != nil {
		log.Printf("review %s: 认领 reviewing 失败：%v", intentID, err)
		s.reviewPhases.Delete(intentID)
		return false
	} else if !applied {
		// A concurrent operator cancel or a newer run moved the direction first;
		// their decision is authoritative and the ordinary path keeps the wake.
		s.reviewPhases.Delete(intentID)
		return false
	}

	go s.runReviewPhase(intentID, runID, facts)
	return true
}

// claimReviewPhase registers a direction's in-flight fixed review. False means a
// phase already owns the direction: it will publish the final state, so the
// caller must not start a second goroutine for the same direction.
func (s *Server) claimReviewPhase(intentID string) bool {
	_, loaded := s.reviewPhases.LoadOrStore(intentID, struct{}{})
	return !loaded
}

// resumeReviewPhases re-arms the fixed phases a review pause suspended. A pause
// is an operator emergency stop, not a verdict about the work, so a suspended
// direction stays in reviewing and publishes nothing. Resuming is the other half
// of the settlement: a phase whose goroutine is still alive is left alone, an
// empty review set publishes done directly (the phase would have found nothing
// left to prove), and the rest run their reviews again.
func (s *Server) resumeReviewPhases() {
	if s.Store == nil || s.ReviewsPaused() {
		return
	}
	for _, in := range s.Store.ListIntents() {
		if in.Status != model.StatusReviewing {
			continue
		}
		if !s.claimReviewPhase(in.ID) {
			continue
		}
		facts := s.settlementReviewFacts(in.ID)
		if len(facts) == 0 {
			s.settleReviewPhase(in.ID, in.RunID, model.StatusDone, "", nil)
			s.reviewPhases.Delete(in.ID)
			continue
		}
		go s.runReviewPhase(in.ID, in.RunID, facts)
	}
}

// reviewFactsForRun is the settlement review set for a direction whose run just
// completed. runID is kept for the call-site contract; the set itself is
// direction-scoped (see settlementReviewFacts).
func (s *Server) reviewFactsForRun(intentID, runID string) []model.Fact {
	return s.settlementReviewFacts(intentID)
}

// settlementReviewFacts is the settlement review set. It belongs to the
// direction, not to a single run: a run that yielded (spawn/wait), a crash
// reconciliation or a retry must not hide conclusions an earlier run produced,
// and the unresolved facts from a previous review_failed phase stay in it until
// they are proven or corrected. Evidence is checked by the phase itself because
// a missing file is a needs_evidence verdict, not a reason to hide the fact.
func (s *Server) settlementReviewFacts(intentID string) []model.Fact {
	seen := map[string]bool{}
	out := []model.Fact{}
	add := func(f model.Fact) {
		if seen[f.ID] {
			return
		}
		seen[f.ID] = true
		out = append(out, f)
	}
	for _, f := range s.Store.Snapshot().Facts {
		if f.IntentID != intentID || f.Status != "unverified" || f.Origin == model.OriginDerived {
			continue
		}
		switch f.Kind {
		case "vuln", "cred":
			add(f)
		}
	}
	if in, ok := s.Store.GetIntent(intentID); ok {
		for _, id := range in.ReviewFailedFacts {
			if f, ok := s.Store.FactByID(id); ok {
				add(f)
			}
		}
	}
	return out
}

// runReviewPhase re-proves the claimed facts, then publishes exactly one final
// direction state. A concurrent operator retry/cancel moves the direction out of
// reviewing; the final CAS refuses to overwrite that decision.
func (s *Server) runReviewPhase(intentID, runID string, facts []model.Fact) {
	if s.ReviewsPaused() {
		// A review pause suspends the phase: publish nothing, keep the direction
		// in reviewing. resumeReviewPhases re-arms it when the switch flips back.
		s.suspendReviewPhase(intentID, runID)
		return
	}
	ctx := s.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	var (
		mu        sync.Mutex
		failures  []string
		failedIDs = map[string]bool{}
		wg        sync.WaitGroup
	)
	fail := func(factID, format string, args ...any) {
		mu.Lock()
		if factID != "" {
			failedIDs[factID] = true
		}
		if len(failures) < reviewFailureNoteCap {
			failures = append(failures, fmt.Sprintf(format, args...))
		}
		mu.Unlock()
	}
	markAllFailed := func(format string, args ...any) {
		for _, f := range facts {
			if cur, ok := s.Store.FactByID(f.ID); ok && cur.Status == "verified" {
				// Proven by another reviewer while this phase could not run:
				// that is not a failure of the phase.
				continue
			}
			fail(f.ID, format, args...)
		}
	}
	if s.Verifier == nil {
		markAllFailed("复核未启用：无法完成固定复核")
	} else {
		for _, f := range facts {
			f := f
			if f.Status == "verified" {
				continue
			}
			if f.Status == "rejected" {
				fail(f.ID, "结论「%s」已被否决：需重新测试并给出新断言", f.Title)
				continue
			}
			if len(f.EvidenceIDs) == 0 {
				// No bytes means no independent proof. Record the verdict without
				// buying a model run, and make the direction retryable.
				_, err := s.Store.ReviewFact(model.Review{
					FactID:  f.ID,
					Verdict: "needs_evidence",
					Notes:   "没有可打开的原始证据，未启动复核",
				})
				if err != nil {
					fail(f.ID, "复核结论「%s」无法登记：%v", f.Title, err)
				} else {
					fail(f.ID, "结论「%s」没有原始证据，需补证据后重试", f.Title)
				}
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				review, err := s.reviewFactForPhase(ctx, f, s.Verifier.Run)
				if err != nil {
					fail(f.ID, "结论「%s」复核未完成：%v", f.Title, err)
					return
				}
				switch review.Verdict {
				case "pass":
					return
				case "reject":
					fail(f.ID, "结论「%s」复核否决：%s", f.Title, review.Notes)
				case "needs_evidence":
					fail(f.ID, "结论「%s」缺证据：%s", f.Title, review.Notes)
				default:
					fail(f.ID, "结论「%s」复核结论异常：%s", f.Title, review.Verdict)
				}
			}()
		}
		wg.Wait()
	}

	// The operator may have paused reviews while this batch was running (the
	// pause cancels live verifier runs). A pause is a suspension, not a verdict:
	// discard the partial outcome, keep the direction in reviewing and let the
	// resume re-arm the phase.
	if s.ReviewsPaused() {
		s.suspendReviewPhase(intentID, runID)
		return
	}

	final, note := model.StatusDone, ""
	if len(failures) > 0 {
		final = model.StatusReviewFailed
		note = "固定复核未通过："
		for i, failure := range failures {
			if i > 0 {
				note += "；"
			}
			note += failure
		}
	}
	s.reviewPhases.Delete(intentID)
	s.settleReviewPhase(intentID, runID, final, note, failedIDs)
}

// suspendReviewPhase releases the phase registry without publishing a state. The
// release happens before the switch is re-read: a resume that landed while this
// goroutine was deciding sees no in-flight phase and re-arms; if the switch was
// already off when the registry was released, this goroutine re-arms itself so
// the direction cannot be left in reviewing between the two events.
func (s *Server) suspendReviewPhase(intentID, runID string) {
	s.reviewPhases.Delete(intentID)
	if s.ReviewsPaused() {
		return
	}
	if !s.claimReviewPhase(intentID) {
		return
	}
	facts := s.settlementReviewFacts(intentID)
	if len(facts) == 0 {
		s.settleReviewPhase(intentID, runID, model.StatusDone, "", nil)
		s.reviewPhases.Delete(intentID)
		return
	}
	go s.runReviewPhase(intentID, runID, facts)
}

// reviewFactForPhase proves one fact for the fixed phase. Busy() is only a hint:
// another reviewer (the on-demand pump, or the operator's verify request) may
// claim the fact between that check and this claim. ErrBusy therefore is not a
// failure of this phase — wait for the other claim and trust the status it
// publishes instead of turning the overlap into a false review_failed.
//
// run is Verifier.Run in production; it is a parameter so the retry policy can be
// tested without depending on a real race window.
func (s *Server) reviewFactForPhase(ctx context.Context, f model.Fact, run func(context.Context, string) (model.Review, error)) (model.Review, error) {
	for {
		for s.Verifier.Busy(f.ID) {
			select {
			case <-ctx.Done():
				return model.Review{}, fmt.Errorf("复核被中断：%w", ctx.Err())
			case <-time.After(100 * time.Millisecond):
			}
		}
		if cur, ok := s.Store.FactByID(f.ID); ok && cur.Status == "verified" {
			// The other reviewer already published the proof: nothing to redo.
			return model.Review{Verdict: "pass"}, nil
		}
		review, err := run(ctx, f.ID)
		if errors.Is(err, verifier.ErrBusy) {
			// Lost the claim race; the winner will publish. Go back to waiting
			// instead of recording a failure for a review that is still running.
			continue
		}
		return review, err
	}
}

// settleReviewPhase publishes exactly one final direction state for a claimed
// review phase. It is the single owner of the reviewing -> done/review_failed
// transition: the CAS refuses to overwrite an operator stop or a newer run.
// afterIntentSettled is the single settlement wake: it routes and delivers
// pending work, settles a parent wait and pumps the queue; a second explicit
// wake here would only duplicate that chain.
func (s *Server) settleReviewPhase(intentID, runID, final, note string, failedIDs map[string]bool) bool {
	var applied bool
	if _, ok, err := s.Store.UpdateIntentIfCurrent(intentID, model.StatusReviewing, runID, func(i *model.Intent) {
		i.Status = final
		i.Hold = false
		i.Wait = nil
		i.ReviewFailedFacts = nil
		if final == model.StatusReviewFailed {
			ids := make([]string, 0, len(failedIDs))
			for id := range failedIDs {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			i.ReviewFailedFacts = ids
		}
		if note != "" {
			i.Notes = append(i.Notes, model.IntentNote{
				At:   time.Now(),
				Kind: model.IntentNoteReview,
				Text: s.redactSecrets(note),
			})
		}
	}); err != nil {
		log.Printf("review %s: 写入最终状态失败：%v", intentID, err)
		return false
	} else {
		applied = ok
	}
	if !applied {
		// A concurrent operator cancel or a newer run moved the direction out of
		// reviewing; that decision stays final and nobody else needs the wake.
		return false
	}
	s.afterIntentSettled(intentID, runID, model.RunDone)
	return true
}
