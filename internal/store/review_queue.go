package store

// The automatic review queue is an enumeration, not a top-N. A finding can be
// pushed out of "the newest 50 facts" by notes and host observations; that must
// not mean it never gets a second look (F11). This file owns the eligible set,
// its stable ring order, and the durable per-intent resume cursor. The caller
// owns the review switch, the concurrency admission and the retry cooldown; the
// store only answers "which candidates exist and where were we".

import (
	"sort"
	"strings"

	"github.com/kksty/tianwu/internal/model"
)

// ReviewEligible decides what belongs in the automatic review queue: the same
// findings a report would carry, while they are still unverified. verifier.Eligible
// delegates here so the queue and the verifier's admission can never disagree
// (verifier imports store; store must not import verifier).
func ReviewEligible(f model.Fact) bool {
	if f.Status != "unverified" {
		return false
	}
	switch f.Kind {
	case "vuln", "cred":
		return true
	}
	return false
}

// ReviewCandidates returns up to limit review candidates in stable ID order,
// starting strictly after afterID ("" starts at the smallest ID), and the cursor
// a following walk should resume after.
//
// The walk is contiguous and wraps at the tail: when fewer than limit candidates
// remain the batch returns just those and next is the ID of the last one
// returned, so the next call wraps to the ring head. Returned next is the
// "already handled" point, not the first untouched candidate: the caller must
// persist it only after the returned facts were actually processed, so a pause
// leaves the cursor on the last processed candidate and the next pass starts
// with the first untouched one.
//
// Every eligible fact is reachable: the order is by ID and the walk wraps, so a
// new note or host fact can push an old finding out of a "newest N" window but
// not out of the queue. A verified or rejected fact drops out of the set; a
// claim correction returns it to unverified (store.UpdateFact) and therefore
// back into the queue with no separate bookkeeping.
type ReviewPolicy struct {
	Mode         string
	RequiredOnly bool
}

func (s *Store) ReviewCandidates(intentID, afterID string, limit int, options ...ReviewPolicy) ([]model.Fact, string) {
	if limit <= 0 {
		return nil, afterID
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var policy ReviewPolicy
	if len(options) > 0 {
		policy = options[0]
	}
	needed := map[string]bool{}
	if policy.Mode == model.PremiseVerifiedOnly || policy.RequiredOnly {
		for _, in := range s.db.Intents {
			if model.Settled(in.Status) {
				continue
			}
			for _, id := range in.FromFactIDs {
				needed[id] = true
			}
		}
	}
	rows := make([]model.Fact, 0, len(s.db.Facts))
	for _, f := range s.db.Facts {
		if intentID != "" && f.IntentID != intentID {
			continue
		}
		eligible := ReviewEligible(f) || (policy.Mode == model.PremiseVerifiedOnly && needed[f.ID] && f.Status == "unverified" && model.ReviewableKind(f.Kind))
		if !eligible || (policy.RequiredOnly && !needed[f.ID]) {
			continue
		}
		rows = append(rows, detachFact(f))
	}
	if len(rows) == 0 {
		return nil, ""
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	start := sort.Search(len(rows), func(i int) bool { return rows[i].ID > afterID })
	if start == len(rows) {
		// The cursor is past the tail (or the rest of the ring left the set):
		// wrap to the beginning.
		start = 0
	}
	batch := rows[start:]
	if len(batch) > limit {
		batch = batch[:limit]
	}
	return batch, batch[len(batch)-1].ID
}

// ReviewCursor returns the stored resume point for one intent, "" when the walk
// has not started. It is durable ledger memory, not process state: a restart
// continues where the last pass stopped instead of re-reading the ring head.
func (s *Store) ReviewCursor(intentID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db.Memory == nil || intentID == "" {
		return ""
	}
	return s.db.Memory.ReviewCursors[intentID]
}

// SetReviewCursor records the resume point for one intent. It flushes as
// bookkeeping (flushNoRev): a cursor move is not a change to the fact graph and
// must not wake the commander.
func (s *Store) SetReviewCursor(intentID, nextID string) error {
	if strings.TrimSpace(intentID) == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	mem := Memory{Epoch: s.readEpoch, Checkpoints: map[string]Checkpoint{}}
	if s.db.Memory != nil {
		mem = *s.db.Memory
	}
	mem.ReviewCursors = map[string]string{}
	if s.db.Memory != nil {
		for k, v := range s.db.Memory.ReviewCursors {
			mem.ReviewCursors[k] = v
		}
	}
	if nextID == "" {
		delete(mem.ReviewCursors, intentID)
	} else {
		mem.ReviewCursors[intentID] = nextID
	}
	s.db.Memory = &mem
	return s.flushNoRev("memory")
}
