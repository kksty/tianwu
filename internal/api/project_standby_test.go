package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/decider"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/project"
)

// The commander's stop request is a real project pause that only the operator can
// undo, and the round that asked for it must be allowed to finish. The failure
// this covers: the commander could only park a direction, so the parked result and
// an already-answered native message woke it again for a round that did nothing
// (431k tokens on the unreachable-target project).
func TestCommanderStandbyPausesProjectAndSparesTheAskingRound(t *testing.T) {
	s, p := newBindingServer(t)
	s.Decider = decider.NewRunner(s.Store, nil, "")
	s.Decider.ModelName = "fixture"
	s.Decider.Commander = func(context.Context, string, bool) (decider.RoundResult, error) {
		t.Error("standby did not stop the next commander round")
		return decider.RoundResult{}, nil
	}
	// The asking round: a live commander purpose run, plus an unrelated purpose
	// run that the stop must cancel.
	cmdRun, err := s.Store.AddRun(model.Run{Purpose: "commander"})
	if err != nil {
		t.Fatal(err)
	}
	cmdCtx, cmdCancel := context.WithCancel(context.Background())
	defer cmdCancel()
	cmdSettle := s.TrackPurposeRun(cmdRun.ID, cmdCancel)
	otherCtx, otherCancel := context.WithCancel(context.Background())
	defer otherCancel()
	otherSettle := s.TrackPurposeRun("r_other_purpose", otherCancel)

	mux := http.NewServeMux()
	s.Register(mux)
	do := func(request *http.Request) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, request)
		return rec
	}

	// The operator cannot borrow the commander's action; the operator switch is
	// /api/project/pause.
	rec := do(httptest.NewRequest(http.MethodPost, "/api/project/standby",
		strings.NewReader(`{"reason":"目标不可达"}`)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("operator standby = %d, want 403: %s", rec.Code, rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodPost, "/api/project/standby",
		strings.NewReader(`{"reason":"目标不可达"}`))
	req.Header.Set("Content-Type", "application/json")
	// The gate stamps this header for the shared harness credential; the handler
	// additionally proves the caller is a commander and that a commander round is
	// live (see the negative cases below).
	req.Header.Set("X-Tw-Actor", collab.CommanderID)
	req = req.WithContext(WithAgentSurface(req.Context()))
	rec = do(req)
	if rec.Code != http.StatusOK {
		t.Fatalf("commander standby = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Paused   bool `json:"paused"`
		Canceled int  `json:"canceled"`
		Reason   string
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Paused || out.Canceled != 1 {
		t.Fatalf("standby response = %+v, want paused with exactly the other run canceled", out)
	}
	if !s.paused.Load() || !s.Decider.Paused() {
		t.Fatal("standby did not gate the project and the commander")
	}
	if cause := otherSettle(); cause != model.CancelCausePause {
		t.Fatalf("other purpose run cause = %q, want the pause", cause)
	}
	select {
	case <-otherCtx.Done():
	default:
		t.Fatal("other purpose run was not canceled")
	}
	// The asking round was spared: it still has to write its answer and note.
	if cause := cmdSettle(); cause != "" {
		t.Fatalf("asking commander run was canceled: %q", cause)
	}
	if cmdCtx.Err() != nil {
		t.Fatal("asking commander run context was canceled")
	}
	if st := s.projectState(); st.Kind != pauseKindCommanderStandby || !strings.Contains(st.Reason, "目标不可达") {
		t.Fatalf("project state = %+v, want commander_standby with the reason", st)
	}
	if !containsKind(actionsKinds(s), "standby") {
		t.Fatalf("standby action not recorded: %v", actionsKinds(s))
	}
	m, err := project.Open(filepath.Dir(p.Dir))
	if err != nil {
		t.Fatal(err)
	}
	if !m.Get(p.ID).IsPaused() {
		t.Fatal("commander standby was not persisted")
	}
	if d := s.Decider.Run(context.Background(), "heartbeat", true); d != nil {
		t.Fatalf("paused commander ran after standby: %+v", d)
	}

	// The console reads the machine kind, not the reason prose.
	boardRec := do(httptest.NewRequest(http.MethodGet, "/api/board", nil))
	if boardRec.Code != http.StatusOK {
		t.Fatalf("board = %d: %s", boardRec.Code, boardRec.Body.String())
	}
	var boardBody struct {
		ProjectPaused    bool   `json:"project_paused"`
		ProjectPauseKind string `json:"project_pause_kind"`
	}
	if err := json.Unmarshal(boardRec.Body.Bytes(), &boardBody); err != nil {
		t.Fatal(err)
	}
	if !boardBody.ProjectPaused || boardBody.ProjectPauseKind != pauseKindCommanderStandby {
		t.Fatalf("board pause = %+v, want commander_standby", boardBody)
	}

	// Operator recovery is the explicit append-and-continue action: the paused
	// project is resumed from the commander stop and the new goal is kept.
	rec = do(httptest.NewRequest(http.MethodPost, "/api/project/append-goal",
		strings.NewReader(`{"statement":"验证新入口","idempotency_key":"standby-resume-1"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("append-goal after standby = %d: %s", rec.Code, rec.Body.String())
	}
	var resumed struct {
		Resumed     bool   `json:"resumed"`
		ResumedFrom string `json:"resumed_from"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resumed); err != nil {
		t.Fatal(err)
	}
	if !resumed.Resumed || resumed.ResumedFrom != pauseKindCommanderStandby {
		t.Fatalf("resume = %+v, want resumed_from commander_standby", resumed)
	}
	if s.paused.Load() || s.Decider.Paused() {
		t.Fatal("explicit continue did not clear the commander standby")
	}
	if m, err = project.Open(filepath.Dir(p.Dir)); err != nil {
		t.Fatal(err)
	}
	if m.Get(p.ID).IsPaused() {
		t.Fatal("commander standby stayed in the manifest after the explicit continue")
	}
}

// The commander's global stop is a declared role action, not a credential every
// harness session may use: the handler needs a commander attribution (the auth
// gate stamps X-Tw-Actor for the shared harness credential; a commander
// participant identity also counts) and a live commander round. This is a
// declarative boundary for a trusted internal session, not a sandbox (D78).
func TestProjectStandbyRequiresCommanderDeclaration(t *testing.T) {
	s, _ := newBindingServer(t)
	if _, err := s.Store.AddRun(model.Run{Purpose: "commander"}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.Register(mux)
	do := func(req *http.Request) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// The operator's own switch is /api/project/pause; it must not borrow the
	// commander action.
	if rec := do(standbyRequest(t, `{"reason":"越权"}`)); rec.Code != http.StatusForbidden {
		t.Fatalf("operator standby = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	// A harness session without a commander attribution is refused as well.
	undeclared := standbyRequest(t, `{"reason":"无声明"}`)
	undeclared = undeclared.WithContext(WithAgentSurface(undeclared.Context()))
	if rec := do(undeclared); rec.Code != http.StatusForbidden {
		t.Fatalf("undeclared standby = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	if s.paused.Load() {
		t.Fatal("a refused standby paused the project")
	}
	// A commander participant identity is also a valid declaration.
	identity := standbyRequest(t, `{"reason":"身份声明"}`)
	identity = identity.WithContext(WithIdentity(identity.Context(), collab.Identity{
		ProjectID: s.Project.ID, ParticipantID: collab.CommanderID,
	}))
	if rec := do(identity); rec.Code != http.StatusOK {
		t.Fatalf("identity-declared standby = %d: %s", rec.Code, rec.Body.String())
	}
	if !s.paused.Load() {
		t.Fatal("identity-declared standby did not pause the project")
	}
}

// A declaration without a live commander round is not a stop request the control
// plane can attribute to the current round: refuse it instead of pausing a live
// project on a stale/forged call.
func TestProjectStandbyRequiresLiveCommanderRound(t *testing.T) {
	s, _ := newBindingServer(t)
	if _, live := s.liveCommanderRun(); live {
		t.Fatal("fixture already has a live commander run")
	}
	mux := http.NewServeMux()
	s.Register(mux)
	req := standbyRequest(t, `{"reason":"没有在跑的轮次"}`)
	req.Header.Set("X-Tw-Actor", collab.CommanderID)
	req = req.WithContext(WithAgentSurface(req.Context()))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("standby without a live commander round = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if s.paused.Load() {
		t.Fatal("an invalid standby paused the project")
	}
	if containsKind(actionsKinds(s), "standby") {
		t.Fatal("an invalid standby was recorded as an action")
	}
}

// standbyRequest is the wire shape every standby case shares: the reason body is
// optional to the handler, and the surface/declaration headers are added per
// case.
func standbyRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/project/standby", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

// actionsKinds is the one-line observation the test needs from the round action
// log; the decision log is asserted through the real ActionsSince path elsewhere.
func actionsKinds(s *Server) []string {
	s.recentMu.Lock()
	defer s.recentMu.Unlock()
	kinds := make([]string, 0, len(s.recent))
	for _, a := range s.recent {
		kinds = append(kinds, a.Kind)
	}
	return kinds
}

func containsKind(kinds []string, want string) bool {
	for _, kind := range kinds {
		if kind == want {
			return true
		}
	}
	return false
}
