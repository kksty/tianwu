package api

// Automatic review queue scheduling (F11). The store owns the enumerable
// candidate set and the durable cursor (store/review_queue.go); this file owns
// the per-call bound and the retry cooldown that keeps a failing candidate from
// buying a model run on every wake.

import (
	"context"
	"time"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// SchedulePendingReviews resumes the durable candidate set after startup or a
// review pause. It creates no findings and uses the same admission as finishes.
func (s *Server) automaticReviewsEnabled() bool {
	return s.autoVerifyEnabled() || s.premisePolicy() == model.PremiseVerifiedOnly
}
func (s *Server) autoVerifyEnabled() bool {
	s.autoReviewMu.RLock()
	defer s.autoReviewMu.RUnlock()
	return s.AutoVerify
}
func (s *Server) SetAutoVerify(enabled bool) {
	s.autoReviewMu.Lock()
	s.AutoVerify = enabled
	s.autoReviewMu.Unlock()
	s.SchedulePendingReviews()
	s.reviewPumpMu.Lock()
	if s.reviewPumpWake != nil {
		select {
		case s.reviewPumpWake <- struct{}{}:
		default:
		}
	}
	s.reviewPumpMu.Unlock()
}
func (s *Server) reviewPolicy() store.ReviewPolicy {
	mode := s.premisePolicy()
	return store.ReviewPolicy{Mode: mode, RequiredOnly: !s.autoVerifyEnabled() && mode == model.PremiseVerifiedOnly}
}

func (s *Server) SchedulePendingReviews() {
	if s.Store == nil || !s.automaticReviewsEnabled() {
		return
	}
	seen := map[string]bool{}
	candidates, _ := s.Store.ReviewCandidates("", "", int(^uint(0)>>1), s.reviewPolicy())
	for _, f := range candidates {
		if !seen[f.IntentID] && f.IntentID != "" {
			seen[f.IntentID] = true
			s.scheduleAutoVerify(f.IntentID)
		}
	}
}

func (s *Server) scheduleAutoVerify(intentID string) {
	if !s.automaticReviewsEnabled() || s.Verifier == nil || s.Store == nil || s.deleting.Load() || s.closing.Load() {
		return
	}
	s.reviewPumpMu.Lock()
	if s.reviewPumpDue == nil {
		s.reviewPumpDue = map[string]time.Time{}
	}
	s.reviewPumpDue[intentID] = time.Now()
	if s.reviewPumpWake == nil {
		s.reviewPumpWake = make(chan struct{}, 1)
	}
	if !s.reviewPumpRunning && !s.ReviewsPaused() {
		ctx := s.Ctx
		if ctx == nil {
			ctx = context.Background()
		}
		ctx, s.reviewPumpCancel = context.WithCancel(ctx)
		s.reviewPumpRunning = true
		s.reviewPumpEpoch++
		go s.runReviewPump(ctx, s.reviewPumpEpoch, s.reviewPumpCancel)
	}
	select {
	case s.reviewPumpWake <- struct{}{}:
	default:
	}
	s.reviewPumpMu.Unlock()
}

// One serial pump per project, round-robin by due time. Timed waits release the
// loop until a cooldown expires or new work arrives; no producer must finish
// again merely to make a ninth candidate reachable.
func (s *Server) runReviewPump(ctx context.Context, epoch uint64, cancel context.CancelFunc) {
	defer cancel()
	defer func() {
		s.reviewPumpMu.Lock()
		if s.reviewPumpEpoch == epoch {
			s.reviewPumpRunning = false
			s.reviewPumpCancel = nil
			if len(s.reviewPumpDue) > 0 && !s.ReviewsPaused() && !s.closing.Load() && !s.deleting.Load() && s.automaticReviewsEnabled() && (s.Ctx == nil || s.Ctx.Err() == nil) {
				base := s.Ctx
				if base == nil {
					base = context.Background()
				}
				var next context.Context
				next, s.reviewPumpCancel = context.WithCancel(base)
				s.reviewPumpRunning = true
				s.reviewPumpEpoch++
				go s.runReviewPump(next, s.reviewPumpEpoch, s.reviewPumpCancel)
			}
		}
		s.reviewPumpMu.Unlock()
	}()
	for {
		if ctx.Err() != nil || s.closing.Load() || s.deleting.Load() || s.ReviewsPaused() || !s.automaticReviewsEnabled() {
			return
		}
		s.reviewPumpMu.Lock()
		id := ""
		var due time.Time
		for candidate, at := range s.reviewPumpDue {
			if id == "" || at.Before(due) || (at.Equal(due) && candidate < id) {
				id, due = candidate, at
			}
		}
		if id == "" {
			// Seal idle while holding the same lock producers acquire, so a wake
			// cannot be lost between the empty check and deferred cleanup.
			s.reviewPumpRunning = false
			s.reviewPumpCancel = nil
			s.reviewPumpMu.Unlock()
			return
		}
		wait := time.Until(due)
		if wait <= 0 {
			delete(s.reviewPumpDue, id)
		}
		s.reviewPumpMu.Unlock()
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-s.reviewPumpWake:
				timer.Stop()
			case <-timer.C:
			}
			continue
		}
		s.autoVerify(id)
		// The store is authoritative after the pass: new versions and newly
		// appended candidates need no second queue database.
		remaining, _ := s.Store.ReviewCandidates(id, "", int(^uint(0)>>1), s.reviewPolicy())
		if len(remaining) == 0 {
			continue
		}
		next := time.Now().Add(reviewRetryCooldown)
		for _, f := range remaining {
			at := time.Now()
			if s.Verifier.Busy(f.ID) {
				at = at.Add(250 * time.Millisecond)
			} else if delay, cooling := s.reviewCoolingDown(f, at); cooling {
				at = at.Add(delay)
			}
			if at.Before(next) {
				next = at
			}
		}
		s.reviewPumpMu.Lock()
		if existing, ok := s.reviewPumpDue[id]; !ok || next.Before(existing) {
			s.reviewPumpDue[id] = next
		}
		s.reviewPumpMu.Unlock()
	}
}

type reviewSummary struct {
	Automatic  bool `json:"automatic"`
	Paused     bool `json:"paused"`
	Pending    int  `json:"pending"`
	Processing int  `json:"processing"`
	Cooling    int  `json:"cooling"`
}

func (s *Server) reviewSummary() reviewSummary {
	out := reviewSummary{Automatic: s.automaticReviewsEnabled(), Paused: s.ReviewsPaused()}
	rows, _ := s.Store.ReviewCandidates("", "", int(^uint(0)>>1), s.reviewPolicy())
	for _, f := range rows {
		if s.Verifier != nil && s.Verifier.Busy(f.ID) {
			out.Processing++
			continue
		}
		out.Pending++
		if _, cooling := s.reviewCoolingDown(f, time.Now()); cooling {
			out.Cooling++
		}
	}
	return out
}

const (
	// autoVerifyBatch bounds how many review candidates one automatic pass may
	// start. It is a batch size, not a selection window: the persisted cursor
	// walks the whole eligible ring and wraps, so a finding beyond any "newest N"
	// still gets its turn. The bound exists because a single wake must not fan
	// out into an unbounded pile of model runs.
	autoVerifyBatch = 8
	// reviewRetryCooldown is how long a failed automatic review waits before the
	// queue may retry the same assertion version. The review already spent a run
	// and left the fact unverified; retrying on the next wake (after every run
	// finish) would just burn more runs on the same candidate.
	reviewRetryCooldown = 5 * time.Minute
)

// reviewFailure is one fact's last failed automatic review. The assertion
// version is part of the record: a corrected claim is a new assertion and gets a
// fresh attempt immediately instead of inheriting the old failure's cooldown.
type reviewFailure struct {
	claimVersion uint64
	at           time.Time
}

// reviewCoolingDown reports whether the automatic queue should leave this fact
// alone for now, and how much of the cooldown is left. A cooldown that already
// expired (or belonged to an older claim version) is cleared.
func (s *Server) reviewCoolingDown(f model.Fact, now time.Time) (time.Duration, bool) {
	s.reviewFailMu.Lock()
	defer s.reviewFailMu.Unlock()
	fr, ok := s.reviewFailed[f.ID]
	if !ok || fr.claimVersion != f.ClaimVersion {
		return 0, false
	}
	if now.Sub(fr.at) >= reviewRetryCooldown {
		delete(s.reviewFailed, f.ID)
		return 0, false
	}
	return reviewRetryCooldown - now.Sub(fr.at), true
}

// noteReviewFailure records a failed automatic review. The map holds at most one
// entry per fact, so it cannot grow with retries.
func (s *Server) noteReviewFailure(f model.Fact, now time.Time) {
	s.reviewFailMu.Lock()
	defer s.reviewFailMu.Unlock()
	if s.reviewFailed == nil {
		s.reviewFailed = map[string]reviewFailure{}
	}
	s.reviewFailed[f.ID] = reviewFailure{claimVersion: f.ClaimVersion, at: now}
}

// clearReviewFailure forgets the failure record once the fact got a usable
// verdict, so a later claim correction starts with a clean slate.
func (s *Server) clearReviewFailure(f model.Fact) {
	s.reviewFailMu.Lock()
	defer s.reviewFailMu.Unlock()
	delete(s.reviewFailed, f.ID)
}
