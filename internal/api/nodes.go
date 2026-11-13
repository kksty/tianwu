package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/host"
)

// nodeView is one execution location as the console sees it. The single-machine
// edition has exactly one execution host (the `aiscan web` process the control
// plane starts); audit entries in <data>/nodes.json are local one-shot processes
// and have no terminal.
type nodeView struct {
	ID        string `json:"id"`
	Note      string `json:"note,omitempty"`
	Default   bool   `json:"default,omitempty"`
	Transport string `json:"transport,omitempty"`
	Endpoint  string `json:"endpoint,omitempty"`
	Bin       string `json:"bin,omitempty"`
	Terminal  bool   `json:"terminal,omitempty"`
	// Bound and ProjectDefault are kept for older consoles. Single-machine
	// projects are always bound to the local host, so they are true for it.
	Bound          bool             `json:"bound"`
	ProjectDefault bool             `json:"project_default,omitempty"`
	Local          bool             `json:"local,omitempty"`
	Host           *host.HostStatus `json:"host,omitempty"`
}

// listNodes reports the instance's execution locations. There is no per-project
// allowlist any more: the local host is the execution position.
func (s *Server) listNodes(w http.ResponseWriter, r *http.Request) {
	local := s.localNode()
	writeJSON(w, http.StatusOK, map[string]any{
		"nodes":           s.nodeViews(),
		"default":         local,
		"project_default": local,
	})
}

// setNodes is gone in the single-machine edition. Keeping the route answers old
// clients with a clear message instead of silently ignoring a binding that no
// longer exists.
func (s *Server) setNodes(w http.ResponseWriter, r *http.Request) {
	writeErr(w, http.StatusGone, "单机版本不需要绑定节点：执行位置固定为本机宿主")
}

func (s *Server) nodeViews() []nodeView {
	na, ok := s.Exec.(executor.NodeAware)
	if !ok {
		return []nodeView{}
	}
	local := s.localNode()
	infos := na.Nodes()
	out := make([]nodeView, 0, len(infos))
	for _, n := range infos {
		view := nodeView{
			ID:             n.ID,
			Note:           n.Note,
			Default:        n.Default || n.ID == local,
			Transport:      n.Transport,
			Endpoint:       n.Endpoint,
			Bin:            n.Bin,
			Terminal:       n.Transport == "aop",
			Bound:          n.ID == local,
			ProjectDefault: n.ID == local,
			Local:          n.ID == local,
		}
		if n.ID == local && s.LocalHostStatus != nil {
			status := s.LocalHostStatus()
			view.Host = &status
		}
		out = append(out, view)
	}
	return out
}

// CheckLocalHost is the shared default-model dispatch gate. Preserve the
// supervisor's reason instead of turning every failure into a binding error.
func (s *Server) CheckLocalHost() error {
	if s.Profiles != nil {
		profiles, active, err := s.Profiles()
		if err != nil {
			return fmt.Errorf("读模型配置：%w", err)
		}
		for _, p := range profiles {
			if p.ID == active && p.Paused {
				return fmt.Errorf("默认模型配置 %s 已暂停：先在设置中恢复使用或切换模型", active)
			}
		}
	}
	if s.LocalHostStatus == nil {
		return nil
	}
	status := s.LocalHostStatus()
	if status.Ready {
		return nil
	}
	reason := status.LastError
	if reason == "" {
		if status.Online {
			reason = "模型正在初始化"
		} else {
			reason = "执行宿主正在启动或已停止"
		}
	}
	return fmt.Errorf("本机宿主未就绪：%s；请在「执行」查看状态，修好模型连接后在「设置 → 模型」保存重试", reason)
}

// localNode is the single execution position: the host tw started. Callers that
// used to read a project binding now resolve here; a bare test server without a
// LocalNode falls back to the executor's default node.
func (s *Server) localNode() string {
	if s.LocalNode != "" {
		return s.LocalNode
	}
	// A non-NodeAware executor is the single-host case: ResolveNode reports
	// "local" for an empty request, which is exactly this edition.
	if id := executor.ResolveNode(s.Exec, ""); id != "" {
		return id
	}
	na, ok := s.Exec.(executor.NodeAware)
	if !ok {
		return ""
	}
	for _, n := range na.Nodes() {
		if n.Default {
			return n.ID
		}
	}
	if nodes := na.Nodes(); len(nodes) > 0 {
		return nodes[0].ID
	}
	return ""
}

// checkNode validates an explicitly requested node. Only the local host is a
// legal target now; an old client asking for a remote id gets a clear error.
func (s *Server) checkNode(id string) error {
	id = strings.TrimSpace(id)
	local := s.localNode()
	if local == "" {
		return errors.New("本机宿主未配置")
	}
	if id != "" && id != local {
		return fmt.Errorf("单机版只能在本机宿主 %s 执行，不能指定节点 %s", local, id)
	}
	if !s.HasNode(local) {
		return fmt.Errorf("本机宿主 %s 不在执行器里", local)
	}
	return nil
}

// resolveNode is the dispatch-time rule: every intent runs on the local host.
func (s *Server) resolveNode(requested string) (string, error) {
	local := s.localNode()
	if local == "" {
		return "", errors.New("本机宿主未就绪：无法派发")
	}
	if req := strings.TrimSpace(requested); req != "" && req != local {
		return "", fmt.Errorf("单机版只能在本机宿主 %s 执行，不能指定节点 %s", local, req)
	}
	return local, nil
}
