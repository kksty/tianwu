package node

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/executor"
)

func TestNewValidates(t *testing.T) {
	cases := []struct {
		name  string
		specs []Spec
		def   string
		want  string
	}{
		{"empty", nil, "", "一个节点都没有"},
		{"no id", []Spec{{AOP: "http://h"}}, "", "缺 id"},
		{"no aop", []Spec{{ID: "a"}}, "", "缺 aop"},
		// spawn is gone: a plain node with only a binary is refused with the
		// sentence that says what to do instead (D64).
		{"bin only", []Spec{{ID: "a", Bin: "x"}}, "", "spawn 已移除"},
		{"aop with bin", []Spec{{ID: "a", AOP: "http://h", Bin: "x"}}, "", "只属于 audit"},
		{"audit without bin", []Spec{{ID: "a", Audit: true}}, "", "缺 bin"},
		{"audit with aop", []Spec{{ID: "a", Audit: true, Bin: "b", AOP: "http://h"}}, "", "既是 audit 又是 aop"},
		{"duplicate", []Spec{{ID: "a", AOP: "http://h"}, {ID: "a", AOP: "http://h2"}}, "", "重复"},
		{"unknown default", []Spec{{ID: "a", AOP: "http://h"}}, "b", "默认节点"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := New(c.specs, c.def)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want error containing %q, got %v", c.want, err)
			}
		})
	}
}

func TestResolveFallsBackToDefault(t *testing.T) {
	r, err := New([]Spec{
		{ID: "local", AOP: "http://127.0.0.1:18766"},
		{ID: "lab", AOP: "http://10.0.0.5:18766"},
	}, "local")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ in, want string }{
		{"", "local"},
		{"lab", "lab"},
		{"  lab  ", "lab"},
		// A node that no longer exists must not turn every dispatch into a dead
		// run: it lands on the default, and the run records where it really ran.
		{"gone", "local"},
	} {
		if got := r.Resolve(c.in); got != c.want {
			t.Fatalf("Resolve(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDefaultIsFirstWhenUnset(t *testing.T) {
	r, err := New([]Spec{
		{ID: "a", AOP: "http://a"},
		{ID: "b", AOP: "http://b"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Default() != "a" {
		t.Fatalf("default = %q, want a", r.Default())
	}
	if ids := r.IDs(); len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("ids = %v", ids)
	}
}

func TestLoadAndInfos(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nodes.json")
	body := `{
	  "default": "lab",
	  "nodes": [
	    {"id": "lab", "aop": "http://10.0.0.5:18766", "token": "t", "agent": "gpu",
	     "note": "另一台机器的宿主"},
	    {"id": "audit", "bin": "./bin/cyber-audit", "audit": true,
	     "env": {"PATH": "/opt/audit/bin", "LAB": "1"}}
	  ]
	}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if r.Default() != "lab" || r.Path() != path {
		t.Fatalf("default=%q path=%q", r.Default(), r.Path())
	}
	infos := r.Infos()
	if len(infos) != 2 || infos[0].ID != "lab" || infos[1].ID != "audit" {
		t.Fatalf("infos = %+v", infos)
	}
	if !infos[0].Default || infos[1].Default {
		t.Fatalf("default flag wrong: %+v", infos)
	}
	if infos[0].Transport != "aop" || infos[0].Endpoint != "http://10.0.0.5:18766" {
		t.Fatalf("aop info = %+v", infos[0])
	}
	if infos[1].Transport != "audit" {
		t.Fatalf("audit info = %+v", infos[1])
	}
	// The environment reaches the audit process, and in a stable order.
	spec, _ := r.Spec("audit")
	if got := envList(spec.Env); strings.Join(got, ",") != "LAB=1,PATH=/opt/audit/bin" {
		t.Fatalf("env = %v", got)
	}
}

// The control plane injects its local host before New: a file with remote hosts
// is combined with it, and a file that tries to define "local" again is a
// mistake worth naming.
func TestInjectLocalHostNextToFileNodes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nodes.json")
	if err := os.WriteFile(path, []byte(`{"nodes":[{"id":"lab","aop":"http://lab"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	specs := append([]Spec{{ID: "local", AOP: "http://127.0.0.1:18766", Agent: "local"}}, f.Nodes...)
	r, err := New(specs, f.Default)
	if err != nil {
		t.Fatal(err)
	}
	if r.Default() != "local" {
		t.Fatalf("default = %q, want the injected local host", r.Default())
	}

	if _, err := New(append(specs, Spec{ID: "local", AOP: "http://other"}), ""); err == nil ||
		!strings.Contains(err.Error(), "重复") {
		t.Fatalf("a file that redefines local must fail loudly, got %v", err)
	}
}

func TestRouterNamesTheNodeWhenItFails(t *testing.T) {
	r, err := New([]Spec{
		// Closed ports: the failure must name the node and say it was the AOP
		// transport, not the old "missing binary" path.
		{ID: "local", AOP: "http://127.0.0.1:1"},
		{ID: "lab", AOP: "http://127.0.0.1:2"},
	}, "local")
	if err != nil {
		t.Fatal(err)
	}
	_, runErr := r.Executor(0).Run(context.Background(), executor.Request{Node: "lab"}, func(executor.Event) {})
	if runErr == nil || !strings.Contains(runErr.Error(), "lab") || !strings.Contains(runErr.Error(), "AOP") {
		t.Fatalf("a broken node must name itself and its transport, got %v", runErr)
	}
}

func TestLoadAnchorsRelativeAuditBin(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nodes.json")
	if err := os.WriteFile(path, []byte(`{"nodes":[{"id":"a","bin":"cyber-audit","audit":true}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := r.Spec("a")
	// A relative bin must not be resolved against the child's work dir, or the
	// first audit run from a case directory fails with "path not found".
	if !filepath.IsAbs(s.Bin) {
		t.Fatalf("bin not absolute: %q", s.Bin)
	}
	if filepath.Dir(s.Bin) != dir {
		t.Fatalf("bin anchored to %q, want %q", filepath.Dir(s.Bin), dir)
	}
	if got := r.Missing(); len(got) != 1 {
		t.Fatalf("missing = %v", got)
	}
}

func TestRouterIsNodeAware(t *testing.T) {
	r, err := New([]Spec{
		{ID: "local", AOP: "http://a"},
		{ID: "lab", AOP: "http://b"},
	}, "local")
	if err != nil {
		t.Fatal(err)
	}
	ex := r.Executor(0)
	na, ok := ex.(executor.NodeAware)
	if !ok {
		t.Fatal("router must expose its nodes to the API")
	}
	if got := na.ResolveNode("lab"); got != "lab" {
		t.Fatalf("ResolveNode = %q", got)
	}
	if got := executor.ResolveNode(ex, ""); got != "local" {
		t.Fatalf("ResolveNode(\"\") = %q", got)
	}
	if name := ex.Name(); !strings.Contains(name, "local") || !strings.Contains(name, "lab") {
		t.Fatalf("Name = %q", name)
	}
}

// TestAOPNodeIsAHostNotABinary pins D64: every execution node is a host, so it
// needs no local executable and is not "missing" when none is on disk. Only the
// audit form carries a binary.
func TestAOPNodeIsAHostNotABinary(t *testing.T) {
	r, err := New([]Spec{
		{ID: "hub", AOP: "http://10.0.0.5:18766", Token: "t", Agent: "gpu"},
		{ID: "audit", Bin: "definitely-not-here", Audit: true},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Missing(); len(got) != 1 || !strings.Contains(got[0], "audit") {
		t.Fatalf("only the audit node can be missing a binary, got %v", got)
	}
	infos := r.Infos()
	if infos[0].Transport != "aop" || infos[0].Endpoint != "http://10.0.0.5:18766" {
		t.Fatalf("aop info = %+v", infos[0])
	}
	if infos[0].Bin != "" {
		t.Fatalf("a host must not advertise a binary: %+v", infos[0])
	}
}

func TestRouterRoutesToAOPHost(t *testing.T) {
	r, err := New([]Spec{
		{ID: "local", AOP: "http://127.0.0.1:1"},
		// Closed port: the failure must be an AOP dial failure, not a missing
		// binary, or the transport never got chosen.
		{ID: "hub", AOP: "http://127.0.0.1:1", Token: "t"},
	}, "local")
	if err != nil {
		t.Fatal(err)
	}
	_, runErr := r.Executor(0).Run(context.Background(), executor.Request{Node: "hub"}, func(executor.Event) {})
	if runErr == nil || !strings.Contains(runErr.Error(), "hub") || !strings.Contains(runErr.Error(), "AOP") {
		t.Fatalf("want an AOP dial error naming the node, got %v", runErr)
	}
}

// TestRouterCanResumePerTransport: "is there something to continue?" is the
// host's answer for a host, and always no for a one-shot audit report.
func TestRouterCanResumePerTransport(t *testing.T) {
	r, err := New([]Spec{
		{ID: "hub", AOP: "http://127.0.0.1:1"},
		{ID: "audit", Bin: "x", Audit: true},
	}, "hub")
	if err != nil {
		t.Fatal(err)
	}
	ex := r.Executor(0)
	work := t.TempDir()

	if executor.CanResume(ex, "hub", work) {
		t.Fatal("empty case must not be resumable")
	}
	if err := os.MkdirAll(filepath.Join(work, ".tw"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, ".tw", "aop-session.json"), []byte(`{"url":"http://h","session":"s1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if !executor.CanResume(ex, "hub", work) {
		t.Fatal("an AOP session marker must be resumable")
	}
	if executor.CanResume(ex, "audit", work) {
		t.Fatal("a one-shot audit report must not be treated as a resumable trajectory")
	}
}

// TestAuditNodeKeepsItsOwnTransport pins D47 under D64: audit is the only local
// binary form left, it is announced as its own transport, and it cannot also be
// an AOP host.
func TestAuditNodeKeepsItsOwnTransport(t *testing.T) {
	r, err := New([]Spec{
		{ID: "local", AOP: "http://127.0.0.1:18766"},
		{ID: "audit", Bin: "also-not-here", Audit: true},
	}, "local")
	if err != nil {
		t.Fatal(err)
	}
	infos := r.Infos()
	if infos[1].Transport != "audit" {
		t.Fatalf("audit transport = %q, want audit", infos[1].Transport)
	}
	if got := r.Missing(); len(got) != 1 {
		t.Fatalf("audit node still needs its binary on disk, missing = %v", got)
	}
}

// The instance's harness settings travel with the registry; audit runs are the
// local caller that still needs them per run.
func TestRegistryCarriesLLMSettings(t *testing.T) {
	reg, err := New([]Spec{{ID: "local", AOP: "http://127.0.0.1:18766"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	reg.SetLLM([]string{"CYBER_MODEL=m"}, []string{"--context-window", "131072"})
	env, args := reg.llm()
	if strings.Join(env, " ") != "CYBER_MODEL=m" {
		t.Fatalf("env = %v", env)
	}
	if strings.Contains(strings.Join(env, " "), "CYBER_DATA_DIR") {
		t.Fatalf("the instance must not pin CYBER_DATA_DIR: %v", env)
	}
	if strings.Join(args, " ") != "--context-window 131072" {
		t.Fatalf("args = %v", args)
	}
}
