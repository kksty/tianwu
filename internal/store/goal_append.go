package store

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kksty/tianwu/internal/model"
)

// ErrGoalIdempotencyConflict means the same idempotency key arrived with a
// different goal payload. Returning the first goal for a changed request would
// hide that the caller believes it appended something else, so the retry is
// refused instead (same contract as workers and messages).
var ErrGoalIdempotencyConflict = errors.New("同一个幂等键对应不同的目标")

// AddGoalIdempotent appends one goal, or finds the goal an earlier request with
// the same non-empty IdempotencyKey already created. The lookup and the append
// happen under the same store lock, so two concurrent retries cannot both
// append: exactly one call reports created=true and both return the same goal
// id. An empty key is the plain create path and never dedupes.
//
// The receipt fields (met/evidence/note/from/completed_at) belong to a stamped
// goal and are never written here: appending a new goal must leave an earlier
// goal's completion proof exactly as it was.
func (s *Store) AddGoalIdempotent(g model.Goal) (model.Goal, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g.IdempotencyKey = strings.TrimSpace(g.IdempotencyKey)
	if g.IdempotencyKey != "" {
		for i := range s.db.Goals {
			cur := s.db.Goals[i]
			if cur.IdempotencyKey != g.IdempotencyKey {
				continue
			}
			if cur.Statement != g.Statement || cur.Start != g.Start || cur.QuantifiedTarget != g.QuantifiedTarget {
				return model.Goal{}, false, fmt.Errorf("%w：%s", ErrGoalIdempotencyConflict, g.IdempotencyKey)
			}
			return markGoalReceipt(detachGoal(cur), s.db.Facts), false, nil
		}
	}
	g.ID = NewID("g")
	g.Met = false
	g.EvidenceID = ""
	g.From, g.FromClaimVersions, g.CompletedAt, g.Note = nil, nil, nil, ""
	g.CompletionPolicy = ""
	g.ReceiptStale, g.ReceiptChanged = false, nil
	g.CreatedAt = time.Now()
	s.db.Goals = append(s.db.Goals, g)
	if err := s.flush("goal"); err != nil {
		return model.Goal{}, false, err
	}
	return detachGoal(g), true, nil
}
