package store

import (
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/kksty/tianwu/internal/model"
)

// ErrContextConflict means the caller edited a context selection it had not
// seen (stale version), or named a run that no longer owns the direction.
// Context is deliberately independent of the task's stable hard prerequisites:
// a reference or pin can be added without rewriting the intent's premises.
var ErrContextConflict = errors.New("任务上下文版本或运行所有权已变化")

var ErrContextNotFound = errors.New("意图不存在")

// TaskContext is one explicit context selection. Version is the optimistic
// concurrency token the caller read; References/Pins/Entities are the whole
// selection, not a delta, so an update cannot silently drop the other two.
type TaskContext struct {
	Version    uint64   `json:"version"`
	References []string `json:"references"`
	Pins       []string `json:"pins"`
	Entities   []string `json:"entities"`
}

// normalizeContextIDs trims, drops empties and de-duplicates while keeping the
// caller's order: the order is how the operator listed the reads, not a ranking
// the store may silently rewrite.
func normalizeContextIDs(ids []string) []string {
	out := []string{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id != "" && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// UpdateTaskContext applies one full context selection under an explicit
// version check. runID is the current participant run when a model edits its
// own direction; it must be the holder of a running intent. An operator edit
// has no run (""), which is why the ownership half is skipped rather than
// satisfied by a fake run.
func (s *Store) UpdateTaskContext(id, runID string, c TaskContext) (model.Intent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in, ok := s.intentLocked(id)
	if !ok {
		return model.Intent{}, ErrContextNotFound
	}
	if c.Version != in.ContextVersion {
		return model.Intent{}, ErrContextConflict
	}
	if runID != "" && (in.RunID != runID || in.Status != model.StatusRunning) {
		return model.Intent{}, ErrContextConflict
	}
	references := normalizeContextIDs(c.References)
	pins := normalizeContextIDs(c.Pins)
	if err := s.checkFactRefsLocked(references, pins); err != nil {
		return model.Intent{}, err
	}
	in.ContextFactIDs = references
	in.PinnedFactIDs = pins
	in.InterestEntityRefs = normalizeContextIDs(c.Entities)
	in.ContextVersion++
	in.UpdatedAt = time.Now()
	if err := s.flush("intent"); err != nil {
		return model.Intent{}, err
	}
	return detachIntent(*in), nil
}
