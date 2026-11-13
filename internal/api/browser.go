package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kksty/tianwu/internal/access"
	"github.com/kksty/tianwu/internal/aop"
	"github.com/kksty/tianwu/internal/executor"
)

type browserControl struct {
	Endpoint string `json:"endpoint"`
	Session  string `json:"session"`
	Cursor   string `json:"cursor"`
	Pending  bool   `json:"pending,omitempty"`
}

// Browser actions are native harness commands in a dedicated project session.
// They consume no model tokens and survive a worker's yield or replacement.
func (s *Server) browserAction(w http.ResponseWriter, r *http.Request) {
	if s.paused.Load() || s.deleting.Load() {
		writeErr(w, 409, "项目已暂停或正在删除")
		return
	}
	var req struct {
		Action  string `json:"action"`
		LeaseID string `json:"lease_id"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req) != nil {
		writeErr(w, 400, "浏览器动作无效")
		return
	}
	s.accessMu.Lock()
	defer s.accessMu.Unlock()
	entry, err := access.Get(s.CaseRoot, r.PathValue("id"))
	if err != nil || entry.Kind != "browser" {
		writeErr(w, 400, "需要浏览器类型的登录材料")
		return
	}
	if s.paused.Load() || s.deleting.Load() || s.browserActive[entry.ID] {
		writeErr(w, 409, "项目已暂停、正在删除或现场有在途操作")
		return
	}
	if entry.Lease == nil || entry.Lease.ID != req.LeaseID || entry.Lease.Owner != "operator" || entry.Lease.RunID != "" {
		writeErr(w, 409, "请先领取浏览器现场；其他执行者占用时不能操作")
		return
	}
	material, err := access.Material(s.CaseRoot, entry.ID)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	var binding access.BrowserBinding
	if json.Unmarshal(material, &binding) != nil {
		writeErr(w, 400, "浏览器材料无效")
		return
	}
	var line string
	session := quoteNative(entry.Session)
	switch req.Action {
	case "open":
		if binding.ExistingSession || binding.Connected {
			// This is the worker's actual login page. Reopening would discard
			// the CAPTCHA or QR challenge the human was asked to complete.
			line = "!playwright url " + session
		} else if binding.CDPURL != "" {
			line = "!playwright attach --cdp " + quoteNative(binding.CDPURL) + " --session " + session
		} else {
			line = "!playwright open " + quoteNative(entry.URL) + " --headed --session " + session
		}
	case "inspect":
		line = "!playwright url " + session
	case "save":
		if err := os.Remove(filepath.Join(s.CaseRoot, ".access", entry.ID, "state-export.json")); err != nil && !os.IsNotExist(err) {
			writeErr(w, 409, err.Error())
			return
		}
		line = "!playwright state-save " + session + " state-export.json"
	case "detach":
		if binding.CDPURL != "" {
			line = "!playwright detach " + session
		} else {
			line = "!playwright close " + session
		}
	default:
		writeErr(w, 400, "action 只能是 open、inspect、save、detach")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if s.Ctx != nil {
		stop := context.AfterFunc(s.Ctx, cancel)
		defer stop()
	}
	if s.browserActive == nil {
		s.browserActive = map[string]bool{}
	}
	s.browserActive[entry.ID] = true
	output, err := func() (string, error) {
		// Restore the resource lock before publishing results or returning, even
		// on panic. Lease changes and deletion reject browserActive meanwhile.
		defer func() { s.accessMu.Lock(); delete(s.browserActive, entry.ID) }()
		s.accessMu.Unlock()
		return s.runBrowserCommand(ctx, entry, line)
	}()
	if err != nil {
		writeErr(w, 409, s.redactSecrets(err.Error()))
		return
	}
	if req.Action == "open" || req.Action == "detach" {
		binding.Connected = req.Action == "open"
		if req.Action == "detach" {
			binding.ExistingSession = false
		}
		if err := access.SaveBrowserBinding(s.CaseRoot, entry.ID, binding); err != nil {
			writeErr(w, 409, "浏览器已返回，但连接状态保存失败，请检查当前窗口："+s.redactSecrets(err.Error()))
			return
		}
	}
	response := map[string]any{"returned": true, "output": s.redactSecrets(output), "verified": false}
	if req.Action == "save" {
		raw, err := os.ReadFile(filepath.Join(s.CaseRoot, ".access", entry.ID, "state-export.json"))
		if err != nil {
			writeErr(w, 409, "宿主未生成状态文件；请查看浏览器会话是否仍然存在。"+s.redactSecrets(output))
			return
		}
		state, err := access.Create(s.CaseRoot, access.Input{Name: entry.Name + " · 状态快照", Kind: "state", Origin: entry.Origin, Account: entry.Account, Role: entry.Role, Tenant: entry.Tenant, Material: raw})
		if err != nil {
			writeErr(w, 409, err.Error())
			return
		}
		response["access"] = state
		s.secretMu.Lock()
		s.secretAt = time.Time{}
		s.secretMu.Unlock()
	}
	s.recordAction("browser_"+req.Action, entry.ID, entry.Name)
	writeJSON(w, 200, response)
}

func quoteNative(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }

func (s *Server) runBrowserCommand(ctx context.Context, entry access.Entry, line string) (string, error) {
	node := s.LocalNode
	if node == "" {
		return "", fmt.Errorf("本机宿主未配置")
	}
	if err := s.checkNode(node); err != nil {
		return "", err
	}
	tn, ok := s.Exec.(executor.TerminalNode)
	if !ok {
		return "", fmt.Errorf("执行器没有本机宿主通道")
	}
	base, token, agent, ok := tn.TerminalEndpoint(node)
	if !ok {
		return "", fmt.Errorf("本机宿主不可用")
	}
	if entry.Profile != "" {
		if profiles, ok := s.Exec.(interface {
			LocalHostFor(string) (string, string, string, bool)
		}); ok {
			if pbase, ptoken, pagent, found := profiles.LocalHostFor(entry.Profile); found {
				base, token, agent = pbase, ptoken, pagent
			} else {
				return "", fmt.Errorf("浏览器模型宿主不可用：%s", entry.Profile)
			}
		} else {
			return "", fmt.Errorf("执行器不支持命名宿主")
		}
	}
	dir := filepath.Join(s.CaseRoot, ".access", entry.ID)
	marker := filepath.Join(dir, "control-session.json")
	var control browserControl
	if raw, err := os.ReadFile(marker); err == nil {
		if err := json.Unmarshal(raw, &control); err != nil {
			return "", err
		}
		if control.Endpoint != base {
			return "", fmt.Errorf("浏览器宿主已变化，请重新创建浏览器材料或使用外部 CDP 接管")
		}
		if control.Pending {
			return "", fmt.Errorf("上次浏览器命令的结果未确认，不能安全重发；请检查原窗口，重新创建浏览器材料后接管。原窗口可能仍然打开")
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	conn, err := aop.New(base, token).Dial(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	if control.Session == "" {
		session, err := conn.OpenSession(ctx, agent, "tianwu browser: "+entry.ID, dir)
		if err != nil {
			return "", err
		}
		control = browserControl{Endpoint: base, Session: session.Id}
		raw, _ := json.Marshal(control)
		if err := os.WriteFile(marker, raw, 0600); err != nil {
			_ = conn.CloseSession(ctx, control.Session, "browser control marker failed")
			return "", err
		}
	}
	if err := conn.Watch(ctx, control.Session, control.Cursor); err != nil {
		return "", err
	}
	// A crash/timeout cannot turn a manual retry into a duplicate side effect
	// or accept an old completion for an identical command line.
	control.Pending = true
	raw, _ := json.Marshal(control)
	if err := os.WriteFile(marker, raw, 0600); err != nil {
		return "", err
	}
	output, err := conn.Command(ctx, control.Session, line)
	if err != nil {
		return "", err
	}
	control.Cursor = conn.Cursor()
	control.Pending = false
	raw, _ = json.Marshal(control)
	if err := os.WriteFile(marker, raw, 0600); err != nil {
		return "", err
	}
	return output, nil
}
