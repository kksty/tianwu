package store

import (
	"fmt"
	"strings"
	"time"

	"github.com/kksty/tianwu/internal/model"
)

// RegisterOperatorWait persists a business yield before the API interrupts
// the current execution. Settlement preserves the outstanding condition.
func (s *Store) RegisterOperatorWait(intentID, runID string, request model.OperatorWait) (model.OperatorWait, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in, ok := s.intentLocked(intentID)
	if !ok || in.Status != model.StatusRunning || in.RunID != runID {
		return model.OperatorWait{}, fmt.Errorf("只有本方向当前运行的 turn 可以请求人工操作")
	}
	request.Request = strings.TrimSpace(request.Request)
	if request.Request == "" || len([]rune(request.Request)) > 2000 {
		return model.OperatorWait{}, fmt.Errorf("人工操作说明应为 1–2000 字")
	}
	if in.Wait != nil {
		if in.Wait.Operator != nil && in.Wait.Operator.Request == request.Request &&
			strings.Join(in.Wait.Operator.AccessIDs, "\x00") == strings.Join(request.AccessIDs, "\x00") {
			return *detachWait(in.Wait).Operator, nil
		}
		return model.OperatorWait{}, fmt.Errorf("已有等待条件：不能覆盖子任务或另一项人工操作")
	}
	request.ID = NewID("op")
	request.Response = ""
	request.Resolution = ""
	request.CompletedAt = nil

	in.Wait = detachWait(&model.Wait{Operator: &request, RegisteredByRun: runID, RegisteredAt: time.Now()})
	in.UpdatedAt = time.Now()
	if err := s.flush("intent"); err != nil {
		return model.OperatorWait{}, err
	}
	return request, nil
}

// CompleteOperatorWait uses the request identity, so a stale browser tab cannot
// release a later CAPTCHA or a replacement run's unrelated request.
func (s *Store) CompleteOperatorWait(intentID, requestID, response string) error {
	return s.ResolveOperatorWait(intentID, requestID, response, model.OperatorCompleted)
}

// AddOperatorAccess binds a newly prepared browser to the exact pending human
// request. A stale console cannot attach material to a replacement request.
func (s *Store) AddOperatorAccess(intentID, requestID, accessID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	in, ok := s.intentLocked(intentID)
	if !ok || in.Wait == nil || in.Wait.Operator == nil || in.Wait.Operator.ID != requestID || in.Wait.Operator.CompletedAt != nil {
		return fmt.Errorf("人工操作请求已失效，请刷新")
	}
	for _, id := range in.Wait.Operator.AccessIDs {
		if id == accessID {
			return nil
		}
	}
	if len(in.Wait.Operator.AccessIDs) >= 16 {
		return fmt.Errorf("关联登录现场过多")
	}
	previous, updatedAt := in.Wait, in.UpdatedAt
	wait := detachWait(in.Wait)
	wait.Operator.AccessIDs = append(wait.Operator.AccessIDs, accessID)
	in.Wait, in.UpdatedAt = wait, time.Now()
	if err := s.flush("intent"); err != nil {
		in.Wait, in.UpdatedAt = previous, updatedAt
		return err
	}
	return nil
}

// ResolveOperatorWait answers the human request without treating an unavailable
// prerequisite as a completed action. The original direction resumes to assess
// what can still be done within its authorization and actual access.
func (s *Store) ResolveOperatorWait(intentID, requestID, response, resolution string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if resolution == "" {
		resolution = model.OperatorCompleted
	}
	if resolution != model.OperatorCompleted && resolution != model.OperatorUnavailable {
		return fmt.Errorf("人工回执结果只能是 completed 或 unavailable")
	}
	response = strings.TrimSpace(response)
	if resolution == model.OperatorUnavailable && response == "" {
		return fmt.Errorf("无法完成时必须说明原因，例如没有账号或本次仅测未登录面")
	}
	in, ok := s.intentLocked(intentID)
	if !ok || in.Wait == nil || in.Wait.Operator == nil || in.Wait.Operator.ID != requestID {
		return fmt.Errorf("人工操作请求已失效，请刷新")
	}
	if in.Status != model.StatusRunning && in.Status != model.StatusWaiting &&
		!(in.Status == model.StatusPending && in.HandoffOperation != "") {
		return fmt.Errorf("方向已不在等待人工操作")
	}
	if len([]rune(response)) > 2000 {
		return fmt.Errorf("完成说明不能超过 2000 字")
	}
	if in.Wait.Operator.CompletedAt != nil {
		previous := in.Wait.Operator.Resolution
		if previous == "" {
			previous = model.OperatorCompleted
		}
		if previous != resolution {
			return fmt.Errorf("人工请求已经收到另一种结果，请刷新")
		}
		return nil
	}
	now := time.Now()

	// Readers hold detached intent values. Replace nested state instead of
	// mutating a pointer a board render or scheduler may already be reading.
	wait := *in.Wait
	operator := *wait.Operator
	operator.Response = strings.TrimSpace(response)
	operator.Resolution = resolution
	operator.CompletedAt = &now
	wait.Operator = &operator
	in.Wait = &wait
	in.UpdatedAt = now
	if err := s.flush("intent"); err != nil {
		return err
	}
	return nil
}
