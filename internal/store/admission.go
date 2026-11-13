package store

import (
	"fmt"
	"time"

	"github.com/kksty/tianwu/internal/model"
)

// StartIntentRun commits ownership, generation, run and wait transfer together.
// A failed write leaves neither a claimed intent nor an orphan running row.
func (s *Store) StartIntentRun(id, node, owner string, run model.Run) (model.Intent, model.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in, ok := s.intentLocked(id)
	if !ok {
		return model.Intent{}, model.Run{}, fmt.Errorf("intent %s 不存在", id)
	}
	if !handoffOwnerMatches(in, owner) {
		return model.Intent{}, model.Run{}, ErrHandoffConflict
	}
	if in.Status != model.StatusPending && in.Status != model.StatusFailed {
		return model.Intent{}, model.Run{}, fmt.Errorf("intent %s 无法认领（状态为 %s）", id, in.Status)
	}
	now := time.Now()
	previousRun := in.RunID
	if in.Wait != nil {
		ready, results, _, reason := s.evaluateWaitLocked(in.Wait, now)
		if ready {
			in.Wait.WakePending, in.Wait.Results, in.Wait.WakeReason = true, results, reason
		}
	}
	previousWait := detachWait(in.Wait)
	in.Status, in.ClaimedBy, in.Node = model.StatusRunning, node, node
	in.Hold, in.Aborted, in.HandoffOperation = false, false, ""
	in.Attempts++
	in.UpdatedAt = now
	run.ID, run.IntentID, run.Status = NewID("r"), id, model.RunRunning
	run.StartedAt, run.Generation = now, in.Attempts
	run.PremiseVersions = map[string]uint64{}
	for _, id := range in.FromFactIDs {
		run.PremiseVersions[id] = s.records[recordKey("fact", id)].version
	}
	in.RunID = run.ID
	if in.Wait != nil {
		if in.Wait.WakePending {
			in.Wait = nil
		} else {
			in.Wait.RegisteredByRun = run.ID
		}
	}
	s.db.Runs = append(s.db.Runs, run)
	if err := s.flush("intent", "run"); err != nil {
		return model.Intent{}, model.Run{}, err
	}
	// Dispatch builds retry history from the previous holder, while the ledger
	// already points at the new run for concurrent API calls and settlement.
	brief := detachIntent(*in)
	brief.RunID = previousRun
	brief.Wait = previousWait
	return brief, detachRun(run), nil
}
