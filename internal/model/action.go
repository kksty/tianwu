package model

import "time"

const (
	ActionStarted         = "started"
	ActionCompleted       = "completed"
	ActionUnknown         = "unknown"
	CancelCauseActionWait = "action_wait"
	WaitReasonActions     = "actions_settled"
)

// ExternalAction is execution history, never a security verdict or a fact.
// Completed means a result was recorded; it does not mean the action succeeded.
type ExternalAction struct {
	ID            string    `json:"id"`
	IntentID      string    `json:"intent_id"`
	RunID         string    `json:"run_id"`
	Generation    int       `json:"generation"`
	Key           string    `json:"idempotency_key"`
	Name          string    `json:"name"`
	Reference     string    `json:"reference,omitempty"`
	State         string    `json:"state"`
	Version       int       `json:"version"`
	Result        string    `json:"result,omitempty"`
	ResultRef     string    `json:"result_ref,omitempty"`
	ResolvedByRun string    `json:"resolved_by_run,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}
