// Package node owns the execution locations of one instance.
//
// A node is "a machine + a harness host + the toolchain around it": it is an
// AOP endpoint, including the machine tw runs on (tw starts a local `aiscan
// web` and registers it as "local"). cyber-audit is the one non-host form: a
// one-shot local binary with no agent to talk to. Everything else — dispatch,
// steer, verify, report, terminal, REPL — goes through the host (D64).
package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kksty/tianwu/internal/aopexec"
	"github.com/kksty/tianwu/internal/executor"
)

// Spec is one node as it is written in <data>/nodes.json or injected by the
// control plane. Every execution node is an AOP host: the control-plane machine
// is not an exception, tw starts a local `aiscan web` and registers it as the
// node "local" (docs/14-nodes.md, D64).
//
//	{
//	  "default": "lab-win",
//	  "nodes": [
//	    {"id": "lab-win", "aop": "http://10.0.0.5:18766", "token": "…",
//	     "agent": "local", "note": "另一台机器的 harness 宿主"},
//	    {"id": "audit", "bin": "../tools/cyber-audit", "audit": true,
//	     "note": "源码审计：本地一次性进程，不是 agent"}
//	  ]
//	}
type Spec struct {
	ID   string `json:"id"`
	Note string `json:"note,omitempty"`
	// AOP nodes are reached through a harness host's AOP endpoint. `bin` is a
	// control-plane local process and exists only for audit nodes.
	AOP string `json:"aop,omitempty"`
	// Token is that host's access key.
	Token string `json:"token,omitempty"`
	// Agent pins sessions to one agent node id inside the host. Empty means the
	// host picks, which is only right when it runs exactly one agent.
	Agent string `json:"agent,omitempty"`
	// Audit marks a node that runs cyber-audit instead of an agent (D47). The
	// command line and the product are different: a one-shot audit writes a
	// report directory, which the executor translates into the ledger's handoff.
	// It is the only node form with a local binary (D64).
	Audit bool `json:"audit,omitempty"`
	// Bin, Args and Env belong to the audit form: the binary, its extra flags
	// and the environment it runs with. They are ignored (and rejected) on an
	// execution node, which is always a host somewhere else.
	Bin  string            `json:"bin,omitempty"`
	Args []string          `json:"args,omitempty"`
	Env  map[string]string `json:"env,omitempty"`
	// Note on the AOP form: the agent's working directory is the host's, so the
	// case directory must be visible to it at the same path (same machine or an
	// identical mount). There is no file-read RPC to fall back on.
}

// Registry is the instance-level list of nodes plus the default one.
type Registry struct {
	specs map[string]Spec
	order []string
	def   string
	path  string

	// llmEnv / llmArgs are the instance's harness configuration from the
	// settings page (CYBER_* and --context-window / --max-tokens). The local
	// host tw starts inherits them; a remote AOP host keeps its own host-side
	// configuration.
	llmMu   sync.RWMutex
	llmEnv  []string
	llmArgs []string

	// localID is the node the control plane started itself. Only that node can
	// run an intent on a named model profile: the profile host pool belongs to
	// this process, and a remote host has its own configuration.
	localID  string
	profiles LocalProfiles
}

// LocalProfiles resolves a saved model profile to an executor backed by a local
// host started with that profile's configuration.
type LocalProfiles interface {
	Executor(profile string) (executor.Executor, error)
}

// ProfileHost exposes the live endpoint of a profile host. The say path needs
// it: a profile run's AOP session lives on that host, not on the node's default
// endpoint, so resolving the node would find the wrong machine.
type ProfileHost interface {
	Host(profile string) (endpoint, token, agent string, err error)
	// HostForURL finds a (possibly retired) revision by its endpoint, so a run
	// that started before a binding edit can still receive messages.
	HostForURL(profile, endpoint string) (base, token, agent string, ok bool)
}

// LocalHostFor returns the AOP endpoint of the local host serving a profile.
func (r *Registry) LocalHostFor(profile string) (endpoint, token, agent string, ok bool) {
	if r.profiles == nil || strings.TrimSpace(profile) == "" {
		return "", "", "", false
	}
	ph, ok := r.profiles.(ProfileHost)
	if !ok {
		return "", "", "", false
	}
	endpoint, token, agent, err := ph.Host(profile)
	if err != nil || endpoint == "" {
		return "", "", "", false
	}
	return endpoint, token, agent, true
}

// LocalHostForSession resolves the host that owns a specific session endpoint.
// It falls back to the current revision when the endpoint is unknown (for
// example an old marker after a restart).
func (r *Registry) LocalHostForSession(profile, endpoint string) (base, token, agent string, ok bool) {
	if r.profiles == nil || strings.TrimSpace(profile) == "" {
		return "", "", "", false
	}
	ph, ok := r.profiles.(ProfileHost)
	if !ok {
		return "", "", "", false
	}
	if base, token, agent, ok := ph.HostForURL(profile, endpoint); ok {
		return base, token, agent, true
	}
	return r.LocalHostFor(profile)
}

// SetLocalProfiles marks which node is local and how a profile is turned into
// an executor. A registry with neither is a pure remote/audit instance.
func (r *Registry) SetLocalProfiles(id string, profiles LocalProfiles) {
	r.localID = strings.TrimSpace(id)
	r.profiles = profiles
}

// LocalID is the node id tw started ("" when this instance has no local host).
func (r *Registry) LocalID() string { return r.localID }

func (r *Registry) localIDOr(fallback string) string {
	if r.localID != "" {
		return r.localID
	}
	return fallback
}

// SetLLM replaces the harness environment and flags handed to local audit runs.
// It is called at boot and whenever the settings page saves, so the next run
// uses the new configuration without restarting the control plane. (The local
// AOP host is started once, with the settings in effect at boot.)
func (r *Registry) SetLLM(env, args []string) {
	r.llmMu.Lock()
	r.llmEnv = append([]string(nil), env...)
	r.llmArgs = append([]string(nil), args...)
	r.llmMu.Unlock()
}

func (r *Registry) llm() (env, args []string) {
	r.llmMu.RLock()
	defer r.llmMu.RUnlock()
	return append([]string(nil), r.llmEnv...), append([]string(nil), r.llmArgs...)
}

// New validates a set of specs and picks a default. An empty default means the
// first node, which is what a one-node deployment wants.
//
// Every node is a host except audit: there is no local one-shot agent form any
// more (D64). A node that only has `bin` is refused with that sentence instead
// of being run as a subprocess.
func New(specs []Spec, def string) (*Registry, error) {
	if len(specs) == 0 {
		return nil, errors.New("一个节点都没有：本机宿主没起来，也没有 nodes.json")
	}
	r := &Registry{specs: make(map[string]Spec, len(specs))}
	for _, s := range specs {
		s.ID = strings.TrimSpace(s.ID)
		if s.ID == "" {
			return nil, errors.New("节点缺 id")
		}
		if _, dup := r.specs[s.ID]; dup {
			return nil, fmt.Errorf("节点 id 重复：%s", s.ID)
		}
		if s.Audit {
			if strings.TrimSpace(s.Bin) == "" {
				return nil, fmt.Errorf("审计节点 %s 缺 bin", s.ID)
			}
			if strings.TrimSpace(s.AOP) != "" {
				return nil, fmt.Errorf("节点 %s 既是 audit 又是 aop：cyber-audit 是本地进程，不通过宿主跑", s.ID)
			}
		} else {
			if strings.TrimSpace(s.AOP) == "" {
				if strings.TrimSpace(s.Bin) != "" {
					return nil, fmt.Errorf("节点 %s 只写了 bin：spawn 已移除，执行节点必须是 aop 宿主（本机也由控制台自动起 aiscan web）", s.ID)
				}
				return nil, fmt.Errorf("节点 %s 缺 aop：执行节点必须是 harness 宿主", s.ID)
			}
			if strings.TrimSpace(s.Bin) != "" || len(s.Args) > 0 || len(s.Env) > 0 {
				return nil, fmt.Errorf("节点 %s 同时写了 aop 和 bin/args/env：执行节点由宿主运行，这些字段只属于 audit", s.ID)
			}
		}
		r.specs[s.ID] = s
		r.order = append(r.order, s.ID)
	}
	if def == "" {
		def = r.order[0]
	}
	if _, ok := r.specs[def]; !ok {
		return nil, fmt.Errorf("默认节点 %s 不在列表里", def)
	}
	r.def = def
	return r, nil
}

// File is the on-disk nodes.json: extra hosts on top of the local one tw
// starts itself.
type File struct {
	Default string `json:"default"`
	Nodes   []Spec `json:"nodes"`
}

// Read loads nodes.json without validating it: the control plane adds its own
// local host to the list first, so validation belongs to New.
//
// A relative `bin` (audit only) is resolved against the directory of the file,
// not the shell that started tw: the file describes its own nodes, so moving
// the file moves its references.
func Read(path string) (File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return File{}, err
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return File{}, fmt.Errorf("%s 不是合法的节点文件：%w", path, err)
	}
	f.Nodes = absolutize(f.Nodes, filepath.Dir(path))
	return f, nil
}

// Load reads and validates a nodes.json on its own (used by tests and tools
// that have no local host to inject).
func Load(path string) (*Registry, error) {
	f, err := Read(path)
	if err != nil {
		return nil, err
	}
	r, err := New(f.Nodes, f.Default)
	if err != nil {
		return nil, fmt.Errorf("%s：%w", path, err)
	}
	r.path = path
	return r, nil
}

// absolutize anchors relative binary paths so a child process can start them
// from any work dir. base "" means the process's own working directory. The
// result is absolute even when base itself was relative (`--nodes .build/x`).
func absolutize(specs []Spec, base string) []Spec {
	out := make([]Spec, len(specs))
	copy(out, specs)
	for i := range out {
		p := strings.TrimSpace(out[i].Bin)
		if p == "" {
			continue
		}
		target := p
		if !filepath.IsAbs(p) {
			if base != "" {
				target = filepath.Join(base, p)
			}
			if abs, err := filepath.Abs(target); err == nil {
				target = abs
			}
		}
		// A nodes.json written on Windows may omit the .exe suffix.
		out[i].Bin = executor.NormalizePath(target)
	}
	return out
}

// Missing lists audit nodes whose binary is not on disk right now. A node may
// be provisioned after the process starts, so this is a startup warning, not an
// error: it turns "the first run failed mysteriously" into "the log said so".
// A host is checked by the health probe, not by stat.
func (r *Registry) Missing() []string {
	var out []string
	for _, id := range r.order {
		s := r.specs[id]
		if s.AOP != "" {
			continue // a host somewhere else: reachability is a probe, not a file
		}
		if st, err := os.Stat(s.Bin); err != nil || st.IsDir() {
			label := s.Bin
			if strings.TrimSpace(label) == "" {
				label = "没配 bin"
			}
			out = append(out, id+" ("+label+")")
		}
	}
	return out
}

func (r *Registry) Default() string { return r.def }

// Path is where the registry came from; "" when it was synthesised.
func (r *Registry) Path() string { return r.path }

func (r *Registry) IDs() []string { return append([]string(nil), r.order...) }

func (r *Registry) Spec(id string) (Spec, bool) {
	s, ok := r.specs[id]
	return s, ok
}

// Resolve maps a request to the id that will actually run. An unknown id falls
// back to the default: the caller records the resolved id, so a mistaken route
// is visible as "ran on local" rather than a failed run.
func (r *Registry) Resolve(requested string) string {
	if s := strings.TrimSpace(requested); s != "" {
		if _, ok := r.specs[s]; ok {
			return s
		}
	}
	return r.def
}

// TerminalEndpoint reports how the console reaches a shell on the node. Only an
// AOP host has one: audit is a local process with no host to dial (D64). The
// agent id is what the host routes PTY streams to.
func (r *Registry) TerminalEndpoint(id string) (baseURL, token, agent string, ok bool) {
	s, found := r.specs[strings.TrimSpace(id)]
	if !found || s.AOP == "" {
		return "", "", "", false
	}
	agent = strings.TrimSpace(s.Agent)
	if agent == "" {
		agent = "local"
	}
	return s.AOP, s.Token, agent, true
}

// Infos is the console-facing view: id, note, host endpoint or audit binary,
// and the transport that reaches it.
func (r *Registry) Infos() []executor.NodeInfo {
	out := make([]executor.NodeInfo, 0, len(r.order))
	for _, id := range r.order {
		s := r.specs[id]
		info := executor.NodeInfo{ID: id, Note: s.Note, Default: id == r.def}
		if s.AOP != "" {
			info.Transport, info.Endpoint = "aop", s.AOP
		} else {
			// An audit node is the only form with a local binary (D64).
			info.Transport, info.Bin = "audit", s.Bin
		}
		out = append(out, info)
	}
	return out
}

// Executor builds the router for this registry.
func (r *Registry) Executor(timeout time.Duration) executor.Executor {
	return &Router{reg: r, timeout: timeout}
}

// Router dispatches one request to the node it names.
type Router struct {
	reg     *Registry
	timeout time.Duration
}

func (r *Router) Name() string {
	return "nodes(" + strings.Join(r.reg.IDs(), ",") + ")"
}

func (r *Router) Nodes() []executor.NodeInfo { return r.reg.Infos() }

func (r *Router) ResolveNode(requested string) string { return r.reg.Resolve(requested) }

// TerminalEndpoint exposes the host terminal through the router, which is what
// the API's executor actually is (not the Registry).
func (r *Router) TerminalEndpoint(nodeID string) (baseURL, token, agent string, ok bool) {
	return r.reg.TerminalEndpoint(nodeID)
}

// LocalHostFor exposes profile routing to native browser commands as well as
// model turns. The API receives a Router, never the underlying Registry.
func (r *Router) LocalHostFor(profile string) (baseURL, token, agent string, ok bool) {
	return r.reg.LocalHostFor(profile)
}

// CanResume answers "does this case have something to continue?" for the node
// that would run it. A host keeps its trajectory on the host; audit produces
// one report and has nothing to continue.
func (r *Router) CanResume(node, workDir string) bool {
	id := r.reg.Resolve(node)
	spec, _ := r.reg.Spec(id)
	if spec.AOP != "" {
		return aopexec.CanResume(workDir)
	}
	// audit: a cyber-audit run is one one-shot report; a "continuation" would
	// need a second report and a new task, which is a re-dispatch, not a steer.
	return false
}

func (r *Router) CanResumeProfile(node, profile, workDir string) bool {
	id := r.reg.Resolve(node)
	if profile != "" && id != r.reg.localID {
		return false
	}
	session, ok := aopexec.ReadSession(workDir)
	if !ok {
		return false
	}
	var endpoint, agent string
	if profile == "" {
		endpoint, _, agent, ok = r.reg.TerminalEndpoint(id)
	} else {
		endpoint, _, agent, ok = r.reg.LocalHostFor(profile)
	}
	return ok && strings.TrimRight(session.URL, "/") == strings.TrimRight(endpoint, "/") && (agent == "" || session.Agent == "" || agent == session.Agent)
}

// Run hands the request to the node's own runner. The request carries the
// resolved id, so an event handler or a wrapper can tell where work happened.
func (r *Router) Run(ctx context.Context, req executor.Request, emit func(executor.Event)) (executor.Result, error) {
	id := r.reg.Resolve(req.Node)
	req.Node = id
	if profile := strings.TrimSpace(req.Profile); profile != "" {
		if r.reg.localID == "" || id != r.reg.localID {
			return executor.Result{ExitCode: -1}, fmt.Errorf(
				"节点 %s 不能指定模型配置 %s：只有本机宿主（%s）能按配置启动独立宿主，远端节点使用它自己的模型配置",
				id, profile, r.reg.localIDOr("未启动"))
		}
		if r.reg.profiles == nil {
			return executor.Result{ExitCode: -1}, fmt.Errorf("实例没有模型配置目录：不能按配置 %s 执行", profile)
		}
		ex, err := r.reg.profiles.Executor(profile)
		if err != nil {
			return executor.Result{ExitCode: -1}, fmt.Errorf("模型配置 %s：%w", profile, err)
		}
		res, err := ex.Run(ctx, req, emit)
		if err != nil {
			return res, fmt.Errorf("节点 %s（模型配置 %s，AOP %s）：%w", id, profile, ex.Name(), err)
		}
		return res, nil
	}
	spec, _ := r.reg.Spec(id)
	llmEnv, llmArgs := r.reg.llm()
	switch {
	case spec.Audit:
		ex := executor.Audit{
			Bin:     spec.Bin,
			Extra:   append(append([]string{}, llmArgs...), spec.Args...),
			Env:     append(envList(spec.Env), llmEnv...),
			Timeout: r.timeout,
		}
		res, err := ex.Run(ctx, req, emit)
		if err != nil {
			return res, fmt.Errorf("节点 %s（audit %s）：%w", id, spec.Bin, err)
		}
		return res, nil
	case spec.AOP != "":
		// The host owns its model configuration; passing CYBER_* here would be
		// a lie the host cannot honour (docs/14).
		ex := aopexec.Executor{BaseURL: spec.AOP, Token: spec.Token, Agent: spec.Agent, Timeout: r.timeout}
		res, err := ex.Run(ctx, req, emit)
		if err != nil {
			return res, fmt.Errorf("节点 %s（AOP %s）：%w", id, spec.AOP, err)
		}
		return res, nil
	default:
		// Unreachable for specs built through New; kept so a hand-built Spec
		// fails loudly instead of running somewhere unexpected.
		return executor.Result{ExitCode: -1}, fmt.Errorf("节点 %s 没有 aop 也不是 audit：不能执行", id)
	}
}

func envList(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}
