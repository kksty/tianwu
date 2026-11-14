package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/project"
	"github.com/kksty/tianwu/internal/store"
)

// Machine-readable project states for the explicit "append a goal and continue"
// action. A plain "paused" boolean cannot tell a completion stand-by (the
// chartered work finished) from a manual pause (the operator stopped it) from a
// credential fault (a human must repair the key/profile first): the first two
// are resumable by this explicit action, the fault is refused, and waiting work
// is not a pause at all. The console switches its copy on these codes instead of
// parsing the human-readable reason.
const (
	pauseKindRunning           = "running"
	pauseKindWaitingHuman      = "waiting_human"
	pauseKindWaitingChildren   = "waiting_children"
	pauseKindWaitingAction     = "waiting_action"
	pauseKindWaitingResume     = "waiting_resume"
	pauseKindCompletionStandby = project.PauseKindCompletionStandby
	pauseKindManualPause       = project.PauseKindManualPause
	pauseKindCommanderStandby  = project.PauseKindCommanderStandby
	pauseKindProviderFault     = "provider_fault"
)

// Structured causes written by the pause paths in api.go. They are deliberately
// not the console prose: the reason is for people, the cause is what the resume
// contract switches on.
const (
	pauseCauseManual     = "manual"
	pauseCauseCompletion = "completion"
	// stopReason* are the structured reasons a commander may attach to
	// POST /api/project/standby. goal_achieved is the only one the control plane
	// can check against the ledger: it is refused while any goal is still open,
	// so "目标已达成" cannot be a prose claim without a stamp.
	stopReasonGoalAchieved = "goal_achieved"
	stopReasonBlocked      = "blocked"
	stopReasonLowValue     = "low_value"
	// pauseCauseCommander is the commander's explicit "stop this engagement"
	// request (POST /api/project/standby). It is a real pause: persists across
	// restarts, cancels executing directions, and only the operator can resume.
	// It is distinct from the operator's own pause so the console can say who
	// stopped the project instead of guessing from the reason text.
	pauseCauseCommander = "commander"
)

// projectState is the classified parking state of an engagement. Reason is the
// decider's human-readable pause reason (free text, for display only); Kind is
// the machine code the append-and-continue contract uses.
type projectState struct {
	Paused bool
	Kind   string
	Reason string
}

// projectState is projectStateLocked with the control mutex taken. Readers that
// already hold controlMu (the append-and-continue section) must call the locked
// variant instead: pauseCause is guarded by that mutex.
func (s *Server) projectState() projectState {
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	return s.projectStateLocked()
}

// projectStateLocked classifies the current pause without guessing from prose
// where a structured cause exists:
//
//   - a completion stand-by, a manual pause, and a commander stop request are
//     recorded in pauseCause by the paths that apply them;
//   - a boot/restart pause (attachState) writes the manifest flag but no runtime
//     cause, so a paused project with a persisted flag and no cause is reported
//     as manual: it needs the same explicit human resume;
//   - the credential-fault path is the only production path that pauses dispatch
//     without persisting the manifest flag (a human must repair the key first),
//     so "runtime paused while the manifest still says running" is the fault;
//   - not paused but with a waiting direction is waiting_human: live work that
//     must not be disturbed, not a park.
func (s *Server) projectStateLocked() projectState {
	st := projectState{Paused: s.paused.Load()}
	if s.Decider != nil {
		st.Reason = s.Decider.Status().PausedReason
	}
	if st.Paused {
		switch s.pauseCause {
		case pauseCauseCompletion:
			st.Kind = pauseKindCompletionStandby
		case pauseCauseCommander:
			st.Kind = pauseKindCommanderStandby
		case pauseCauseManual:
			st.Kind = pauseKindManualPause
		default:
			if s.Project != nil && !s.Project.IsPaused() {
				st.Kind = pauseKindProviderFault
			} else {
				st.Kind = pauseKindManualPause
			}
		}
		return st
	}
	st.Kind = pauseKindRunning
	if s.Store != nil {
		for _, in := range s.Store.ListIntents() {
			if in.Status == model.StatusWaiting {
				if in.Wait == nil || in.Wait.WakePending || (in.Wait.Operator != nil && in.Wait.Operator.CompletedAt != nil) {
					if st.Kind == pauseKindRunning {
						st.Kind = pauseKindWaitingResume
					}
					continue
				}
				if in.Wait != nil && in.Wait.Operator != nil && in.Wait.Operator.CompletedAt == nil {
					st.Kind = pauseKindWaitingHuman
					break
				}
				if in.Wait != nil && len(in.Wait.Actions) > 0 {
					st.Kind = pauseKindWaitingAction
				} else if st.Kind == pauseKindRunning {
					st.Kind = pauseKindWaitingChildren
				}
			}
		}
	}
	return st
}

// resumeScope is the one-shot target attached by the operator's "continue
// current goal" action. StartCommander consumes it and injects a section into
// the next commander prompt, so the round knows it is continuing that open goal
// without a persistent instruction or a chat message.
type resumeScope struct {
	GoalID    string
	Statement string
}

func (s *Server) setResumeScope(scope resumeScope) {
	if s == nil || strings.TrimSpace(scope.GoalID) == "" {
		return
	}
	s.scopeMu.Lock()
	defer s.scopeMu.Unlock()
	sc := scope
	s.resumeScope = &sc
}

func (s *Server) consumeResumeScope() *resumeScope {
	if s == nil {
		return nil
	}
	s.scopeMu.Lock()
	defer s.scopeMu.Unlock()
	sc := s.resumeScope
	s.resumeScope = nil
	return sc
}

// appendGoalAndContinue is the explicit operator action for a finished or parked
// engagement: append a NEW independent goal and, when the project is parked in a
// resumable stand-by (completion, a manual pause, or the commander's own stop
// request this request observes), clear the pause and wake the commander so the
// new goal is acted on. It never touches a stamped goal's receipt: completion
// history stays exactly what was claimed.
//
// It is not reachable from the harness surface: the route lives under
// /api/project/, which the agent-token gate (cmd/tw auth.agentRouteHidden)
// treats as operator-only, and the participant mux never registers it.
//
// The body carries an idempotency_key: a retry after a lost response or after a
// failed resume finds the goal it already created instead of appending a second
// one, then completes (or reports again) the resume. Fault-paused projects are
// refused before the goal is written, so a refused request has no side effect.
func (s *Server) appendGoalAndContinue(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Statement        string `json:"statement"`
		Start            string `json:"start"`
		QuantifiedTarget string `json:"quantified_target"`
		IdempotencyKey   string `json:"idempotency_key"`
		Note             string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json: " + err.Error(), "code": "goal_invalid"})
		return
	}
	req.Statement = strings.TrimSpace(req.Statement)
	req.Start = strings.TrimSpace(req.Start)
	req.QuantifiedTarget = strings.TrimSpace(req.QuantifiedTarget)
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	req.Note = strings.TrimSpace(req.Note)
	switch {
	case req.Statement == "":
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "statement 不能为空：目标要能判断真假", "code": "goal_invalid"})
		return
	case req.IdempotencyKey == "":
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "idempotency_key 必填：响应丢失或恢复失败后要用同一个 key 重试，不能重复追加目标",
			"code":  "goal_invalid"})
		return
	case s.Store == nil:
		writeErr(w, http.StatusServiceUnavailable, "这个项目没有账本，不能追加目标")
		return
	}

	// Serialise classify+resume with projectPause/AutoPause/projectResume: the
	// explicit "continue" must not race a concurrent pause into a half-applied
	// state. The append runs in the same section; a goal write is a short SQLite
	// transaction.
	s.controlMu.Lock()
	defer s.controlMu.Unlock()

	before := s.projectStateLocked()
	if before.Kind == pauseKindProviderFault {
		// A credential fault is not ours to clear: a human must fix the key or
		// switch the profile first. Refuse before writing anything, so the
		// refusal leaves the ledger untouched.
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":             "项目因模型/凭据故障而暂停：先修复 key 或切换 profile，再用同一个 idempotency_key 重试",
			"code":              "project_fault_paused",
			"pause_kind":        before.Kind,
			"pause_kind_before": before.Kind,
			"pause_reason":      before.Reason,
			"created":           false,
			"resumed":           false,
			"needs_human":       true,
		})
		return
	}

	goal, created, err := s.Store.AddGoalIdempotent(model.Goal{
		Statement:        req.Statement,
		Start:            req.Start,
		QuantifiedTarget: req.QuantifiedTarget,
		IdempotencyKey:   req.IdempotencyKey,
	})
	if err != nil {
		if errors.Is(err, store.ErrGoalIdempotencyConflict) {
			writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "code": "goal_idempotency_conflict"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": "追加目标失败：" + err.Error(), "code": "goal_append_failed"})
		return
	}
	if created {
		text := goal.Statement
		if req.Note != "" {
			text += "（" + req.Note + "）"
		}
		s.recordAction("goal", goal.ID, text)
	}

	// Re-classify after the append: the credential-fault path does not take
	// controlMu, so a fault that landed during this request must still keep the
	// project paused. The goal stays (retrying with the same key finds it) and a
	// retry after the human fix completes the resume.
	current := s.projectStateLocked()
	if current.Paused && current.Kind == pauseKindProviderFault {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":             "目标已追加，但项目在请求期间因模型/凭据故障暂停：未恢复项目；修复后用同一个 idempotency_key 重试会找回该目标并继续",
			"code":              "project_fault_paused",
			"goal":              goal,
			"created":           created,
			"resumed":           false,
			"retryable":         true,
			"pause_kind_before": before.Kind,
			"pause_kind_after":  current.Kind,
			"pause_reason":      current.Reason,
		})
		return
	}

	resumedFrom := ""
	if current.Paused {
		switch current.Kind {
		case pauseKindCompletionStandby, pauseKindManualPause, pauseKindCommanderStandby:
			resumedFrom = current.Kind
		}
	}
	resumed := false
	if resumedFrom != "" {
		if err := s.setProjectPaused(false); err != nil {
			// The manifest write failed, so the runtime pause was never flipped:
			// the project stays paused exactly as the manifest still says. The
			// goal exists; a retry with the same key finds it and retries the
			// resume, so the failure is reported as retryable, not final.
			after := s.projectStateLocked()
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error":             "目标已追加，但无法保存继续状态，项目保持暂停；用同一个 idempotency_key 重试会找回该目标并完成恢复：" + err.Error(),
				"code":              "resume_failed",
				"goal":              goal,
				"created":           created,
				"resumed":           false,
				"retryable":         true,
				"pause_kind_before": before.Kind,
				"pause_kind_after":  after.Kind,
				"pause_reason":      after.Reason,
			})
			return
		}
		s.resumeProjectRuntimeLocked()
		resumed = true
		// Same kick as projectResume: the scheduler was gated while paused, so
		// pending children and satisfied waits need one explicit sweep.
		go s.pump()
	}

	woken := false
	if (created || resumed) && s.Decider != nil && !s.Decider.Paused() && !s.paused.Load() {
		// A forced wake, not a heartbeat: the commander must act on the new goal
		// without waiting for the board digest to move. A duplicate request that
		// created and resumed nothing does not wake a second time.
		s.Decider.Wake("operator")
		woken = true
	}

	after := s.projectStateLocked()
	note := ""
	switch {
	case resumed && resumedFrom == pauseKindCompletionStandby:
		note = "项目此前处于完成待命，已追加目标并显式恢复派发；旧目标的完成收据保持不变。"
	case resumed && resumedFrom == pauseKindCommanderStandby:
		note = "项目此前由指挥官请求停止，已按本次显式操作追加目标并恢复派发；指挥官请求停止的原因仍在它的暂停说明里。"
	case resumed:
		note = "项目此前是人工暂停，已按本次显式操作恢复；旧目标的完成收据保持不变。"
	case !created:
		note = "这个 idempotency_key 已创建过该目标：没有重复追加。"
	case before.Kind == pauseKindWaitingHuman:
		note = "项目未暂停（有方向在等待人工）：只追加了目标，没有改动运行/等待状态。"
	case before.Kind == pauseKindWaitingChildren, before.Kind == pauseKindWaitingAction:
		note = "目标已追加；现有子任务或外部结果等待继续保留，无需为此人工确认。"
	default:
		note = "项目在运行中：只追加了目标，没有改动在跑方向。"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"goal":              goal,
		"created":           created,
		"resumed":           resumed,
		"resumed_from":      resumedFrom,
		"decider_woken":     woken,
		"decider_paused":    s.Decider != nil && s.Decider.Paused(),
		"paused_before":     before.Paused,
		"pause_kind_before": before.Kind,
		"paused_after":      after.Paused,
		"pause_kind_after":  after.Kind,
		"pause_reason":      after.Reason,
		"retryable":         false,
		"note":              note,
	})
}
