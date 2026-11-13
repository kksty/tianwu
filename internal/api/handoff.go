package api

// Model handoff: replace the current session with a new one on another profile,
// carrying a compact snapshot instead of the old conversation.
//
// The old model's transcript stays in its run log. The new session gets the
// full brief plus this snapshot (facts/evidence/attempts/children/wait/message
// cursor and a short note). HandoffVersion is an optimistic concurrency check:
// two turns cannot both replace the same direction.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

type handoffReq struct {
	Profile string `json:"profile"`
	Note    string `json:"note"`
	// ExpectedVersion is the HandoffVersion the caller saw. The first handoff
	// uses 0.
	ExpectedVersion int `json:"expected_version"`
}

func (s *Server) handoffIntent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	in, ok := s.Store.GetIntent(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "意图不存在")
		return
	}
	if c := s.caller(r); c.agent {
		if c.participant != collab.ParticipantID(id) {
			writeErr(w, http.StatusForbidden, "只能为自己的意图办理交接")
			return
		}
		if in.Status == model.StatusRunning && in.RunID != c.runID {
			writeErr(w, http.StatusConflict, "这个 turn 不是当前承接者")
			return
		}
	}
	var req handoffReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是 JSON")
		return
	}
	updated, handoff, runID, err := s.HandoffIntent(r.Context(), id, req.Profile, req.Note, req.ExpectedVersion)
	if err != nil {
		status := http.StatusBadRequest
		switch {
		case errors.Is(err, store.ErrHandoffConflict):
			status = http.StatusConflict
		case strings.Contains(err.Error(), "已结束"):
			status = http.StatusConflict
		}
		writeErr(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"intent": updated, "handoff": handoff, "run_id": runID,
	})
}

// HandoffIntent stops the old turn (if any), writes a versioned snapshot and
// starts a new session on toProfile with a fresh brief.
//
// Ordering is part of the contract: the version CAS and the handoff Hold are
// taken before any side effect, so a stale or racing request never cancels the
// live run. Hold closes the window where pump could restart the direction while
// the old run is being stopped; the successful claim of the replacement run
// clears it (store.ClaimIntent), and an aborted handoff releases it explicitly.
func (s *Server) HandoffIntent(ctx context.Context, id, toProfile, note string, expectedVersion int) (model.Intent, model.Handoff, string, error) {
	toProfile = strings.TrimSpace(toProfile)
	if toProfile == "" {
		return model.Intent{}, model.Handoff{}, "", fmt.Errorf("交接必须指定目标 profile")
	}
	if _, err := s.profileBinding(toProfile); err != nil {
		return model.Intent{}, model.Handoff{}, "", err
	}
	if s.LocalNode == "" {
		return model.Intent{}, model.Handoff{}, "", fmt.Errorf("这个实例没有本机宿主：不能换模型接班")
	}
	in, ok := s.Store.GetIntent(id)
	if !ok {
		return model.Intent{}, model.Handoff{}, "", fmt.Errorf("意图不存在")
	}
	switch in.Status {
	case model.StatusPending, model.StatusClaimed, model.StatusRunning, model.StatusWaiting:
	default:
		return model.Intent{}, model.Handoff{}, "", fmt.Errorf("意图已结束（%s）：先 retry/reopen，再交接", in.Status)
	}
	if in.Node != "" && in.Node != s.LocalNode {
		return model.Intent{}, model.Handoff{}, "", fmt.Errorf("意图在远端节点 %s：模型配置只能在本机执行，交接不能换位置", in.Node)
	}
	// CAS + Hold before touching the live run: a duplicate click carrying an old
	// expected_version must fail here, with no run cancelled and no wait touched.
	held, err := s.Store.BeginHandoff(id, expectedVersion)
	if err != nil {
		return model.Intent{}, model.Handoff{}, "", err
	}
	in = held
	owner := held.HandoffOperation
	if in.Status == model.StatusRunning || in.Status == model.StatusClaimed {
		if in.RunID != "" {
			if err := s.CancelRunWithCause(in.RunID, model.CancelCauseHandoff, "换模型接班"); err != nil {
				log.Printf("handoff %s: 停止旧 run 失败：%v", id, err)
			}
		}
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			cur, ok := s.Store.GetIntent(id)
			if !ok {
				_ = s.Store.ReleaseHandoffHold(id, "意图在交接时消失", owner)
				return model.Intent{}, model.Handoff{}, "", fmt.Errorf("意图在交接时消失")
			}
			if cur.Status != model.StatusRunning && cur.Status != model.StatusClaimed {
				in = cur
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if in.Status == model.StatusRunning || in.Status == model.StatusClaimed {
			_ = s.Store.ReleaseHandoffHold(id, "旧 run 未停下", owner)
			return model.Intent{}, model.Handoff{}, "", fmt.Errorf("旧 run 还没停下，稍后重试交接")
		}
	}
	snapshot := s.buildHandoff(in, toProfile, note)
	saved, err := s.Store.SaveHandoff(id, expectedVersion, snapshot, owner)
	if err != nil {
		_ = s.Store.ReleaseHandoffHold(id, "交接版本冲突", owner)
		return model.Intent{}, model.Handoff{}, "", err
	}
	snapshot = *saved.Handoff
	runID, err := s.startRunOwned(id, "", "", false, false, "", "", owner)
	if err != nil {
		// The snapshot is durable; release the hold so the direction stays a
		// normal pending unit that can be run/retried (existing semantics).
		_ = s.Store.ReleaseHandoffHold(id, "起新会话失败", owner)
		return saved, snapshot, "", err
	}
	_, _ = s.Store.UpdateRun(runID, func(r *model.Run) {
		r.HandoffFrom = snapshot.FromRun
		r.HandoffVersion = saved.HandoffVersion
	})
	run, _ := s.Store.GetRun(runID)
	snapshot.ToGeneration = run.Generation
	// The child results that finished during the handoff window are now the new
	// run's to consume; let the scheduler settle/queue whatever changed.
	go s.pump()
	return saved, snapshot, runID, nil
}

func (s *Server) buildHandoff(in model.Intent, toProfile, note string) model.Handoff {
	db := s.Store.Snapshot()
	h := model.Handoff{
		FromRun: in.RunID, FromProfile: in.Profile, FromGeneration: in.Attempts,
		ToProfile: toProfile, ToGeneration: in.Attempts + 1,
		Note: strings.TrimSpace(note),
	}
	if in.Wait != nil {
		w := *in.Wait
		w.Children = append([]string(nil), in.Wait.Children...)
		w.Actions = append([]string(nil), in.Wait.Actions...)
		w.Results = append([]model.WaitResult(nil), in.Wait.Results...)
		h.Wait = &w
	}
	seen := map[string]bool{}
	evidence := map[string]string{}
	for _, ev := range db.Evidence {
		evidence[ev.ID] = ev.URI
	}
	subtree := map[string]bool{in.ID: true}
	for _, d := range s.Store.Descendants(in.ID) {
		subtree[d.ID] = true
	}
	for _, f := range db.Facts {
		if !subtree[f.IntentID] || f.Status == "rejected" {
			continue
		}
		if f.Status != "verified" && len(f.EvidenceIDs) == 0 {
			continue
		}
		if seen[f.ID] {
			continue
		}
		seen[f.ID] = true
		h.Facts = append(h.Facts, f.ID)
		for _, eid := range f.EvidenceIDs {
			if uri := evidence[eid]; uri != "" && !contains(h.Evidence, uri) {
				h.Evidence = append(h.Evidence, uri)
			}
		}
		if len(h.Facts) >= 30 {
			break
		}
	}
	// Newest first, and only runs that actually said something.
	runs := s.Store.Runs()
	for i := len(runs) - 1; i >= 0 && len(h.Attempts) < 5; i-- {
		r := runs[i]
		if r.IntentID != in.ID || r.CancelCause == model.CancelCauseHandoff {
			continue
		}
		reason := strings.TrimSpace(r.StopReason)
		if reason == "" {
			continue
		}
		h.Attempts = append(h.Attempts, fmt.Sprintf("%s/%s: %s", r.Status, r.Model, clipRunes(reason, 200)))
	}
	for _, child := range s.Store.Children(in.ID) {
		h.Children = append(h.Children, child.ID)
	}
	if msgs := s.Store.Messages(1); len(msgs) > 0 {
		h.MessageCursor = msgs[len(msgs)-1].Seq
	}
	return h
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func handoffSection(db store.DB, in model.Intent, h model.Handoff) string {
	byID := map[string]model.Fact{}
	for _, f := range db.Facts {
		byID[f.ID] = f
	}
	var b strings.Builder
	b.WriteString("\n\n## 交接（新会话，换模型接班）\n")
	b.WriteString("旧对话没有带过来。以下是交接索引，完整事实与历史可按 ID 续读；本轮前提和约束以当前任务书为准。")
	b.WriteString("历史判断描述当时条件；结合当前观察自主决定后续行动。\n")
	if h.Note != "" {
		b.WriteString("\n### 交接说明\n" + h.Note + "\n")
	}
	if len(h.Facts) > 0 {
		b.WriteString("\n### 已确认/有证据的事实\n")
		for _, id := range h.Facts {
			f, ok := byID[id]
			if !ok {
				fmt.Fprintf(&b, "- `%s`：当前快照不存在，不能作为前提\n", id)
				continue
			}
			fmt.Fprintf(&b, "- `%s` 断言 v%d（%s）%s：%s\n", id, f.ClaimVersion, f.Status, f.Title, clipRunes(f.Summary, 240))
		}
	}
	if len(h.Evidence) > 0 {
		b.WriteString("\n### 交接时记录的证据位置（相对当前 case）\n")
		for _, uri := range h.Evidence {
			rel, err := filepath.Rel(in.ID, filepath.FromSlash(uri))
			if err == nil {
				fmt.Fprintf(&b, "- `%s`\n", filepath.ToSlash(rel))
			}
		}
	}
	if len(h.Attempts) > 0 {
		b.WriteString("\n### 此前的尝试与停止原因\n")
		for _, a := range h.Attempts {
			fmt.Fprintf(&b, "- %s\n", a)
		}
	}
	if len(h.Children) > 0 {
		b.WriteString("\n### 子任务\n")
		for _, child := range db.Intents {
			if child.ParentID != in.ID {
				continue
			}
			fmt.Fprintf(&b, "- `%s` %s：%s", child.ID, child.Title, child.Status)
			if child.RunID != "" {
				for _, r := range db.Runs {
					if r.ID == child.RunID {
						fmt.Fprintf(&b, "（run %s %s）", r.Status, clipRunes(r.StopReason, 80))
						break
					}
				}
			}
			b.WriteString("\n")
		}
	}
	if h.Wait != nil && !h.Wait.WakePending {
		if h.Wait.Operator != nil {
			fmt.Fprintf(&b, "\n### 人工交接\n请求 %s：%s。人工是否完成以控制面收据为准，不能自行确认。\n", h.Wait.Operator.ID, h.Wait.Operator.Request)
			if h.Wait.Operator.Resolution == model.OperatorUnavailable {
				fmt.Fprintf(&b, "操作员明确无法完成：%s。缺失登录前提未获得，相关测试未覆盖，按授权与当前实际访问能力重新判断。\n", h.Wait.Operator.Response)
			} else if h.Wait.Operator.CompletedAt != nil {
				fmt.Fprintf(&b, "操作员完成说明：%s。完成回执不等于登录有效；继续前核对实际会话与访问权限。\n", h.Wait.Operator.Response)
			}

		} else if len(h.Wait.Actions) > 0 {
			fmt.Fprintf(&b, "\n### 外部动作等待\n仍在等待：%s（mode=%s，deadline=%s）。结果未知时需要核对，记录不代表执行或成功。\n", strings.Join(h.Wait.Actions, ", "), h.Wait.Mode, deadlineText(h.Wait.Deadline))
		} else {
			fmt.Fprintf(&b, "\n### 等待关系\n仍然在等这些子任务：%s（mode=%s，deadline=%s）。\n",
				strings.Join(h.Wait.Children, ", "), h.Wait.Mode, deadlineText(h.Wait.Deadline))
		}
	}
	fmt.Fprintf(&b, "\n消息游标：%d（用 GET /messages?after=%d 看新消息）。\n", h.MessageCursor, h.MessageCursor)
	return b.String()
}
