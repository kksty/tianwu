package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/host"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/project"
)

func TestNodeListShowsModelFailureWithoutInventingAnUnboundNode(t *testing.T) {
	s, _ := newBindingServer(t)
	s.LocalHostStatus = func() host.HostStatus {
		return host.HostStatus{Online: true, Ready: false, LastError: "API error (402): Insufficient Balance"}
	}
	w := call(t, s, http.MethodGet, "/api/nodes", nil)
	var body struct {
		Nodes []nodeView `json:"nodes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, n := range body.Nodes {
		if n.ID == "local" {
			if !n.Bound || !n.Terminal || n.Host == nil || !n.Host.Online || n.Host.Ready || !strings.Contains(n.Host.LastError, "Insufficient Balance") {
				t.Fatalf("node=%+v", n)
			}
			return
		}
	}
	t.Fatal("missing local node")
}

// nodeExec is a registry with the local host, a legacy remote host and an audit
// node. The single-machine server must treat only the local host as executable.
type nodeExec struct{}

func (nodeExec) Name() string { return "nodes" }

func (nodeExec) Run(context.Context, executor.Request, func(executor.Event)) (executor.Result, error) {
	return executor.Result{ExitCode: 0, StopReason: "completed"}, nil
}

func (nodeExec) Nodes() []executor.NodeInfo {
	return []executor.NodeInfo{
		{ID: "local", Transport: "aop", Endpoint: "http://127.0.0.1:18766", Default: true},
		{ID: "lab", Transport: "aop", Endpoint: "http://10.0.0.7:18766", Note: "lab host"},
		{ID: "audit", Transport: "audit", Bin: `C:\tools\cyber-audit.exe`, Note: "源码审计"},
	}
}

func (nodeExec) ResolveNode(string) string { return "local" }

func (nodeExec) TerminalEndpoint(id string) (baseURL, token, agent string, ok bool) {
	switch id {
	case "local":
		return "http://127.0.0.1:18766", "tok", "local", true
	case "lab":
		return "http://10.0.0.7:18766", "tok", "local", true
	}
	// audit has a binary, not a host: no PTY.
	return "", "", "", false
}

// testDir is a scratch directory for a fixture. It is not t.TempDir because that
// asserts the directory is empty at cleanup, and a finished run may still have a
// background goroutine (afterIntentSettled, the scheduler) writing the ledger a
// moment later. Waiting for "no running runs" does not cover that window; a
// best-effort remove does, without hiding it behind a sleep.
func testDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "tw-api-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func newBindingServer(t *testing.T) (*Server, *project.Project) {
	t.Helper()
	mgr, err := project.Open(filepath.Join(testDir(t), "projects"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := mgr.Create("绑定测试", "")
	if err != nil {
		t.Fatal(err)
	}
	s := New(p.Store, nodeExec{}, p.Dir, p.CaseDir)
	s.Project = p
	return s, p
}

// call routes one API request through the server's real mux. It lives here
// because the node/API tests are the fixture-heavy ones that need it.
func call(t *testing.T, s *Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	s.Register(mux)
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// An unbound project must still dispatch: binding is gone, the local host is the
// execution position.
func TestSingleNodeDispatchRecordsLocal(t *testing.T) {
	s, p := newBindingServer(t)
	in, err := p.Store.CreateIntent(model.Intent{Title: "查一遍", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	runID, err := s.StartRun(in.ID)
	if err != nil {
		t.Fatalf("StartRun on an unbound project: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		r, ok := p.Store.GetRun(runID)
		if ok && r.Status != model.RunRunning {
			if r.Node != "local" {
				t.Fatalf("run node = %q, want local", r.Node)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("run did not finish")
}

func TestCreateIntentRejectsRemoteNode(t *testing.T) {
	s, _ := newBindingServer(t)
	w := call(t, s, http.MethodPost, "/api/intents", map[string]string{"title": "x", "node": "lab"})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "只能在本机宿主") {
		t.Fatalf("remote node: %d %s", w.Code, w.Body.String())
	}
}

func TestNodeListIsReadOnly(t *testing.T) {
	s, _ := newBindingServer(t)
	w := call(t, s, http.MethodGet, "/api/nodes", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Nodes          []nodeView `json:"nodes"`
		Default        string     `json:"default"`
		ProjectDefault string     `json:"project_default"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Default != "local" || resp.ProjectDefault != "local" {
		t.Fatalf("defaults = %q/%q", resp.Default, resp.ProjectDefault)
	}
	byID := map[string]nodeView{}
	for _, n := range resp.Nodes {
		byID[n.ID] = n
	}
	if !byID["local"].Bound || !byID["local"].Local || !byID["local"].Terminal {
		t.Fatalf("local view = %+v", byID["local"])
	}
	if byID["lab"].Bound {
		t.Fatalf("a legacy remote node must not be bound: %+v", byID["lab"])
	}
	if byID["audit"].Terminal {
		t.Fatalf("an audit node has no host terminal: %+v", byID["audit"])
	}

	w = call(t, s, http.MethodPut, "/api/nodes", map[string]any{"nodes": []string{"lab"}, "default": "lab"})
	if w.Code != http.StatusGone {
		t.Fatalf("PUT /api/nodes = %d %s, want 410", w.Code, w.Body.String())
	}
}

func TestResolveNodeOnlyLocal(t *testing.T) {
	s, _ := newBindingServer(t)
	got, err := s.resolveNode("")
	if err != nil || got != "local" {
		t.Fatalf("resolve empty = %q/%v, want local", got, err)
	}
	if _, err := s.resolveNode("lab"); err == nil || !strings.Contains(err.Error(), "只能在本机宿主") {
		t.Fatalf("resolve lab = %v, want a single-machine refusal", err)
	}
	if _, err := s.resolveNode("local"); err != nil {
		t.Fatalf("resolve local: %v", err)
	}
}
