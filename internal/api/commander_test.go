package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/project"
	"github.com/kksty/tianwu/internal/store"
)

type commanderExec struct {
	req       executor.Request
	runs      int
	resumable bool
	window    int
	during    func()
}

func (e *commanderExec) Name() string { return "commander-stub" }

func (e *commanderExec) Run(ctx context.Context, req executor.Request, emit func(executor.Event)) (executor.Result, error) {
	e.runs++
	e.req = req
	if e.during != nil {
		e.during()
	}
	return executor.Result{ExitCode: 0, ContextWindow: e.window}, nil
}

func (e *commanderExec) CanResume(node, workDir string) bool { return e.resumable }

func newCommanderServer(t *testing.T) (*Server, *commanderExec, *store.Store) {
	t.Helper()
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	st, err := store.Open(filepath.Join(projectDir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	ex := &commanderExec{}
	s := New(st, ex, projectDir, filepath.Join(projectDir, "cases"))
	s.Project = &project.Project{Dir: projectDir, Manifest: project.Manifest{ID: "default"}}
	s.APIBase = "http://127.0.0.1:9000"
	return s, ex, st
}

// A first commander turn must load the skill through harness and carry the
// control-plane coordinates; tianwu never pastes the skill body itself (D78).
func TestStartCommanderLoadsSkillOnFirstTurn(t *testing.T) {
	s, ex, st := newCommanderServer(t)
	if _, _, err := s.StartCommander(context.Background(), "## 本轮\nboard", false); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ex.req.Prompt, "/skill:commander ") {
		t.Fatalf("prompt = %q", ex.req.Prompt)
	}
	for _, needle := range []string{"http://127.0.0.1:9000", "TW_AGENT_TOKEN", "## 本轮"} {
		if !strings.Contains(ex.req.Prompt, needle) {
			t.Fatalf("prompt missing %q: %q", needle, ex.req.Prompt)
		}
	}
	if ex.req.Resume {
		t.Fatal("a new session must not be marked resume")
	}
	if want := filepath.Join(filepath.Dir(s.CaseRoot), "commander"); ex.req.WorkDir != want {
		t.Fatalf("workdir = %q, want %q", ex.req.WorkDir, want)
	}
	runs := st.Runs()
	if len(runs) != 1 || runs[0].Purpose != "commander" || runs[0].Status != model.RunDone {
		t.Fatalf("runs = %+v", runs)
	}
	// The console compares this run's context against the same window the
	// handoff line is measured against (D80).
	if runs[0].ContextWindow != s.ModelContext() {
		t.Fatalf("context window = %d, want %d", runs[0].ContextWindow, s.ModelContext())
	}
}

// The harness resolves the model's real window and reports it with usage; the
// run records that value, not the profile fallback (D80).
func TestStartCommanderRecordsHarnessWindow(t *testing.T) {
	s, ex, st := newCommanderServer(t)
	ex.window = 1000000
	s.SetContextWindow(128000)
	if _, _, err := s.StartCommander(context.Background(), "## 本轮", false); err != nil {
		t.Fatal(err)
	}
	runs := st.Runs()
	if len(runs) != 1 || runs[0].ContextWindow != 1000000 {
		t.Fatalf("runs = %+v, want the harness-reported 1000000", runs)
	}
}

// A session that still exists gets only the new round; the skill body is
// already in its context.
func TestStartCommanderResumesWithoutSkillPrefix(t *testing.T) {
	s, ex, _ := newCommanderServer(t)
	ex.resumable = true
	if _, _, err := s.StartCommander(context.Background(), "## 本轮\nboard", true); err != nil {
		t.Fatal(err)
	}
	if !ex.req.Resume {
		t.Fatal("resume = false, want true")
	}
	if strings.HasPrefix(ex.req.Prompt, "/skill:commander") {
		t.Fatalf("resumed prompt re-loads the skill: %q", ex.req.Prompt)
	}
}

// The commander's write endpoints go through the same rules as the decider's
// actions: a goal without evidence, a reason or a playbook without provenance
// are all refused (D55, D70, D78, D96).
func TestCommanderActionEndpointsShareTheRules(t *testing.T) {
	s, _, st := newCommanderServer(t)
	mux := http.NewServeMux()
	s.Register(mux)

	g, err := st.AddGoal(model.Goal{Statement: "拿到 shell"})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/goals/"+g.ID+"/complete",
		strings.NewReader(`{"from":["f_missing"],"note":"范围已覆盖"}`)))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "不存在的事实") {
		t.Fatalf("complete = %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/goals/"+g.ID+"/complete",
		strings.NewReader(`{"from":["f_missing"]}`)))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "必须写 note") {
		t.Fatalf("complete without a reason = %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/playbooks/nominate",
		strings.NewReader(`{"title":"x","when":"y"}`)))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "已有事实") {
		t.Fatalf("nominate = %d %s", rec.Code, rec.Body.String())
	}
}

// The round's decision log is where the console reads back "why did it close":
// recording the goal statement there said what was closed, never why (D96).
func TestCompleteActionRecordsTheReason(t *testing.T) {
	s, _, st := newCommanderServer(t)
	s.Project = nil
	mux := http.NewServeMux()
	s.Register(mux)

	ev, err := st.AddEvidence(model.Evidence{Name: "p.txt", URI: "p.txt"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := st.AddFact(model.Fact{Kind: "vuln", Title: "已复核", Status: "verified", EvidenceIDs: []string{ev.ID}})
	if err != nil {
		t.Fatal(err)
	}
	g, err := st.AddGoal(model.Goal{Statement: "拿到 shell"})
	if err != nil {
		t.Fatal(err)
	}
	mark := s.ActionMark()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/goals/"+g.ID+"/complete",
		strings.NewReader(`{"from":["`+f.ID+`"],"note":"复核通过：验证码可跳过，实测可反复试口令"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("complete = %d %s", rec.Code, rec.Body.String())
	}
	acts := s.ActionsSince(mark)
	if len(acts) != 1 || acts[0].Kind != "complete" || acts[0].ID != g.ID {
		t.Fatalf("actions = %+v", acts)
	}
	if acts[0].Text != "复核通过：验证码可跳过，实测可反复试口令" {
		t.Fatalf("action text = %q, want the reason, not the statement", acts[0].Text)
	}
}

// The operator's manual pin carries the same receipt: a met goal the page
// cannot explain is exactly the state D96 forbids.
func TestOperatorPinRequiresReason(t *testing.T) {
	s, _, st := newCommanderServer(t)
	s.Project = nil
	mux := http.NewServeMux()
	s.Register(mux)

	ev, err := st.AddEvidence(model.Evidence{Name: "p.txt", URI: "p.txt"})
	if err != nil {
		t.Fatal(err)
	}
	g, err := st.AddGoal(model.Goal{Statement: "人工盖章"})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/goals/"+g.ID+"/met",
		strings.NewReader(`{"evidence_id":"`+ev.ID+`"}`)))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "必须写理由") {
		t.Fatalf("met without a reason = %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/goals/"+g.ID+"/met",
		strings.NewReader(`{"evidence_id":"`+ev.ID+`","note":"操作员核对原始响应后确认"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("met = %d %s", rec.Code, rec.Body.String())
	}
	var got model.Goal
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Met || got.Note != "操作员核对原始响应后确认" || got.CompletedAt == nil {
		t.Fatalf("goal = %+v", got)
	}
}

// Withdrawing a stamp is the operator's correction path: the goal leaves
// "已达成" and re-enters the open list with its receipt cleared; a goal that is
// already open is refused rather than reported as reopened (D98).
func TestOperatorReopenWithdrawsTheStamp(t *testing.T) {
	s, _, st := newCommanderServer(t)
	s.Project = nil
	mux := http.NewServeMux()
	s.Register(mux)

	ev, err := st.AddEvidence(model.Evidence{Name: "p.txt", URI: "p.txt"})
	if err != nil {
		t.Fatal(err)
	}
	g, err := st.AddGoal(model.Goal{Statement: "人工盖章"})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/goals/"+g.ID+"/met",
		strings.NewReader(`{"evidence_id":"`+ev.ID+`","note":"操作员核对原始响应后确认"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("met = %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/goals/"+g.ID+"/reopen", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("reopen = %d %s", rec.Code, rec.Body.String())
	}
	var got model.Goal
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Met || got.EvidenceID != "" || got.Note != "" || got.CompletedAt != nil {
		t.Fatalf("reopened goal = %+v", got)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/goals/"+g.ID+"/reopen", nil))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "没有盖章") {
		t.Fatalf("second reopen = %d %s", rec.Code, rec.Body.String())
	}
}

// A round reports the mutations performed through the API while it ran, and
// takes them once so the next round does not inherit them (D78).
func TestStartCommanderReportsRoundActions(t *testing.T) {
	s, ex, _ := newCommanderServer(t)
	ex.during = func() { s.recordAction("intent", "in_1", "验证 /admin") }
	_, acts, err := s.StartCommander(context.Background(), "## 本轮", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(acts) != 1 || acts[0].Kind != "intent" || acts[0].ID != "in_1" {
		t.Fatalf("actions = %+v", acts)
	}
	if got := s.ActionsSince(0); len(got) != 0 {
		t.Fatalf("actions replayed: %+v", got)
	}
}

func TestActionsSinceRoundTrip(t *testing.T) {
	s, _, _ := newCommanderServer(t)
	mark := s.ActionMark()
	s.recordAction("intent", "in_1", "x")
	got := s.ActionsSince(mark)
	if len(got) != 1 {
		t.Fatalf("actions = %+v", got)
	}
}

// The handlers that mutate the ledger record what they did, so a round can
// report real actions instead of the model's own account (D78).
func TestMutatingEndpointsRecordActions(t *testing.T) {
	s, _, _ := newCommanderServer(t)
	s.Project = nil // no node binding required for this path
	mux := http.NewServeMux()
	s.Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/intents",
		strings.NewReader(`{"title":"t","description":"d"}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	acts := s.ActionsSince(0)
	if len(acts) != 1 || acts[0].Kind != "intent" || acts[0].ID == "" {
		t.Fatalf("actions = %+v", acts)
	}
}

// A handoff round writes state.md through the API. If that write is not in the
// round's actions, "did the 85% round actually hand off?" is unanswerable from
// decisions.jsonl (D80).
func TestStateWriteIsRecordedAsAnAction(t *testing.T) {
	s, _, _ := newCommanderServer(t)
	mux := http.NewServeMux()
	s.Register(mux)
	mark := s.ActionMark()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/state",
		strings.NewReader(`{"state":"# 交接\n- 下一步：验 /admin"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("put state = %d %s", rec.Code, rec.Body.String())
	}
	acts := s.ActionsSince(mark)
	if len(acts) != 1 || acts[0].Kind != "state" || !strings.Contains(acts[0].Text, "交接") {
		t.Fatalf("actions = %+v", acts)
	}
}
