package store

import (
	"errors"
	"fmt"
	"time"

	"github.com/kksty/tianwu/internal/model"
)

var ErrHandoffConflict = errors.New("交接冲突：版本已变化或方向正由另一项交接持有")

// BeginHandoff is the handoff gate: it validates the optimistic-concurrency
// version and claims the intent for the replacement session in one store lock.
// A stale expectedVersion returns ErrHandoffConflict without touching any field,
// so a retried/raced request never cancels a run or clears a wait first.
//
// Hold is set here and owned by the caller until StartIntentRun commits the new
// run or the handoff aborts (ReleaseHandoffHold). Between
// those points pump must not restart the direction: the old run is being stopped
// and a "pending" state there would look like fresh work.
func (s *Store) BeginHandoff(intentID string, expectedVersion int) (model.Intent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in, ok := s.intentLocked(intentID)
	if !ok {
		return model.Intent{}, fmt.Errorf("意图 %s 不存在", intentID)
	}
	if in.HandoffOperation != "" || in.HandoffVersion != expectedVersion {
		return model.Intent{}, fmt.Errorf("%w：期望 %d，当前 %d", ErrHandoffConflict, expectedVersion, in.HandoffVersion)
	}
	switch in.Status {
	case model.StatusPending, model.StatusClaimed, model.StatusRunning, model.StatusWaiting:
	default:
		return model.Intent{}, fmt.Errorf("意图已结束（%s）：先 retry/reopen，再交接", in.Status)
	}

	in.HandoffOperation = NewID("handoff")
	in.Hold = true
	in.UpdatedAt = time.Now()
	if err := s.flush("intent"); err != nil {
		return model.Intent{}, err
	}
	return detachIntent(*in), nil
}

// ReleaseHandoffHold aborts an in-flight handoff before it writes a snapshot: the
// Hold taken by BeginHandoff is dropped so the direction can be scheduled again.
// Only the operation that took the hold may release it. An explicit stop or
// completion keeps its terminal status and hold.
func (s *Store) ReleaseHandoffHold(intentID, reason string, owner ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	in, ok := s.intentLocked(intentID)
	if !ok {
		return fmt.Errorf("意图 %s 不存在", intentID)
	}
	if in.HandoffOperation == "" || !handoffOwnerMatches(in, firstOwner(owner)) {
		return ErrHandoffConflict
	}
	in.HandoffOperation = ""
	if !model.Settled(in.Status) {
		in.Hold = false
	}
	in.UpdatedAt = time.Now()
	return s.flush("intent")
}

// SaveHandoff records one replacement snapshot with optimistic concurrency.
// expectedVersion is the HandoffVersion the caller saw; a mismatch means another
// turn already handed the direction over, and the stale caller must not overwrite
// it (nor start a second replacement session).
//
// Hold is deliberately not cleared here: the handoff owner set it in
// BeginHandoff, and only a successful claim of the replacement run may release
// it. Clearing it here would reopen the exact window pump raced through.
func (s *Store) SaveHandoff(intentID string, expectedVersion int, h model.Handoff, owner ...string) (model.Intent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in, ok := s.intentLocked(intentID)
	if !ok {
		return model.Intent{}, fmt.Errorf("意图 %s 不存在", intentID)
	}
	if in.HandoffOperation == "" || !handoffOwnerMatches(in, firstOwner(owner)) || in.HandoffVersion != expectedVersion || model.Settled(in.Status) {
		return model.Intent{}, fmt.Errorf("%w：期望 %d，当前 %d", ErrHandoffConflict, expectedVersion, in.HandoffVersion)
	}
	now := time.Now()
	// Capture the latest receipt under the ownership lock, including a response
	// that arrived while the API was gathering facts for the snapshot.
	if in.Wait != nil {
		ready, results, _, reason := s.evaluateWaitLocked(in.Wait, now)
		in.Wait.WakePending, in.Wait.Results, in.Wait.WakeReason = ready, results, reason
	}
	h.Wait = detachWait(in.Wait)
	h.ID = NewID("h")
	h.Version = in.HandoffVersion + 1
	h.CreatedAt = now
	if h.FromRun == "" {
		h.FromRun = in.RunID
	}
	h.FromProfile = in.Profile
	h.FromGeneration = in.Attempts
	in.HandoffVersion = h.Version
	in.Handoff = &h
	if h.ToProfile != "" {
		in.Profile = h.ToProfile
	}
	// The replacement run starts from pending; a waiting direction keeps its Wait
	// (and active children) and rebinds it to the new run after the claim.
	in.Status = model.StatusPending
	in.ClaimedBy = ""
	in.UpdatedAt = now
	if err := s.flush("intent"); err != nil {
		return model.Intent{}, err
	}
	return detachIntent(*in), nil
}

func firstOwner(owner []string) string {
	if len(owner) > 0 {
		return owner[0]
	}
	return ""
}

func handoffOwnerMatches(in *model.Intent, owner string) bool {
	return in.HandoffOperation == owner
}

// New persisted delivery/ownership states require an explicit opt-in backup.
// Opening an old ledger alone never rewrites it.

// RecoverHandoffs leaves interrupted replacements held for an explicit retry.
// Restarting a partially replaced session automatically could duplicate work.
func (s *Store) RecoverHandoffs() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for i := range s.db.Intents {
		in := &s.db.Intents[i]
		if in.HandoffOperation == "" {
			continue
		}
		in.HandoffOperation, in.ClaimedBy = "", ""
		in.Status, in.Hold = model.StatusFailed, true
		in.UpdatedAt = time.Now()
		changed = true
	}
	if changed {
		return s.flush("intent")
	}
	return nil
}
