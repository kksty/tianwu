package api

// One automatic recovery for a harness-reported context limit (D152, revised).
//
// Tianwu does not predict the model window. The harness refuses a request that
// cannot fit (context_window_exhausted) and compacts once on an overflow before
// failing. When that structured failure reaches dispatch, the one useful action
// is a fresh attempt with a smaller brief: hard constraints and the handoff
// contract stay, optional summaries/indexes become stable ids and API pointers.
// It happens at most once per failed run, and never for a resume/wake whose
// session history is the thing that overflowed (harness compaction owns that).

import (
	"log"
	"strings"
	"time"

	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
)

// contextLimitMarkers are provider/harness contract strings, not model output:
// the harness's own error, and the stable wording of the providers it fronts.
// They are read only after a run has already failed, and only to choose between
// "retry smaller" and "leave the failure alone".
var contextLimitMarkers = []string{
	"context_window_exhausted",
	"context_length_exceeded",
	"maximum context length",
	"prompt is too long",
	"input is too long",
	"exceeds the context window",
	"exceeded the context window",
	"input length and",
	"context overflow",
	"too many tokens",
	"上下文长度",
	"上下文窗口",
}

// contextLimitFailure reports whether this failed turn was the model/window
// refusing the request size. It never fires for a successful or canceled run.
func contextLimitFailure(res executor.Result, runErr error, reason, journalEvidence string) bool {
	failed := runErr != nil || res.ExitCode != 0 || res.IsError ||
		strings.EqualFold(strings.TrimSpace(res.StopReason), "error")
	if !failed {
		return false
	}
	text := strings.ToLower(strings.Join([]string{
		res.Output, res.StopReason, reason, journalEvidence,
	}, "\n"))
	for _, marker := range contextLimitMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func (s *Server) armSlimRetry(intentID string) {
	s.slimRetryMu.Lock()
	defer s.slimRetryMu.Unlock()
	if s.slimRetryFor == nil {
		s.slimRetryFor = map[string]bool{}
	}
	s.slimRetryFor[intentID] = true
}

// takeSlimRetry consumes the flag: exactly one dispatch builds the degraded
// brief. A second context failure is left to the operator/commander.
func (s *Server) takeSlimRetry(intentID string) bool {
	s.slimRetryMu.Lock()
	defer s.slimRetryMu.Unlock()
	if !s.slimRetryFor[intentID] {
		return false
	}
	delete(s.slimRetryFor, intentID)
	return true
}

func (s *Server) clearSlimRetry(intentID string) {
	s.slimRetryMu.Lock()
	delete(s.slimRetryFor, intentID)
	s.slimRetryMu.Unlock()
}

// retryWithSlimPrompt is the one automatic retry after a harness-reported
// context limit. It mirrors fallbackToFreshSession: record the reason on the
// failed run, requeue unconfirmed messages (the new session has no history),
// release the direction and start a fresh dispatch whose brief omits optional
// summaries. It only acts while the failed run still owns the direction, so a
// concurrent retry/handoff cannot be clobbered.
func (s *Server) retryWithSlimPrompt(oldRunID, intentID, reason string) {
	cur, ok := s.Store.GetIntent(intentID)
	if !ok || cur.Status != model.StatusFailed || cur.RunID != oldRunID {
		return // a newer run already took the direction
	}
	const warning = "harness 报告上下文超限：已自动改用精简任务书重试一次（可选摘要转 id/API 指针，硬约束与交接契约保留）"
	if _, err := s.Store.UpdateRun(oldRunID, func(r *model.Run) {
		r.Warnings = append(r.Warnings, warning)
	}); err != nil {
		log.Printf("run %s: 记录上下文精简重试警告失败：%v", oldRunID, err)
	}
	if err := s.appendEvent(oldRunID, map[string]any{
		"kind": "context_slim_retry", "at": time.Now(), "intent_id": intentID,
		"from_run": oldRunID, "reason": clipRunes(reason, 300),
	}); err != nil {
		log.Printf("run %s: 记录上下文精简重试事件失败：%v", oldRunID, err)
	}
	// A fresh session has no conversation memory: anything the failed run may
	// have injected but not had read must ride the retry.
	if n, err := s.Store.RedeliverAllUnconfirmed(collab.ParticipantID(intentID)); err != nil {
		log.Printf("上下文精简重试 %s: 重投未确认消息失败：%v", intentID, err)
	} else if n > 0 {
		log.Printf("上下文精简重试 %s: %d 条未确认消息回队", intentID, n)
	}
	prepared, err := s.Store.UpdateIntent(intentID, func(i *model.Intent) {
		if i.RunID != oldRunID || i.Status != model.StatusFailed {
			return
		}
		i.Status = model.StatusPending
		// Reserve the direction for this explicit retry so the pump cannot win
		// the claim with a full-size brief; the claim clears Hold.
		i.Hold = true
		i.ClaimedBy = ""
	})
	if err != nil {
		log.Printf("上下文精简重试 %s: 准备新会话失败：%v", intentID, err)
		return
	}
	if prepared.Status != model.StatusPending || prepared.RunID != oldRunID {
		return
	}
	s.armSlimRetry(intentID)
	runID, err := s.startRunWith(intentID, "", "", false, false)
	if err != nil {
		s.clearSlimRetry(intentID)
		log.Printf("上下文精简重试 %s: 新会话启动失败：%v", intentID, err)
		return
	}
	log.Printf("run %s: 上下文超限，已用精简任务书重试 run %s", oldRunID, runID)
}
