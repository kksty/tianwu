package store

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kksty/tianwu/internal/model"
)

var ErrActionConflict = errors.New("外部动作版本冲突或已经完成")

func (s *Store) Actions(intentID string) []model.ExternalAction {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []model.ExternalAction{}
	for _, a := range s.db.Actions {
		if intentID == "" || a.IntentID == intentID {
			out = append(out, a)
		}
	}
	return out
}

func (s *Store) GetAction(id string) (model.ExternalAction, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if a, ok := s.actionLocked(id); ok {
		return *a, true
	}
	return model.ExternalAction{}, false
}

func (s *Store) actionLocked(id string) (*model.ExternalAction, bool) {
	for i := range s.db.Actions {
		if s.db.Actions[i].ID == id {
			return &s.db.Actions[i], true
		}
	}
	return nil, false
}

// BeginAction records intent BEFORE the external operation. A retry with the
// same key returns its original state, including unknown, rather than rerunning.
func (s *Store) BeginAction(intentID, runID, key, name, reference string) (model.ExternalAction, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, name, reference = strings.TrimSpace(key), strings.TrimSpace(name), strings.TrimSpace(reference)
	if key == "" || name == "" || len(key) > 200 || len(name) > 300 || len(reference) > 4000 {
		return model.ExternalAction{}, false, fmt.Errorf("动作 key/name 必填且长度必须有效")
	}
	in, ok := s.intentLocked(intentID)
	if !ok || !s.currentActionRunLocked(intentID, runID) {
		return model.ExternalAction{}, false, fmt.Errorf("只有当前正在运行的意图可以登记外部动作")
	}
	for _, a := range s.db.Actions {
		if a.IntentID == intentID && a.Key == key {
			if a.Name != name || a.Reference != reference {
				return model.ExternalAction{}, false, ErrIdempotencyConflict
			}
			return a, false, nil
		}
	}

	now := time.Now()
	a := model.ExternalAction{ID: NewID("ax"), IntentID: intentID, RunID: runID, Generation: in.Attempts, Key: key, Name: name, Reference: reference, State: model.ActionStarted, Version: 1, CreatedAt: now, UpdatedAt: now}
	s.db.Actions = append(s.db.Actions, a)
	if err := s.flushNoRev("action", "intent"); err != nil {
		return model.ExternalAction{}, false, err
	}
	return a, true, nil
}

// ResolveAction is CAS. Only a receipt or explicit reconciliation changes an
// unknown operation to completed; a completed action can never be reset.
func (s *Store) ResolveAction(id string, version int, state, result, resultRef, resolvedByRun string) (model.ExternalAction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if state != model.ActionCompleted && state != model.ActionUnknown {
		return model.ExternalAction{}, fmt.Errorf("状态只能是 completed 或 unknown")
	}
	result, resultRef = strings.TrimSpace(result), strings.TrimSpace(resultRef)
	if result == "" || len(result) > 12000 || len(resultRef) > 4000 {
		return model.ExternalAction{}, fmt.Errorf("必须提供结果或未知原因，且长度必须有效")
	}
	a, ok := s.actionLocked(id)
	if !ok {
		return model.ExternalAction{}, fmt.Errorf("外部动作不存在")
	}
	if resolvedByRun != "" && !s.currentActionRunLocked(a.IntentID, resolvedByRun) {
		return model.ExternalAction{}, ErrActionConflict
	}
	if a.Version != version || a.State == model.ActionCompleted {
		return model.ExternalAction{}, ErrActionConflict
	}
	a.State, a.Result, a.ResultRef, a.ResolvedByRun = state, result, resultRef, resolvedByRun
	a.Version++
	a.UpdatedAt = time.Now()
	s.wakeActionWaitsLocked(id)
	out := *a
	if err := s.flushNoRev("action", "intent"); err != nil {
		return model.ExternalAction{}, err
	}
	return out, nil
}

func (s *Store) currentActionRunLocked(intentID, runID string) bool {
	in, ok := s.intentLocked(intentID)
	if !ok || in.Status != model.StatusRunning || in.RunID != runID {
		return false
	}
	for _, run := range s.db.Runs {
		if run.ID == runID {
			return run.IntentID == intentID && run.Status == model.RunRunning
		}
	}
	return false
}

func (s *Store) markActionsUnknownLocked(runID string) {
	for i := range s.db.Actions {
		a := &s.db.Actions[i]
		if a.RunID != runID || a.State != model.ActionStarted {
			continue
		}
		a.State = model.ActionUnknown
		a.Result = "原执行轮次已结束，外部结果尚未确认；需核对后再决定后续动作"
		a.Version++
		a.UpdatedAt = time.Now()
		s.wakeActionWaitsLocked(a.ID)
	}
}

func (s *Store) wakeActionWaitsLocked(actionID string) {
	for i := range s.db.Intents {
		in := &s.db.Intents[i]
		if in.Wait == nil || in.Wait.WakePending || model.Settled(in.Status) || in.Wait.RegisteredByRun != "" && in.Wait.RegisteredByRun != in.RunID {
			continue
		}
		matches := false
		for _, id := range in.Wait.Actions {
			if id == actionID {
				matches = true
				break
			}
		}
		if !matches {
			continue
		}
		ready, results, _, reason := s.evaluateWaitLocked(in.Wait, time.Now())
		if ready {
			in.Wait.WakePending = true
			in.Wait.Results = results
			in.Wait.WakeReason = reason
			in.UpdatedAt = time.Now()
		}
	}
}

func (s *Store) evaluateActionsLocked(w *model.Wait, now time.Time) (bool, []model.WaitResult, []string, string) {
	var results []model.WaitResult
	var pending []string
	for _, id := range w.Actions {
		a, ok := s.actionLocked(id)
		if !ok {
			results = append(results, model.WaitResult{ActionID: id, Status: "missing", At: now})
			continue
		}
		if a.State == model.ActionStarted {
			pending = append(pending, id)
			continue
		}
		results = append(results, model.WaitResult{ActionID: id, Status: a.State, RunID: a.RunID, Result: a.Result, ResultRef: a.ResultRef, At: a.UpdatedAt})
	}
	if w.Deadline != nil && !now.Before(*w.Deadline) {
		return true, results, pending, model.WaitReasonTimeout
	}
	ready := len(pending) == 0 || w.Mode == model.WaitAny && len(results) > 0
	return ready, results, pending, model.WaitReasonActions
}
