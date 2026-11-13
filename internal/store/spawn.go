package store

// Spawn is the ledger half of "create one more execution unit".
//
// A spawned worker is still an intent (it is a direction of work with its own
// case, claim and run); the difference from a child intent is that it may have
// no parent direction at all. The creation and its idempotency check happen
// under the store lock, so two concurrent spawns with the same key cannot both
// create a worker: one wins and the other observes the existing row.

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/kksty/tianwu/internal/model"
)

var (
	ErrSpawnInvalid             = errors.New("造人请求不合法")
	ErrSpawnIdempotencyConflict = errors.New("同一个幂等键对应不同的 worker")
)

// CreateSpawnIntent records one spawned worker. When the request repeats an
// idempotency key with the same payload, the original intent is returned with
// created=false instead of a second worker being made.
//
// A spawn beneath parentID inherits the operator constraints (Doc), the skills
// and the node default from that direction, exactly like a child intent; it does
// not inherit its model profile (an empty profile goes through the normal
// worker_profile → active chain, so a strong parent still spawns cheap workers).
func (s *Store) CreateSpawnIntent(in model.Intent) (model.Intent, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if in.ParentID != "" {
		parent, ok := s.intentLocked(in.ParentID)
		if !ok {
			return model.Intent{}, false, fmt.Errorf("父意图 %s 不存在", in.ParentID)
		}
		switch parent.Status {
		case model.StatusPending, model.StatusClaimed, model.StatusRunning:
		default:
			return model.Intent{}, false, fmt.Errorf("父意图 %s 是 %s：不能再造人", in.ParentID, parent.Status)
		}
		in.Doc = parent.Doc
		if len(in.Skills) == 0 {
			in.Skills = append([]string(nil), parent.Skills...)
		}
		if in.Node == "" {
			in.Node = parent.Node
		}
	}
	if in.IdempotencyKey != "" {
		for _, cur := range s.db.Intents {
			if cur.IdempotencyKey != in.IdempotencyKey || cur.CreatedBy != in.CreatedBy ||
				cur.ParentID != in.ParentID {
				continue
			}
			// Node is deliberately not compared: dispatch pins the intent's node
			// to where it actually runs, so a retry whose request left node
			// empty would otherwise look like a different payload.
			if cur.Title != in.Title || cur.Description != in.Description ||
				cur.Eval != in.Eval || cur.Profile != in.Profile ||
				cur.MaxTurns != in.MaxTurns ||
				!slices.Equal(cur.Skills, in.Skills) ||
				!slices.Equal(cur.FromFactIDs, in.FromFactIDs) ||
				!slices.Equal(cur.ContextFactIDs, normalizeContextIDs(in.ContextFactIDs)) ||
				!slices.Equal(cur.PinnedFactIDs, normalizeContextIDs(in.PinnedFactIDs)) {
				return model.Intent{}, false, ErrSpawnIdempotencyConflict
			}
			return detachIntent(cur), false, nil
		}
	}
	if err := s.applyInitialContextLocked(&in); err != nil {
		return model.Intent{}, false, err
	}
	s.snapshotPremiseClaimsLocked(&in)
	now := time.Now()
	in.ID = NewID("in")
	in.Status = model.StatusPending
	in.Hold = false
	in.Aborted = false
	in.Wait = nil
	in.CreatedAt, in.UpdatedAt = now, now
	in = detachIntent(in)
	s.db.Intents = append(s.db.Intents, in)
	if err := s.flush("intent"); err != nil {
		return model.Intent{}, false, err
	}
	return detachIntent(in), true, nil
}
