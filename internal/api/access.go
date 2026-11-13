package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kksty/tianwu/internal/access"
	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/model"
)

func (s *Server) listAccess(w http.ResponseWriter, r *http.Request) {
	s.accessMu.Lock()
	defer s.accessMu.Unlock()
	rows, err := access.List(s.CaseRoot)
	if err != nil {
		writeErr(w, 500, "读取登录材料失败："+err.Error())
		return
	}
	for i := range rows {
		s.browserLeaseView(&rows[i])
		if id, agent := identityOf(r); agent && rows[i].Kind == "browser" {
			lease := rows[i].Lease
			if lease == nil || lease.RunID != id.RunID || lease.Generation != id.Generation || lease.State != "held" {
				rows[i].Session = ""
			}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"access": rows})
}

func (s *Server) createAccess(w http.ResponseWriter, r *http.Request) {
	var in access.Input
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, access.MaxMaterial+65536)).Decode(&in); err != nil {
		writeErr(w, 400, "登录材料 JSON 无效或过大")
		return
	}
	s.accessMu.Lock()
	defer s.accessMu.Unlock()
	if in.Session != "" {
		writeErr(w, 400, "已有会话应由执行者随人工请求关联")
		return
	}
	if in.RequestID != "" || in.IntentID != "" {
		intent, ok := s.Store.GetIntent(in.IntentID)
		if !ok || intent.Wait == nil || intent.Wait.Operator == nil || intent.Wait.Operator.ID != in.RequestID || intent.Wait.Operator.CompletedAt != nil {
			writeErr(w, 409, "人工操作请求已失效，请刷新")
			return
		}
		run, ok := s.Store.GetRun(intent.Wait.RegisteredByRun)
		if !ok {
			writeErr(w, 409, "申请浏览器的执行者运行不存在")
			return
		}
		in.Profile = run.Profile
	} else {
		// Proactive preparation uses the default host. Requests always inherit
		// the actual requesting run, never a host selected by the operator.
		in.Profile = ""
	}
	entry, err := access.Create(s.CaseRoot, in)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if in.RequestID != "" {
		if err := s.Store.AddOperatorAccess(in.IntentID, in.RequestID, entry.ID); err != nil {
			_ = access.Delete(s.CaseRoot, entry.ID)
			writeErr(w, 409, err.Error())
			return
		}
	}
	s.secretMu.Lock()
	s.secretAt = time.Time{}
	s.secretMu.Unlock()
	s.recordAction("access_create", entry.ID, entry.Name)
	writeJSON(w, 201, entry)
}

func (s *Server) deleteAccess(w http.ResponseWriter, r *http.Request) {
	s.accessMu.Lock()
	defer s.accessMu.Unlock()
	if s.browserActive[r.PathValue("id")] {
		writeErr(w, 409, "浏览器仍有在途操作，不能删除")
		return
	}
	control, controlErr := s.browserControlState(r.PathValue("id"))
	if controlErr != nil || control.Pending {
		writeErr(w, 409, "浏览器命令结果未知，请先领取并恢复现场")
		return
	}
	if err := access.Delete(s.CaseRoot, r.PathValue("id")); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s.recordAction("access_delete", r.PathValue("id"), "")
	writeJSON(w, 200, map[string]bool{"deleted": true})
}

func (s *Server) accessMaterial(w http.ResponseWriter, r *http.Request) {
	s.accessMu.Lock()
	defer s.accessMu.Unlock()
	if _, agent := identityOf(r); agent {
		entry, err := access.Get(s.CaseRoot, r.PathValue("id"))
		if err != nil {
			writeErr(w, 404, "登录材料不可用")
			return
		}
		if entry.Kind == "browser" {
			s.browserLeaseView(&entry)
			owner, _, err := s.browserOwner(r)
			lease := entry.Lease
			if err != nil || lease == nil || lease.State != "held" || lease.ID != r.URL.Query().Get("lease_id") || lease.RunID != owner.RunID || lease.Generation != owner.Generation || lease.Owner != owner.Owner {
				writeErr(w, 409, "读取浏览器绑定前须由本轮领取，并携带 lease_id")
				return
			}
		}
	}
	raw, err := access.Material(s.CaseRoot, r.PathValue("id"))
	if err != nil {
		writeErr(w, 404, "登录材料不可用")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(raw)
}

func (s *Server) requestOperator(w http.ResponseWriter, r *http.Request) {
	in, ok := s.Store.GetIntent(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "意图不存在")
		return
	}
	c := s.caller(r)
	if c.agent && c.runID != in.RunID {
		writeErr(w, 403, "只能为自己的当前 run 请求人工操作")
		return
	}
	// Per-run ownership is checked as well as run id: a caller cannot redirect
	// a handoff by passing another intent's path.
	if c.agent && c.participant != collab.ParticipantID(in.ID) {
		writeErr(w, 403, "只能操作自己的方向")
		return
	}
	var req struct {
		model.OperatorWait
		Browser *access.Input `json:"browser,omitempty"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&req) != nil {
		writeErr(w, 400, "人工操作请求无效")
		return
	}
	if len(req.AccessIDs) > 16 || (req.Browser != nil && len(req.AccessIDs) >= 16) {
		writeErr(w, 400, "关联登录材料过多")
		return
	}
	s.accessMu.Lock()
	defer s.accessMu.Unlock()
	run, runOK := s.Store.GetRun(in.RunID)
	for _, id := range req.AccessIDs {
		entry, err := access.Get(s.CaseRoot, id)
		if err != nil {
			writeErr(w, 400, "关联登录材料不存在："+id)
			return
		}
		if entry.Kind == "browser" && (!runOK || entry.Profile != run.Profile) {
			writeErr(w, 409, "关联浏览器必须位于申请执行者的宿主")
			return
		}
	}
	var created string
	if req.Browser != nil {
		if !runOK || strings.TrimSpace(req.Request) == "" || len([]rune(req.Request)) > 2000 {
			writeErr(w, 400, "需要当前执行者和有效人工操作说明")
			return
		}
		// Replays of an already registered request must not create new sessions.
		if current, ok := s.Store.GetIntent(in.ID); ok && current.Wait != nil {
			writeErr(w, 409, "已有等待条件，请读取原人工请求")
			return
		}
		browser := *req.Browser
		browser.Kind, browser.Profile, browser.IntentID = "browser", run.Profile, in.ID
		browser.RequestID, browser.Account, browser.Role, browser.Tenant = "", "", "", ""
		entry, err := access.Create(s.CaseRoot, browser)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		created = entry.ID
		req.AccessIDs = append(req.AccessIDs, entry.ID)
	}
	request, err := s.Store.RegisterOperatorWait(in.ID, in.RunID, req.OperatorWait)
	if err != nil {
		if created != "" {
			_ = access.Delete(s.CaseRoot, created)
		}
		writeErr(w, 409, err.Error())
		return
	}
	s.recordAction("operator_request", in.ID, request.Request)
	// The persisted condition precedes cancellation. This also stops a harness
	// evaluator whose login goal cannot be met until the human acts.
	if err := s.CancelRunWithCause(in.RunID, model.CancelCauseOperatorWait, "等待操作员："+request.Request); err != nil {
		if current, ok := s.Store.GetIntent(in.ID); !ok || current.Wait == nil || current.Wait.Operator == nil {
			writeErr(w, 409, "当前轮已结束，人工请求未生效")
			return
		}
	}
	writeJSON(w, 202, map[string]any{"request": request, "yield": true, "message": "人工请求已登记，本轮让出执行槽；完成后恢复原方向。"})
}

func (s *Server) completeOperator(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RequestID  string `json:"request_id"`
		Response   string `json:"response"`
		Resolution string `json:"resolution"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&req) != nil {
		writeErr(w, 400, "完成说明无效")
		return
	}
	s.accessMu.Lock()
	defer s.accessMu.Unlock()
	if in, ok := s.Store.GetIntent(r.PathValue("id")); ok && in.Wait != nil && in.Wait.Operator != nil && in.Wait.Operator.ID == req.RequestID && in.Wait.Operator.CompletedAt == nil {
		for _, id := range in.Wait.Operator.AccessIDs {
			entry, err := access.Get(s.CaseRoot, id)
			if err != nil {
				continue
			}
			if entry.Kind != "browser" {
				continue
			}
			control, err := s.browserControlState(id)
			if entry.Lease != nil || s.browserActive[id] || err != nil || control.Pending {
				writeErr(w, 409, "请先归还或恢复关联浏览器现场，再继续执行者")
				return
			}
		}
	}
	if err := s.Store.ResolveOperatorWait(r.PathValue("id"), req.RequestID, req.Response, req.Resolution); err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	action := "operator_complete"
	if req.Resolution == model.OperatorUnavailable {
		action = "operator_unavailable"
	}
	s.recordAction(action, r.PathValue("id"), req.Response)
	go s.pump()
	writeJSON(w, 202, map[string]any{"accepted": true, "message": "人工回执已记录；有执行容量后由原方向继续判断，无法完成不代表已登录或已覆盖。"})
}

func (s *Server) operatorRequests(w http.ResponseWriter, r *http.Request) {
	rows := []map[string]any{}
	for _, in := range s.Store.ListIntents() {
		if in.Wait != nil && in.Wait.Operator != nil {
			run, _ := s.Store.GetRun(in.Wait.RegisteredByRun)
			rows = append(rows, map[string]any{"intent_id": in.ID, "title": in.Title, "status": in.Status, "run_id": run.ID, "profile": run.Profile, "request": in.Wait.Operator})
		}
	}
	writeJSON(w, 200, map[string]any{"requests": rows})
}

// Only references and operator labels enter a brief. Raw login material stays
// in private files/API responses and is not promoted to a fact or evidence.
func (s *Server) accessSection(workDir string) (string, error) {
	s.accessMu.Lock()
	defer s.accessMu.Unlock()
	rows, err := access.List(s.CaseRoot)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", nil
	}
	for i := range rows {
		s.browserLeaseView(&rows[i])
		if rows[i].Kind == "browser" {
			rows[i].Session = ""
		}
	}
	dir := filepath.Join(workDir, ".tw", "access")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	index, _ := json.MarshalIndent(rows, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "index.json"), index, 0600); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("\n\n## 操作员提供的登录材料（未验证）\n")
	for i, row := range rows {
		if row.Kind == "browser" {
			if i < 8 {
				fmt.Fprintf(&b, "- %s · %s · 浏览器现场 · 站点 %s · 申请方向 %s · 宿主 %s · 领取后按 ID 获取绑定\n", row.ID, row.Name, row.Origin, row.IntentID, row.Profile)
			}
			continue
		}
		raw, err := access.Material(s.CaseRoot, row.ID)
		if err != nil {
			return "", err
		}
		p := filepath.Join(dir, row.ID+".json")
		if err := os.WriteFile(p, raw, 0600); err != nil {
			return "", err
		}
		if i < 8 {
			fmt.Fprintf(&b, "- %s · %s · %s · 站点 %s · 材料 `.tw/access/%s.json`\n", row.ID, row.Name, row.Kind, row.Origin, row.ID)
		}
	}
	fmt.Fprintf(&b, "完整登录材料目录（%d 份，元数据）：`.tw/access/index.json`。\n", len(rows))
	b.WriteString("材料只说明操作员提供了什么；使用前核对身份和有效性。人工登录/扫码/验证码交接契约见 harness skill `collaborate`。原生结果索引可按项目查询，不等于已确认发现。\n")
	return b.String(), nil
}
