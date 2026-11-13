package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/kksty/tianwu/internal/access"
	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

func (s *Server) browserOwner(r *http.Request) (access.BrowserLease, string, error) {
	if id, ok := identityOf(r); ok {
		in, exists := s.Store.GetIntent(id.IntentID)
		run, live := s.Store.GetRun(id.RunID)
		if !exists || !live || run.Status != model.RunRunning || in.Status != model.StatusRunning || run.IntentID != id.IntentID || in.RunID != id.RunID || in.Attempts != id.Generation || run.Generation != id.Generation || id.ParticipantID != collab.ParticipantID(id.IntentID) {
			return access.BrowserLease{}, "", fmt.Errorf("只能由当前运行的执行者领取或归还现场")
		}
		return access.BrowserLease{Owner: id.ParticipantID, RunID: id.RunID, IntentID: id.IntentID, Generation: id.Generation}, run.Profile, nil
	}
	return access.BrowserLease{Owner: "operator"}, "", nil
}

func (s *Server) browserLeaseView(entry *access.Entry) {
	if entry.Lease == nil {
		return
	}
	entry.Lease.Busy = s.browserActive[entry.ID]
	if entry.Lease.Busy {
		return
	}
	if entry.Lease.RunID != "" {
		run, ok := s.Store.GetRun(entry.Lease.RunID)
		if !ok || run.Status != model.RunRunning {
			entry.Lease.State = "recovery"
		}
	}
	control, err := s.browserControlState(entry.ID)
	if err != nil || control.Pending {
		entry.Lease.State = "recovery"
	}
}

func (s *Server) browserControlState(id string) (browserControl, error) {
	var control browserControl
	if _, err := access.Get(s.CaseRoot, id); err != nil {
		return control, err
	}
	raw, err := os.ReadFile(filepath.Join(s.CaseRoot, ".access", id, "control-session.json"))
	if os.IsNotExist(err) {
		return control, nil
	}
	if err != nil {
		return control, err
	}
	err = json.Unmarshal(raw, &control)
	return control, err
}

// CheckBrowserReleaseForDeletion runs after the dispatch/deletion gate closes.
// Removing a project must not erase the only record of an occupied external
// browser. It does not release, detach or otherwise change any resource.
func (s *Server) CheckBrowserReleaseForDeletion() error {
	s.accessMu.Lock()
	defer s.accessMu.Unlock()
	entries, err := access.List(s.CaseRoot)
	if err != nil {
		return fmt.Errorf("读取浏览器占用失败：%w", err)
	}
	for _, entry := range entries {
		if entry.Kind != "browser" {
			continue
		}
		control, err := s.browserControlState(entry.ID)
		if entry.Lease != nil || s.browserActive[entry.ID] || err != nil || control.Pending {
			return fmt.Errorf("浏览器现场「%s」仍有占用或未知操作，请在「访问与交接」归还或恢复后再删除项目", entry.Name)
		}
	}
	return nil
}

func (s *Server) browserLease(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action        string `json:"action"`
		LeaseID       string `json:"lease_id"`
		Note          string `json:"note"`
		ConfirmedIdle bool   `json:"confirmed_idle"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req) != nil || len([]rune(req.Note)) > 500 {
		writeErr(w, 400, "占用请求无效，说明不超过 500 字")
		return
	}
	s.accessMu.Lock()
	defer s.accessMu.Unlock()
	entry, err := access.Get(s.CaseRoot, r.PathValue("id"))
	if err != nil || entry.Kind != "browser" {
		writeErr(w, 400, "浏览器现场不可用")
		return
	}
	if s.browserActive[entry.ID] {
		writeErr(w, 409, "浏览器操作尚未结束，请稍后再归还或恢复")
		return
	}
	owner, profile, err := s.browserOwner(r)
	if err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	switch req.Action {
	case "acquire":
		if s.paused.Load() || s.deleting.Load() {
			writeErr(w, 409, "项目已暂停或正在删除")
			return
		}
		if owner.RunID != "" && profile != entry.Profile {
			writeErr(w, 409, "浏览器位于不同模型宿主，请使用匹配的 profile 或导出状态文件")
			return
		}
		s.browserLeaseView(&entry)
		if entry.Lease != nil && entry.Lease.State == "recovery" {
			writeErr(w, 409, "原占用需要操作员确认现场空闲后恢复")
			return
		}
		owner.Note = strings.TrimSpace(req.Note)
		lease, err := access.AcquireBrowser(s.CaseRoot, entry.ID, owner)
		if err != nil {
			writeErr(w, 409, err.Error())
			return
		}
		s.recordAction("browser_acquire", entry.ID, lease.ID+" · "+lease.Owner)
		writeJSON(w, 200, map[string]any{"lease": lease})
	case "release", "recover":
		control, controlErr := s.browserControlState(entry.ID)
		if req.Action == "release" && (controlErr != nil || control.Pending) {
			writeErr(w, 409, "上次浏览器命令未确认，需由操作员确认现场空闲后恢复")
			return
		}
		if req.Action == "recover" {
			if _, agent := identityOf(r); agent {
				writeErr(w, 403, "只有操作员可以恢复未知占用")
				return
			}
			if entry.Lease == nil || !req.ConfirmedIdle || strings.TrimSpace(req.Note) == "" {
				writeErr(w, 400, "恢复需要确认原现场已空闲并填写说明")
				return
			}
			if entry.Lease.RunID != "" {
				run, exists := s.Store.GetRun(entry.Lease.RunID)
				if exists && run.Status == model.RunRunning {
					writeErr(w, 409, "持有者仍在运行，请先停止该运行并等待结束")
					return
				}
				// The lifecycle registry is the authority on a run that is
				// still exiting; purpose/review runs are covered too.
				if s.Lifecycle.RunActive(entry.Lease.RunID) {
					writeErr(w, 409, "持有者仍在退出，请等待结束")
					return
				}
			}
			owner = *entry.Lease
			if req.LeaseID != owner.ID {
				writeErr(w, 409, "占用凭据已变化，请刷新")
				return
			}
			if controlErr != nil || control.Pending {
				// A new control session has no old command completions to replay.
				// Keep the old marker for inspection and leave its browser intact.
				dir := filepath.Join(s.CaseRoot, ".access", entry.ID)
				if err := os.Rename(filepath.Join(dir, "control-session.json"), filepath.Join(dir, "control-recovery-"+store.NewID("op")+".json")); err != nil {
					writeErr(w, 409, "保存原控制会话失败："+err.Error())
					return
				}
			}
		}
		if err := access.ReleaseBrowser(s.CaseRoot, entry.ID, req.LeaseID, owner); err != nil {
			writeErr(w, 409, err.Error())
			return
		}
		s.recordAction("browser_"+req.Action, entry.ID, req.LeaseID+" · "+strings.TrimSpace(req.Note))
		writeJSON(w, 200, map[string]bool{"released": true})
	default:
		writeErr(w, 400, "action 只能是 acquire、release 或 recover")
	}
}
