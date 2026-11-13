package store

import (
	"fmt"

	"github.com/kksty/tianwu/internal/model"
)

// PrepareRunInput freezes the actual input and its versions together.
// Claiming a task happens earlier: observations can change during preparation.
//
// Three sets are frozen because they answer different questions later:
//   - PremiseVersions: the required facts this dispatch is based on (their
//     record revisions, used by the change views);
//   - PremiseClaimVersions: the assertion versions a reviewer would need to
//     reproduce a conclusion from this input;
//   - ContextFactVersions: the explicit references/pins the task selected, so a
//     later focus projection can tell what the run actually saw.
func (s *Store) PrepareRunInput(runID string, task model.Intent, policy string) (DB, error) {
	if !model.ValidPremisePolicy(policy) {
		return DB{}, fmt.Errorf("unknown premise policy %q", policy)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	index := -1
	for i, run := range s.db.Runs {
		if run.ID == runID && run.IntentID == task.ID && run.Status == model.RunRunning {
			index = i
			break
		}
	}
	if index < 0 {
		return DB{}, fmt.Errorf("run is no longer active")
	}
	current, ok := s.intentLocked(task.ID)
	if !ok || current.RunID != runID || current.Status != model.StatusRunning {
		return DB{}, fmt.Errorf("run no longer owns the intent")
	}
	versions := make(map[string]uint64, len(task.FromFactIDs))
	claims := make(map[string]uint64, len(task.FromFactIDs))
	for _, id := range task.FromFactIDs {
		r, ok := s.records[recordKey("fact", id)]
		if !ok {
			return DB{}, fmt.Errorf("premise %s is missing", id)
		}
		f := r.value.(model.Fact)
		if issue := model.PremiseIssue(f, policy); issue != "" {
			return DB{}, fmt.Errorf("premise %s claim v%d (%s): %s", id, f.ClaimVersion, policy, issue)
		}
		if policy == model.PremiseVerifiedOnly {
			if _, proven := model.CurrentProof(f, s.db.Reviews); !proven {
				return DB{}, fmt.Errorf("premise %s 缺少当前断言的独立核验收据", id)
			}
		}
		versions[id] = r.version
		claims[id] = f.ClaimVersion
	}
	// References and pins are context, not prerequisites: a row that vanished
	// (or an id the caller mistyped) must not block the dispatch. It is simply
	// absent from the frozen set, and the focus projection reports it missing.
	contextVersions := make(map[string]uint64, len(task.ContextFactIDs)+len(task.PinnedFactIDs))
	for _, id := range append(append([]string(nil), task.ContextFactIDs...), task.PinnedFactIDs...) {
		if r, ok := s.records[recordKey("fact", id)]; ok {
			contextVersions[id] = r.version
		}
	}
	s.db.Runs[index].PremiseVersions = versions
	s.db.Runs[index].PremisePolicy = policy
	s.db.Runs[index].PremiseClaimVersions = claims
	s.db.Runs[index].ContextFactVersions = contextVersions
	s.db.Runs[index].InstructionVersions = instructionVersions(ApplicableInstructions(s.db, task.ID, false))
	if err := s.flush("run"); err != nil {
		return DB{}, err
	}
	db := s.db.clone()
	for i := range db.Intents {
		if db.Intents[i].ID != task.ID {
			continue
		}
		// Freeze the selection on the snapshot too: the prompt builder and the
		// focus writer must read the same three lists the versions were taken
		// from, not a live intent row that changed while preparing.
		db.Intents[i].FromFactIDs = append([]string(nil), task.FromFactIDs...)
		db.Intents[i].ContextFactIDs = append([]string(nil), task.ContextFactIDs...)
		db.Intents[i].PinnedFactIDs = append([]string(nil), task.PinnedFactIDs...)
		db.Intents[i].InterestEntityRefs = append([]string(nil), task.InterestEntityRefs...)
	}
	return db, nil
}
