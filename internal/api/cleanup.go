package api

import (
	"context"
	"github.com/kksty/tianwu/internal/aopexec"
	"github.com/kksty/tianwu/internal/model"
	"path/filepath"
	"time"
)

// Cleanup uses the harness's existing active-session inbox. It reserves part
// of the already configured timeout and never extends a pause or hard deadline.
func (s *Server) startCleanupWindow(ctx context.Context, runID, intentID, workDir string) func() {
	deadline, ok := ctx.Deadline()
	if !ok || s.Timeout <= 0 || (s.Say == nil && s.SayEvent == nil) {
		return func() {}
	}
	remaining := time.Until(deadline)
	reserve := remaining / 4
	if reserve > 90*time.Second {
		reserve = 90 * time.Second
	}
	if reserve <= 0 {
		return func() {}
	}
	stopCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		timer := time.NewTimer(remaining - reserve)
		defer timer.Stop()
		select {
		case <-stopCtx.Done():
			return
		case <-timer.C:
		}
		in, ok := s.Store.GetIntent(intentID)
		if !ok || in.RunID != runID || stopCtx.Err() != nil {
			return
		}
		sess, ok := aopexec.ReadSession(filepath.Clean(workDir))
		if !ok {
			return
		}
		_, _ = s.Store.UpdateRun(runID, func(r *model.Run) { r.CleanupPhase = "requested" })
		text := "运行机制通知：本轮已进入原有超时预算内的证据收尾阶段。停止新增探测，保留已取得的工具原件与证据，按事实交接契约提交明确观察并更新 worker-note，标明仍未完成或结果未知的部分，然后结束本轮。推测和残余日志摘要不自动成为事实；授权与验收口径不变。"
		var receipt string
		var err error
		if s.SayEvent != nil {
			receipt, err = s.SayEvent(stopCtx, in, sess, text, true, "cleanup:"+runID)
		} else {
			receipt, err = s.Say(stopCtx, in, sess, text, true)
		}
		phase := "accepted"
		detail := ""
		if err != nil {
			phase = "delivery_failed"
			detail = err.Error()
		}
		_, _ = s.Store.UpdateRun(runID, func(r *model.Run) { r.CleanupPhase = phase })
		_ = s.appendEvent(runID, map[string]any{"kind": "cleanup", "at": time.Now(), "phase": phase, "receipt": receipt, "error": s.redactSecrets(detail), "remaining_ms": time.Until(deadline).Milliseconds()})
	}()
	return func() { stop(); <-done }
}
