package aop

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Agent is one agent node as the host reports it (ListAgents). The control
// plane uses WorkingDir for session placement and Commands for the live
// capability catalogue. Neither command registration nor Bound proves that
// scanner resources or the model provider are ready.
type Agent struct {
	ID          string        `json:"id"`
	Name        string        `json:"name,omitempty"`
	Hostname    string        `json:"hostname,omitempty"`
	OS          string        `json:"os,omitempty"`
	WorkingDir  string        `json:"working_dir,omitempty"`
	Provider    string        `json:"provider,omitempty"`
	Model       string        `json:"model,omitempty"`
	Bound       bool          `json:"bound,omitempty"`
	ConfigError string        `json:"config_error,omitempty"`
	Commands    []CommandSpec `json:"commands"`
}

// CommandSpec is the host's public command declaration, not an executable
// readiness check. It includes runtime commands, native tools and skills.
type CommandSpec struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Usage       string `json:"usage,omitempty"`
}

// Agents lists the agents registered into this host.
//
// It is the management API, not AOP: `cyber.rpc.agent.AgentService/ListAgents`
// is a Connect *unary* call, which speaks plain JSON over HTTP/1.1, so this
// costs an encoding/json struct rather than another protobuf client.
func (c *Client) Agents(ctx context.Context) ([]Agent, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/cyber.rpc.agent.AgentService/ListAgents", strings.NewReader("{}"))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("aop: 列出 agent 失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("aop: 列出 agent 失败：HTTP %d", resp.StatusCode)
	}
	var body struct {
		Agents []struct {
			Hello struct {
				NodeID  string `json:"nodeId"`
				Name    string `json:"name"`
				Runtime struct {
					Hostname   string `json:"hostname"`
					OS         string `json:"os"`
					WorkingDir string `json:"workingDir"`
				} `json:"runtime"`
			} `json:"hello"`
			Status struct {
				Provider    string `json:"provider"`
				Model       string `json:"model"`
				Bound       bool   `json:"bound"`
				ConfigError string `json:"configError"`
			} `json:"status"`
			Commands []CommandSpec `json:"commands"`
		} `json:"agents"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("aop: agent 列表不是 JSON：%w", err)
	}
	out := make([]Agent, 0, len(body.Agents))
	for _, a := range body.Agents {
		out = append(out, Agent{
			ID:          a.Hello.NodeID,
			Name:        a.Hello.Name,
			Hostname:    a.Hello.Runtime.Hostname,
			OS:          a.Hello.Runtime.OS,
			WorkingDir:  a.Hello.Runtime.WorkingDir,
			Provider:    a.Status.Provider,
			Model:       a.Status.Model,
			Bound:       a.Status.Bound,
			ConfigError: a.Status.ConfigError,
			Commands:    a.Commands,
		})
	}
	return out, nil
}

// Find returns the agent with this node id, or the only agent when id is empty.
// "Only" matters: a host with two agents and no pinning would make the working
// directory a guess, and a guessed working directory is a run that writes
// somewhere nobody reads.
func Find(agents []Agent, id string) (Agent, bool) {
	if id != "" {
		for _, a := range agents {
			if a.ID == id {
				return a, true
			}
		}
		return Agent{}, false
	}
	if len(agents) == 1 {
		return agents[0], true
	}
	return Agent{}, false
}
