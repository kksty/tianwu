package store

// Child delegation and waits.
//
// Both are one atomic state change with the ledger, not a side table: a child
// intent, its parent link and the parent's wait must survive a restart together,
// and the "child finished before the wait was registered" race must be evaluated
// under the same lock that records the wait. Everything here runs under s.mu and
// persists with flush() (a wait/child is a change the commander must see).

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/kksty/tianwu/internal/model"
)

var (
	ErrChildInvalid             = errors.New("子意图请求不合法")
	ErrChildIdempotencyConflict = errors.New("同一个幂等键对应不同的子意图")
	ErrWaitInvalid              = errors.New("等待条件不合法")
)

// CreateChildIntent records one delegated direction and its parent link. A
// repeated request with the same idempotency key returns the original child
// (created=false) instead of creating a second assistant.
func (s *Store) CreateChildIntent(parentID string, child model.Intent) (model.Intent, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	parent, ok := s.intentLocked(parentID)
	if !ok {
		return model.Intent{}, false, fmt.Errorf("父意图 %s 不存在", parentID)
	}
	switch parent.Status {
	case model.StatusPending, model.StatusClaimed, model.StatusRunning:
	default:
		return model.Intent{}, false, fmt.Errorf("父意图 %s 是 %s：不能再委派", parentID, parent.Status)
	}
	child.ParentID = parentID
	child.Doc = parent.Doc // operator constraints are inherited, not authored by the delegator
	if child.Node == "" {
		child.Node = parent.Node
	}
	// A child's model tier is NOT inherited from the parent: an empty profile
	// goes through the normal default chain (project worker_profile → instance
	// active), so a strong-tier parent still delegates cheap worker-tier
	// children. Only an explicit `profile` on the child raises it (D89,
	// docs/17 §3.5.1).
	if child.CreatedBy == "" {
		child.CreatedBy = parent.CreatedBy
	}
	if child.IdempotencyKey != "" {
		for _, in := range s.db.Intents {
			if in.ParentID != parentID || in.IdempotencyKey != child.IdempotencyKey || in.CreatedBy != child.CreatedBy {
				continue
			}
			if in.Title != child.Title || in.Description != child.Description ||
				in.Eval != child.Eval || in.Profile != child.Profile ||
				!slices.Equal(in.Skills, child.Skills) ||
				in.MaxTurns != child.MaxTurns || !slices.Equal(in.FromFactIDs, child.FromFactIDs) ||
				!slices.Equal(in.ContextFactIDs, normalizeContextIDs(child.ContextFactIDs)) ||
				!slices.Equal(in.PinnedFactIDs, normalizeContextIDs(child.PinnedFactIDs)) {
				return model.Intent{}, false, ErrChildIdempotencyConflict
			}
			return detachIntent(in), false, nil
		}
	}
	if err := s.applyInitialContextLocked(&child); err != nil {
		return model.Intent{}, false, err
	}
	s.snapshotPremiseClaimsLocked(&child)
	now := time.Now()
	child.ID = NewID("in")
	child.Status = model.StatusPending
	child.Hold = false
	child.CreatedAt, child.UpdatedAt = now, now
	child = detachIntent(child)
	parent.UpdatedAt = now
	s.db.Intents = append(s.db.Intents, child)
	if err := s.flush("intent"); err != nil {
		return model.Intent{}, false, err
	}
	return detachIntent(child), true, nil
}

// Children returns the direct children of one intent, oldest first.
func (s *Store) Children(parentID string) []model.Intent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []model.Intent
	for _, in := range s.db.Intents {
		if in.ParentID == parentID {
			out = append(out, detachIntent(in))
		}
	}
	return out
}

// ActiveChildren returns children that are not terminal. A waiting child is
// still active: it may itself be waiting for grandchildren.
func (s *Store) ActiveChildren(parentID string) []model.Intent {
	var out []model.Intent
	for _, in := range s.Children(parentID) {
		if !model.Settled(in.Status) {
			out = append(out, in)
		}
	}
	return out
}

// Descendants returns the whole subtree below root, breadth-first, excluding
// root. It is what a cancel uses to stop a direction's work without leaving
// orphan children that can still write into a case nobody watches.
func (s *Store) Descendants(rootID string) []model.Intent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	byParent := map[string][]model.Intent{}
	for _, in := range s.db.Intents {
		byParent[in.ParentID] = append(byParent[in.ParentID], detachIntent(in))
	}
	var out []model.Intent
	queue := append([]model.Intent(nil), byParent[rootID]...)
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		out = append(out, cur)
		queue = append(queue, byParent[cur.ID]...)
	}
	return out
}

// SettleOutcome is what a run's completion means for its intent. Wake is true
// when the wait was already satisfied, so the caller can put the direction back
// on the queue instead of parking it in waiting.
type SettleOutcome struct {
	Status string
	Wake   bool
	Wait   *model.Wait
	// Stale is true when the intent already moved to a newer run; the caller
	// must not notify a parent or schedule a wake for this turn.
	Stale bool
}

// SettleIntentForRun applies one run's terminal status to its intent under the
// lock. It is the only place that turns "turn ended" into "done / waiting /
// pending / failed", so the child results and the parent state cannot disagree.
//
// A stale finish (the intent has moved to a newer run) changes nothing: a steered
// direction must not be marked done by the turn it already replaced.
func (s *Store) SettleIntentForRun(intentID, runID, runStatus string, cancelCause ...string) (SettleOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in, ok := s.intentLocked(intentID)
	if !ok {
		return SettleOutcome{}, fmt.Errorf("意图 %s 不存在", intentID)
	}
	if in.RunID != "" && in.RunID != runID {
		return SettleOutcome{Status: in.Status, Stale: true}, nil
	}
	now := time.Now()
	in.UpdatedAt = now
	if in.HandoffOperation != "" {
		// Only the replacement owner may consume the old wait. The old run can
		// finish normally or be canceled while BeginHandoff is stopping it.
		in.Status, in.ClaimedBy, in.Hold = model.StatusPending, "", true
		if runStatus == model.RunCanceled && in.Attempts > 0 {
			in.Attempts--
		}
		if err := s.flush("intent"); err != nil {
			return SettleOutcome{}, err
		}
		return SettleOutcome{Status: in.Status, Wait: detachWait(in.Wait)}, nil
	}
	if runStatus == model.RunCanceled && len(cancelCause) > 0 &&
		(cancelCause[0] == model.CancelCauseOperatorWait || cancelCause[0] == model.CancelCauseChildWait || cancelCause[0] == model.CancelCauseActionWait) &&
		in.Wait != nil && in.Wait.RegisteredByRun == runID && !in.Hold &&
		((cancelCause[0] == model.CancelCauseOperatorWait) == (in.Wait.Operator != nil)) &&
		((cancelCause[0] == model.CancelCauseActionWait) == (len(in.Wait.Actions) > 0)) {
		// The actual run stays canceled; only its business settlement follows
		// the normal yielded-turn path. A concurrent operator cancel that has
		// cleared the wait cannot accidentally be turned into success.
		runStatus = model.RunDone
	}
	switch runStatus {
	case model.RunDone:
		if in.Wait != nil && in.Wait.RegisteredByRun == runID {
			ready, results, _, reason := s.evaluateWaitLocked(in.Wait, now)
			if ready {
				in.Status = model.StatusPending
				in.Hold = false
				in.Wait.WakePending = true
				in.Wait.WakeReason = reason
				in.Wait.Results = results
				if err := s.flush("intent"); err != nil {
					return SettleOutcome{}, err
				}
				return SettleOutcome{Status: in.Status, Wake: true, Wait: detachWait(in.Wait)}, nil
			}
			in.Status = model.StatusWaiting
			if err := s.flush("intent"); err != nil {
				return SettleOutcome{}, err
			}
			return SettleOutcome{Status: in.Status, Wait: detachWait(in.Wait)}, nil
		}
		// A parent that finishes without registering a wait still has open
		// children: it cannot be done and orphan them. The implicit all-wait
		// wakes it with their results.
		if ids := s.activeChildIDsLocked(intentID); len(ids) > 0 {
			in.Wait = &model.Wait{
				Children: ids, Mode: model.WaitAll, RegisteredAt: now,
				RegisteredByRun: runID, Implicit: true,
			}
			in.Status = model.StatusWaiting
			in.Hold = false
			if err := s.flush("intent"); err != nil {
				return SettleOutcome{}, err
			}
			return SettleOutcome{Status: in.Status, Wait: detachWait(in.Wait)}, nil
		}
		in.Status = model.StatusDone
		in.Hold = false
		in.Wait = nil
	case model.RunCanceled:
		in.Wait = nil
		hold := in.Aborted || len(cancelCause) > 0 && model.CancelHoldsIntent(cancelCause[0])
		// Publish an explicit stop and its Hold together. A separate follow-up
		// write would expose pending work to the scheduler between commits.
		if hold {
			in.Status = model.StatusFailed
			in.Hold = true
			in.ClaimedBy = ""
		} else {
			in.Status = model.StatusPending
			in.ClaimedBy = ""
		}
		if in.Attempts > 0 {
			in.Attempts--
		}
	case model.RunFailed:
		in.Status = model.StatusFailed
		in.Hold = false
		in.Wait = nil
	default:
		in.Status = model.StatusFailed
		in.Wait = nil
	}
	if err := s.flush("intent"); err != nil {
		return SettleOutcome{}, err
	}
	return SettleOutcome{Status: in.Status}, nil
}

func (s *Store) activeChildIDsLocked(parentID string) []string {
	var out []string
	for i := range s.db.Intents {
		child := s.db.Intents[i]
		if child.ParentID == parentID && !model.Settled(child.Status) {
			out = append(out, child.ID)
		}
	}
	return out
}

// WaitState is the result of registering or evaluating a wait.
type WaitState struct {
	Ready   bool
	Reason  string
	Results []model.WaitResult
	Pending []string
}

// RegisterWait records a wait on one intent. If the condition is already
// satisfied (a child finished before the parent registered), it returns ready
// immediately and stores no wait: the parent's current turn can continue.
//
// Child results are collected while the same lock is held, so a concurrent
// finish cannot slip between "evaluate" and "record the wait".
func (s *Store) RegisterWait(intentID, runID string, wait model.Wait) (WaitState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if wait.Operator != nil {
		return WaitState{}, fmt.Errorf("%w：人工条件必须通过人工交接接口登记", ErrWaitInvalid)
	}
	in, ok := s.intentLocked(intentID)
	if !ok {
		return WaitState{}, fmt.Errorf("意图 %s 不存在", intentID)
	}
	if runID != "" && in.RunID != "" && in.RunID != runID {
		return WaitState{}, fmt.Errorf("%w：执行轮次已更换", ErrWaitInvalid)
	}
	switch in.Status {
	case model.StatusDone, model.StatusFailed, model.StatusReviewFailed, model.StatusParked:
		return WaitState{}, fmt.Errorf("意图 %s 是 %s：不再等待", intentID, in.Status)
	}
	if (len(wait.Children) == 0) == (len(wait.Actions) == 0) {
		return WaitState{}, fmt.Errorf("%w：只能选择一组 children 或 actions", ErrWaitInvalid)
	}
	if in.Wait != nil && in.Wait.Operator != nil {
		return WaitState{}, fmt.Errorf("%w：正在等待人工操作，不能覆盖", ErrWaitInvalid)
	}
	if wait.Mode == "" {
		wait.Mode = model.WaitAll
	}
	if wait.Mode != model.WaitAll && wait.Mode != model.WaitAny {
		return WaitState{}, fmt.Errorf("%w：mode 只能是 all 或 any", ErrWaitInvalid)
	}
	seen := map[string]bool{}
	for _, id := range wait.Actions {
		if id == "" || seen[id] {
			return WaitState{}, fmt.Errorf("%w：动作 id 为空或重复", ErrWaitInvalid)
		}
		seen[id] = true
		if _, ok := s.actionLocked(id); !ok {
			return WaitState{}, fmt.Errorf("%w：外部动作 %s 不存在", ErrWaitInvalid, id)
		}
	}
	for _, id := range wait.Children {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			return WaitState{}, fmt.Errorf("%w：子意图 id 为空或重复", ErrWaitInvalid)
		}
		seen[id] = true
		child, ok := s.intentLocked(id)
		if !ok || child.ParentID != intentID {
			return WaitState{}, fmt.Errorf("%w：%s 不是 %s 的子意图", ErrWaitInvalid, id, intentID)
		}
	}
	now := time.Now()
	wait.RegisteredAt = now
	wait.RegisteredByRun = runID
	wait.WakePending = false
	wait.WakeReason = ""
	ready, results, pending, reason := s.evaluateWaitLocked(&wait, now)
	if ready {
		// The condition is already true. A running turn keeps running with the
		// results in hand; a waiting direction is put back on the queue. Drop a
		// wait from an earlier registration: otherwise the turn's finish would
		// later see it, wake the direction a second time, and duplicate the run.
		changed := in.Wait != nil
		in.Wait = nil
		if in.Status == model.StatusWaiting || in.Status == model.StatusPending {
			in.Status = model.StatusPending
			in.Hold = false
			changed = true
		}
		if changed {
			in.UpdatedAt = now
			if err := s.flush("intent"); err != nil {
				return WaitState{}, err
			}
		}
		return WaitState{Ready: true, Reason: reason, Results: results, Pending: pending}, nil
	}
	wait.Results = results
	in.Wait = detachWait(&wait)
	if in.Status == model.StatusPending {
		in.Status = model.StatusWaiting
	}
	in.UpdatedAt = now
	if err := s.flush("intent"); err != nil {
		return WaitState{}, err
	}
	return WaitState{Ready: false, Reason: reason, Results: results, Pending: pending}, nil
}

// EvaluateWait recomputes a stored wait without changing it. It is used when a
// child settles and when the watchdog checks deadlines.
func (s *Store) EvaluateWait(intentID string) (WaitState, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	in, ok := s.intentLocked(intentID)
	if !ok || in.Wait == nil {
		return WaitState{}, false
	}
	ready, results, pending, reason := s.evaluateWaitLocked(in.Wait, time.Now())
	return WaitState{Ready: ready, Reason: reason, Results: results, Pending: pending}, true
}

// SatisfyWait marks a stored wait as ready to wake and records the structured
// results. It is called after a child settles.
func (s *Store) SatisfyWait(intentID string) (model.Intent, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in, ok := s.intentLocked(intentID)
	if !ok || in.Wait == nil {
		return model.Intent{}, false, nil
	}
	ready, results, _, reason := s.evaluateWaitLocked(in.Wait, time.Now())
	if !ready {
		return detachIntent(*in), false, nil
	}
	wait := *in.Wait
	wait.WakePending = true
	wait.WakeReason = reason
	wait.Results = results
	in.Wait = detachWait(&wait)
	now := time.Now()
	in.UpdatedAt = now
	if err := s.flush("intent"); err != nil {
		return model.Intent{}, false, err
	}
	return detachIntent(*in), true, nil
}

func (s *Store) intentLocked(id string) (*model.Intent, bool) {
	for i := range s.db.Intents {
		if s.db.Intents[i].ID == id {
			return &s.db.Intents[i], true
		}
	}
	return nil, false
}

// evaluateWaitLocked is the single definition of readiness. Caller holds s.mu.
func (s *Store) evaluateWaitLocked(w *model.Wait, now time.Time) (ready bool, results []model.WaitResult, pending []string, reason string) {
	if len(w.Actions) > 0 {
		return s.evaluateActionsLocked(w, now)
	}
	if w.Operator != nil {
		if w.Operator.Resolution == model.OperatorUnavailable {
			return w.Operator.CompletedAt != nil, nil, nil, "operator_unavailable"
		}
		return w.Operator.CompletedAt != nil, nil, nil, "operator_completed"
	}
	if w.Deadline != nil && !now.Before(*w.Deadline) {
		// A timeout is a result too: every child is reported with its current
		// status, and the open ones are listed as pending.
		for _, id := range w.Children {
			if child, ok := s.intentLocked(id); ok {
				results = append(results, s.waitResultLocked(*child))
				if !model.Settled(child.Status) {
					pending = append(pending, id)
				}
			} else {
				results = append(results, model.WaitResult{ChildID: id, Status: "missing", At: now})
			}
		}
		return true, results, pending, model.WaitReasonTimeout
	}
	for _, id := range w.Children {
		child, ok := s.intentLocked(id)
		if !ok {
			results = append(results, model.WaitResult{ChildID: id, Status: "missing", At: now})
			continue
		}
		if model.Settled(child.Status) {
			results = append(results, s.waitResultLocked(*child))
		} else {
			pending = append(pending, id)
		}
	}
	switch w.Mode {
	case model.WaitAny:
		ready = len(results) > 0
	default:
		ready = len(pending) == 0
	}
	if ready {
		reason = model.WaitReasonChildren
	}
	return ready, results, pending, reason
}

func (s *Store) waitResultLocked(child model.Intent) model.WaitResult {
	out := model.WaitResult{ChildID: child.ID, Status: child.Status, RunID: child.RunID, At: time.Now()}
	if child.RunID != "" {
		for _, r := range s.db.Runs {
			if r.ID == child.RunID {
				out.RunID = r.ID
				out.StopReason = r.StopReason
				if out.StopReason == "" {
					out.StopReason = r.ExitReason
				}
				break
			}
		}
	}
	for _, f := range s.db.Facts {
		if f.IntentID != child.ID {
			continue
		}
		out.FactIDs = append(out.FactIDs, f.ID)
		out.EvidenceIDs = append(out.EvidenceIDs, f.EvidenceIDs...)
	}
	return out
}
