package api

// Project model defaults resolve into actual routing and token observations.

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/project"
)

func tierProfiles() []ProfileInfo {
	return []ProfileInfo{
		{
			ID: "fast", Provider: "openai", Model: "fast-model",
			ContextWindow: 32000, Revision: "rev-fast", Runnable: true, Active: true,
			Tier: "strong",
		},
		{
			ID: "cheap", Provider: "openai", Model: "cheap-model",
			ContextWindow: 16000, Revision: "rev-cheap", Runnable: true, Tier: "worker",
		},
		{
			ID: "strong", Provider: "anthropic", Model: "strong-model",
			ContextWindow: 200000, Revision: "rev-strong", Runnable: true, Tier: "strong",
		},
	}
}

func projectTierServer(t *testing.T, defaults *ProfileDefaults) (*Server, *project.Project) {
	t.Helper()
	s, p := profileServer(t, tierProfiles()...)
	s.ProjectProfiles = defaults
	if err := p.SetNodeBinding([]string{"local"}, "local"); err != nil {
		t.Fatal(err)
	}
	s.Exec = &recordExec{}
	return s, p
}

// An intent that names no profile gets the project's worker tier, not the
// instance default: cheap is the default, strong is the exception.
func TestEmptyProfileIntentRunsOnProjectWorkerProfile(t *testing.T) {
	s, p := projectTierServer(t, &ProfileDefaults{Worker: "cheap"})
	in, err := p.Store.CreateIntent(model.Intent{Title: "核对", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	runID, err := s.StartRun(in.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, p, runID)
	run, _ := p.Store.GetRun(runID)
	if run.Profile != "cheap" || run.ResolvedProfile != "cheap" || run.ProfileSource != "project_worker" ||
		run.Provider != "openai" || run.ContextWindow != 16000 || run.Tier != "worker" {
		t.Fatalf("project worker binding = %+v", run)
	}
	// The run record is not enough: the resolved profile must reach the executor,
	// or the run silently executes on the default host while claiming the worker
	// tier (the bug this assertion pins).
	if got := s.Exec.(*recordExec).Request().Profile; got != "cheap" {
		t.Fatalf("executor request profile = %q, want the project worker tier", got)
	}
}

// An explicit profile always beats the project default (and is never replaced
// silently).
func TestExplicitProfileStillWinsOverProjectWorkerProfile(t *testing.T) {
	s, p := projectTierServer(t, &ProfileDefaults{Worker: "cheap"})
	in, err := p.Store.CreateIntent(model.Intent{Title: "升级", Description: "d", Profile: "strong"})
	if err != nil {
		t.Fatal(err)
	}
	runID, err := s.StartRun(in.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, p, runID)
	run, _ := p.Store.GetRun(runID)
	if run.Profile != "strong" || run.ProfileSource != "explicit" || run.Provider != "anthropic" ||
		run.Tier != "strong" {
		t.Fatalf("explicit binding = %+v", run)
	}
}

// A child delegated without a profile is a worker even when the parent is on
// the strong tier: the default chain is applied per intent, not inherited from
// the parent (sapper's -p chores run the cheap model).
func TestChildIntentWithoutProfileInheritsProjectWorkerProfile(t *testing.T) {
	s, p := projectTierServer(t, &ProfileDefaults{Worker: "cheap"})
	parent, err := p.Store.CreateIntent(model.Intent{Title: "父方向", Description: "d", Profile: "strong"})
	if err != nil {
		t.Fatal(err)
	}
	w := call(t, s, http.MethodPost, "/api/intents/"+parent.ID+"/children",
		map[string]string{"title": "子任务：机械核对"})
	if w.Code != http.StatusCreated {
		t.Fatalf("create child = %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Child model.Intent `json:"child"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Child.Profile != "" {
		t.Fatalf("child inherited the parent's profile: %q", resp.Child.Profile)
	}
	children := p.Store.Children(parent.ID)
	if len(children) != 1 || children[0].RunID == "" {
		t.Fatalf("child did not start: %+v", children)
	}
	waitRun(t, p, children[0].RunID)
	run, _ := p.Store.GetRun(children[0].RunID)
	if run.Profile != "cheap" || run.ProfileSource != "project_worker" {
		t.Fatalf("child run binding = %+v", run)
	}
}

// usageCommanderExec is a commander stub that reports real usage and the model
// it resolved, which is what StartCommander must freeze on the run.
type usageCommanderExec struct {
	req    executor.Request
	res    executor.Result
	model  string
	resume bool
}

func (e *usageCommanderExec) Name() string { return "usage-commander" }

func (e *usageCommanderExec) Run(_ context.Context, req executor.Request, emit func(executor.Event)) (executor.Result, error) {
	e.req = req
	if e.model != "" {
		emit(executor.Event{Kind: executor.KindSession, Text: e.model})
	}
	return e.res, nil
}

func (e *usageCommanderExec) CanResume(node, workDir string) bool { return e.resume }

func commanderWithProfiles(t *testing.T, ex *usageCommanderExec, defaults *ProfileDefaults) (*Server, *usageCommanderExec) {
	t.Helper()
	s, _, _ := newCommanderServer(t)
	s.Exec = ex
	s.Profiles = func() ([]ProfileInfo, string, error) {
		return tierProfiles(), "fast", nil
	}
	s.ProjectProfiles = defaults
	return s, ex
}

// Commander tokens are observed exactly like worker tokens.
func TestCommanderRunRecordsOnlyTokenUsage(t *testing.T) {
	ex := &usageCommanderExec{
		model: "fast-alias-resolved",
		res: executor.Result{
			ExitCode: 0, StopReason: "completed",
			Tokens: 300, InputTokens: 200, OutputTokens: 100,
			CachedInputTokens: 0, UsageRequests: 1, UsageKnown: true,
		},
	}
	s, _ := commanderWithProfiles(t, ex, nil)
	if _, _, err := s.StartCommander(context.Background(), "## 本轮", false); err != nil {
		t.Fatal(err)
	}
	runs := s.Store.Runs()
	if len(runs) != 1 {
		t.Fatalf("runs = %d", len(runs))
	}
	run := runs[0]
	if !run.Usage.Known || run.Usage.InputTokens != 200 || run.Usage.OutputTokens != 100 ||
		run.Usage.TotalTokens != 300 || run.Usage.Requests != 1 {
		t.Fatalf("usage = %+v", run.Usage)
	}
	if run.Cost != nil || run.Pricing != nil {
		t.Fatal("new commander runs only record tokens")
	}
}

// Without pricing the round reports usage but must not fabricate a cost.
func TestCommanderRunWithoutPricingHasNoCost(t *testing.T) {
	ex := &usageCommanderExec{res: executor.Result{
		ExitCode: 0, StopReason: "completed", Tokens: 10,
		InputTokens: 3, OutputTokens: 7, UsageRequests: 1, UsageKnown: true,
	}}
	s, _, _ := newCommanderServer(t)
	s.Exec = ex
	// The instance active profile exists but has no pricing.
	s.Profiles = func() ([]ProfileInfo, string, error) {
		return []ProfileInfo{{ID: "plain", Provider: "openai", Model: "m", Runnable: true, Active: true, Revision: "r"}}, "plain", nil
	}
	if _, _, err := s.StartCommander(context.Background(), "## 本轮", false); err != nil {
		t.Fatal(err)
	}
	run := s.Store.Runs()[0]
	if !run.Usage.Known {
		t.Fatalf("usage = %+v", run.Usage)
	}
	if run.Cost != nil {
		t.Fatalf("cost = %+v, want nil when no pricing is configured", run.Cost)
	}
}

// The bound profile names the provider/model the round started with; the
// harness's session event corrects it to the model actually resolved.
func TestCommanderRunRecordsProviderAndModel(t *testing.T) {
	ex := &usageCommanderExec{model: "fast-actual", res: executor.Result{ExitCode: 0, StopReason: "completed"}}
	s, _ := commanderWithProfiles(t, ex, nil)
	if _, _, err := s.StartCommander(context.Background(), "## 本轮", false); err != nil {
		t.Fatal(err)
	}
	run := s.Store.Runs()[0]
	if run.Provider != "openai" || run.Model != "fast-actual" {
		t.Fatalf("provider/model = %q/%q", run.Provider, run.Model)
	}
	if run.ResolvedProfile != "fast" || run.ProfileSource != "default" || run.Tier != "strong" {
		t.Fatalf("binding = %+v", run)
	}
}

// A selected project commander model is the actual routing target.
func TestCommanderStrongProfileRoutesTheActualRun(t *testing.T) {
	ex := &usageCommanderExec{res: executor.Result{ExitCode: 0, StopReason: "completed"}}
	s, ex := commanderWithProfiles(t, ex, &ProfileDefaults{Strong: "strong"})
	if _, _, err := s.StartCommander(context.Background(), "round", true); err != nil {
		t.Fatal(err)
	}
	run := s.Store.Runs()[0]
	if ex.req.Profile != "strong" || ex.req.Resume || run.Profile != "strong" || run.ResolvedProfile != "strong" || run.Provider != "anthropic" || run.Model != "strong-model" || run.ContextWindow != 200000 {
		t.Fatalf("request=%+v run=%+v", ex.req, run)
	}
	if !strings.HasPrefix(ex.req.Prompt, "/skill:commander ") {
		t.Fatal("new host must get its own skill/board/state input")
	}
}

func TestCommanderMissingStrongProfileNeverFallsBack(t *testing.T) {
	ex := &usageCommanderExec{}
	s, _ := commanderWithProfiles(t, ex, &ProfileDefaults{Strong: "missing"})
	if _, _, err := s.StartCommander(context.Background(), "round", false); err == nil {
		t.Fatal("missing selection fell back to default")
	}
	if len(s.Store.Runs()) != 0 {
		t.Fatal("invalid profile started a run")
	}
}

// The round's prompt is not stored twice, but the decision to inject the whole
// note must be provable from the run log: runs/<id>.jsonl gets a kind=injected
// event when the full section rode the prompt, and nothing when only a pointer
// did (docs/17 P0-2).
func TestCommanderStateInjectionIsJournaled(t *testing.T) {
	ex := &usageCommanderExec{res: executor.Result{ExitCode: 0, StopReason: "completed"}}
	s, _, st := newCommanderServer(t)
	s.Exec = ex
	full := "## 本轮\nboard 更新了。\n## 你的笔记（state.md，全文）\n唯一标记\n\n## board\n{}\n"
	if _, _, err := s.StartCommander(context.Background(), full, false); err != nil {
		t.Fatal(err)
	}
	run := st.Runs()[0]
	data, err := os.ReadFile(filepath.Join(s.DataDir, "runs", run.ID+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	journal := string(data)
	if !strings.Contains(journal, `"kind":"injected"`) || !strings.Contains(journal, `"source":"state"`) {
		t.Fatalf("full-note injection was not journaled:\n%s", journal)
	}
	if !strings.Contains(journal, `"prompt_section":"state"`) {
		t.Fatalf("injection event has no prompt section:\n%s", journal)
	}

	// A normal continued round (pointer only) must not claim to have injected
	// the note.
	pointer := "## 本轮\nboard 更新了。\n## 你的笔记（state.md）\n本会话里已有它。\n- API：GET /api/state\n\n## board\n{}\n"
	if _, _, err := s.StartCommander(context.Background(), pointer, false); err != nil {
		t.Fatal(err)
	}
	for _, r := range st.Runs() {
		if r.ID == run.ID {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.DataDir, "runs", r.ID+".jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), `"source":"state"`) {
			t.Fatalf("pointer-only prompt was journaled as a full injection:\n%s", data)
		}
	}
}
