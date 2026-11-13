package api

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/kksty/tianwu/internal/aop"
	"github.com/kksty/tianwu/internal/executor"
)

type runtimeToolsResponse struct {
	State      string            `json:"state"`
	Error      string            `json:"error,omitempty"`
	AgentID    string            `json:"agent_id,omitempty"`
	Provider   string            `json:"provider,omitempty"`
	Model      string            `json:"model,omitempty"`
	ModelError string            `json:"model_error,omitempty"`
	Commands   []aop.CommandSpec `json:"commands"`
	CheckedAt  time.Time         `json:"checked_at"`
}

// listRuntimeTools reads the running default host's own catalogue. It never
// launches a profile, opens a session or probes a target. Keep it separate
// from log statistics so a slow host cannot block their normal polling.
func (s *Server) listRuntimeTools(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	out := runtimeToolsResponse{State: "unavailable", Commands: []aop.CommandSpec{}, CheckedAt: time.Now()}
	tn, ok := s.Exec.(executor.TerminalNode)
	if !ok {
		out.Error = "执行器没有本机宿主通道"
		writeJSON(w, http.StatusOK, out)
		return
	}
	base, token, agentID, ok := tn.TerminalEndpoint(s.localNode())
	if !ok {
		out.Error = "本机宿主未配置"
		writeJSON(w, http.StatusOK, out)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	agents, err := aop.New(base, token).Agents(ctx)
	if err != nil {
		out.Error = s.redactSecrets(err.Error())
	} else if agent, found := aop.Find(agents, agentID); !found {
		out.Error = "宿主未注册指定的执行者"
	} else {
		out.State, out.AgentID = "connected", agent.ID
		out.Provider, out.Model = agent.Provider, agent.Model
		out.ModelError = s.redactSecrets(agent.ConfigError)
		for _, cmd := range agent.Commands {
			if cmd.Name != "" {
				out.Commands = append(out.Commands, cmd)
			}
		}
		sort.Slice(out.Commands, func(i, j int) bool { return out.Commands[i].Name < out.Commands[j].Name })
	}
	writeJSON(w, http.StatusOK, out)
}
