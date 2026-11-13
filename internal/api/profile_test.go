package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/project"
)

// recordExec captures the dispatch request and reports a session model, which
// is where the run's "actual model" comes from.
type recordExec struct {
	mu  sync.Mutex
	req executor.Request
	run bool
}

func (r *recordExec) Name() string { return "record" }

func (r *recordExec) Run(_ context.Context, req executor.Request, emit func(executor.Event)) (executor.Result, error) {
	r.mu.Lock()
	r.req = req
	r.run = true
	r.mu.Unlock()
	emit(executor.Event{Kind: executor.KindSession, Text: "actual-model"})
	emit(executor.Event{Kind: executor.KindTurnEnd, StopReason: "completed", OK: true})
	return executor.Result{ExitCode: 0, StopReason: "completed"}, nil
}

func (r *recordExec) Request() executor.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.req
}

func profileServer(t *testing.T, profiles ...ProfileInfo) (*Server, *project.Project) {
	t.Helper()
	s, p := newBindingServer(t)
	s.LocalNode = "local"
	s.Profiles = func() ([]ProfileInfo, string, error) {
		return append([]ProfileInfo(nil), profiles...), "fast", nil
	}
	return s, p
}

func testProfiles() []ProfileInfo {
	return []ProfileInfo{
		{ID: "fast", Provider: "openai", Model: "fast-model", ContextWindow: 32000, Revision: "rev-fast", Runnable: true, Active: true, Tier: "strong"},
		{ID: "slow", Provider: "anthropic", Model: "slow-model", ContextWindow: 64000, Revision: "rev-slow", Runnable: true, Tier: "strong"},
		{ID: "broken", Provider: "openai", Model: "m", Revision: "rev-broken"},
	}
}

func TestCreateIntentValidatesProfile(t *testing.T) {
	s, p := profileServer(t, testProfiles()...)
	if err := p.SetNodeBinding([]string{"local", "lab"}, "local"); err != nil {
		t.Fatal(err)
	}

	w := call(t, s, http.MethodPost, "/api/intents", map[string]string{"title": "简单核对", "profile": "fast"})
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var in model.Intent
	if err := json.Unmarshal(w.Body.Bytes(), &in); err != nil {
		t.Fatal(err)
	}
	if in.Profile != "fast" {
		t.Fatalf("intent profile = %q", in.Profile)
	}

	for _, c := range []struct {
		name string
		body map[string]string
		want string
	}{
		{"unknown", map[string]string{"title": "x", "profile": "gone"}, "没有模型配置 gone"},
		{"not runnable", map[string]string{"title": "x", "profile": "broken"}, "还不能执行"},
		{"remote node", map[string]string{"title": "x", "profile": "fast", "node": "lab"}, "只能在本机宿主"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := call(t, s, http.MethodPost, "/api/intents", c.body)
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), c.want) {
				t.Fatalf("got %d %s", w.Code, w.Body.String())
			}
		})
	}
}

// A legacy nodes.json binding must not make a profile unusable: profiles run on
// the local host pool, and project binding is gone in the single-machine
// edition.
func TestCreateIntentProfileIgnoresLegacyNodeBinding(t *testing.T) {
	s, p := profileServer(t, testProfiles()...)
	if err := p.SetNodeBinding([]string{"lab"}, "lab"); err != nil {
		t.Fatal(err)
	}
	w := call(t, s, http.MethodPost, "/api/intents", map[string]string{"title": "x", "profile": "fast"})
	if w.Code != http.StatusCreated {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
}

func TestDispatchFreezesProfileBinding(t *testing.T) {
	s, p := profileServer(t, testProfiles()...)
	if err := p.SetNodeBinding([]string{"local"}, "local"); err != nil {
		t.Fatal(err)
	}
	rec := &recordExec{}
	s.Exec = rec
	in, err := p.Store.CreateIntent(model.Intent{Title: "查", Description: "d", Profile: "slow"})
	if err != nil {
		t.Fatal(err)
	}
	runID, err := s.StartRun(in.ID)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	waitRun(t, p, runID)
	run, ok := p.Store.GetRun(runID)
	if !ok {
		t.Fatal("run missing")
	}
	if run.Profile != "slow" || run.ResolvedProfile != "slow" || run.ProfileRevision != "rev-slow" ||
		run.Provider != "anthropic" || run.ProfileSource != "explicit" || run.Tier != "strong" {
		t.Fatalf("run binding = %+v", run)
	}
	if run.Model != "actual-model" {
		t.Fatalf("actual model = %q", run.Model)
	}
	if run.Node != "local" {
		t.Fatalf("node = %q", run.Node)
	}
	if run.ContextWindow != 64000 {
		t.Fatalf("context window = %d, want the profile's 64000", run.ContextWindow)
	}
	if req := rec.Request(); req.Profile != "slow" || req.Node != "local" {
		t.Fatalf("request = %+v", req)
	}
	if got, _ := p.Store.GetIntent(in.ID); got.Node != "local" {
		t.Fatalf("intent node = %q", got.Node)
	}
}

// An intent without a profile still records which instance default it used, so
// switching the active profile later does not make history ambiguous. The
// default path freezes the profile's tier on the run like the explicit one.
func TestDispatchRecordsDefaultProfile(t *testing.T) {
	s, p := profileServer(t, testProfiles()...)
	if err := p.SetNodeBinding([]string{"local"}, "local"); err != nil {
		t.Fatal(err)
	}
	s.Exec = &recordExec{}
	in, err := p.Store.CreateIntent(model.Intent{Title: "查", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	runID, err := s.StartRun(in.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, p, runID)
	run, _ := p.Store.GetRun(runID)
	if run.Profile != "" || run.ResolvedProfile != "fast" || run.ProfileSource != "default" ||
		run.Provider != "openai" || run.Model != "actual-model" || run.Tier != "strong" {
		t.Fatalf("default binding = %+v", run)
	}
}

func TestModelProfilesEndpointHidesCredentials(t *testing.T) {
	s, _ := profileServer(t, testProfiles()...)
	w := call(t, s, http.MethodGet, "/api/model-profiles", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Profiles  []ProfileInfo `json:"profiles"`
		Active    string        `json:"active"`
		LocalNode string        `json:"local_node"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Profiles) != 3 || resp.Active != "fast" || resp.LocalNode != "local" {
		t.Fatalf("resp = %+v", resp)
	}
	if strings.Contains(w.Body.String(), "api_key") {
		t.Fatal("profile list exposed a credential field")
	}
}

func waitRun(t *testing.T, p *project.Project, runID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if r, ok := p.Store.GetRun(runID); ok && r.Status != model.RunRunning {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("run did not settle")
}
