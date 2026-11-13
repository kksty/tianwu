package node

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/executor"
)

type fakeExecutor struct {
	name    string
	profile string
	ran     bool
	err     error
}

func (f *fakeExecutor) Name() string { return f.name }

func (f *fakeExecutor) Run(_ context.Context, req executor.Request, _ func(executor.Event)) (executor.Result, error) {
	f.ran = true
	f.profile = req.Profile
	return executor.Result{ExitCode: 0}, f.err
}

type fakeProfiles struct {
	ex     executor.Executor
	called string
	err    error
}

func (f *fakeProfiles) Executor(profile string) (executor.Executor, error) {
	f.called = profile
	if f.err != nil {
		return nil, f.err
	}
	return f.ex, nil
}

func profileRegistry(t *testing.T) *Registry {
	t.Helper()
	r, err := New([]Spec{
		{ID: "local", AOP: "http://127.0.0.1:18766"},
		{ID: "lab", AOP: "http://10.0.0.5:18766"},
	}, "local")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// A profile run on the local node goes through the profile pool, and the pool
// sees the case's profile instead of the node's default configuration.
func TestRouterRunsProfileOnLocalPool(t *testing.T) {
	r := profileRegistry(t)
	ex := &fakeExecutor{name: "profile-host"}
	pool := &fakeProfiles{ex: ex}
	r.SetLocalProfiles("local", pool)
	router := r.Executor(0)

	req := executor.Request{Node: "local", Profile: "fast", IntentID: "in_1", WorkDir: t.TempDir()}
	res, err := router.Run(context.Background(), req, func(executor.Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || !ex.ran || ex.profile != "fast" || pool.called != "fast" {
		t.Fatalf("res=%+v ran=%v profile=%q called=%q", res, ex.ran, ex.profile, pool.called)
	}
	if req.Profile != "fast" {
		t.Fatal("request mutated")
	}
}

// A remote host owns its own configuration. Running a profile there would
// silently use the remote model, so the router refuses.
func TestRouterRefusesProfileOnRemoteNode(t *testing.T) {
	r := profileRegistry(t)
	pool := &fakeProfiles{ex: &fakeExecutor{name: "profile-host"}}
	r.SetLocalProfiles("local", pool)
	router := r.Executor(0)

	_, err := router.Run(context.Background(), executor.Request{Node: "lab", Profile: "fast"}, func(executor.Event) {})
	if err == nil || !strings.Contains(err.Error(), "只有本机宿主") {
		t.Fatalf("err = %v", err)
	}
	if pool.called != "" {
		t.Fatal("pool was asked for a remote node")
	}
}

func TestRouterRefusesProfileWithoutLocalNode(t *testing.T) {
	r := profileRegistry(t)
	router := r.Executor(0)
	_, err := router.Run(context.Background(), executor.Request{Node: "local", Profile: "fast"}, func(executor.Event) {})
	if err == nil || !strings.Contains(err.Error(), "只有本机宿主") {
		t.Fatalf("err = %v", err)
	}
}

func TestRouterReportsPoolFailure(t *testing.T) {
	r := profileRegistry(t)
	r.SetLocalProfiles("local", &fakeProfiles{err: errors.New("没有这个配置")})
	router := r.Executor(0)
	_, err := router.Run(context.Background(), executor.Request{Node: "local", Profile: "gone"}, func(executor.Event) {})
	if err == nil || !strings.Contains(err.Error(), "没有这个配置") {
		t.Fatalf("err = %v", err)
	}
}

type nativeProfilePool struct{ fakeProfiles }

func TestSelectedProfileResumesOnlyItsOwnHostSession(t *testing.T) {
	r := profileRegistry(t)
	r.SetLocalProfiles("local", &nativeProfilePool{})
	router := r.Executor(0)
	dir := t.TempDir()
	write := func(marker string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, ".tw"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".tw", "aop-session.json"), []byte(marker), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if executor.CanResumeProfile(router, "local", "browser-profile", dir) {
		t.Fatal("missing session resumed")
	}
	write(`{"url":"http://127.0.0.1:18766","agent":"local","session":"s_default"}`)
	if !executor.CanResumeProfile(router, "local", "", dir) {
		t.Fatal("default host session lost")
	}
	if executor.CanResumeProfile(router, "local", "browser-profile", dir) {
		t.Fatal("default host reused for named profile")
	}
	write(`{"url":"http://127.0.0.1:19001","agent":"other-agent","session":"s_old"}`)
	if executor.CanResumeProfile(router, "local", "browser-profile", dir) {
		t.Fatal("different agent resumed")
	}
	write(`{"url":"http://127.0.0.1:19001","agent":"profile-agent","session":"s_selected"}`)
	if executor.CanResumeProfile(router, "local", "", dir) {
		t.Fatal("clearing the selection resumed the previous profile host")
	}
	if !executor.CanResumeProfile(router, "local", "browser-profile", dir) {
		t.Fatal("selected host session lost")
	}
	if executor.CanResumeProfile(router, "lab", "browser-profile", dir) {
		t.Fatal("remote host selected a local profile")
	}
}

func (*nativeProfilePool) Host(profile string) (string, string, string, error) {
	if profile != "browser-profile" {
		return "", "", "", errors.New("unknown profile")
	}
	return "http://127.0.0.1:19001", "profile-token", "profile-agent", nil
}

func (*nativeProfilePool) HostForURL(string, string) (string, string, string, bool) {
	return "", "", "", false
}

func TestRouterNativeCommandsResolveTheSelectedProfileHost(t *testing.T) {
	r := profileRegistry(t)
	r.SetLocalProfiles("local", &nativeProfilePool{})
	router := r.Executor(0)
	native, ok := router.(interface {
		LocalHostFor(string) (string, string, string, bool)
	})
	if !ok {
		t.Fatal("the API's executor cannot route native commands to a profile host")
	}
	base, token, agent, ok := native.LocalHostFor("browser-profile")
	if !ok || base != "http://127.0.0.1:19001" || token != "profile-token" || agent != "profile-agent" {
		t.Fatalf("profile host = %q %q %q %v", base, token, agent, ok)
	}
	if _, _, _, ok := native.LocalHostFor("missing"); ok {
		t.Fatal("missing profile fell back to the default host")
	}
}
