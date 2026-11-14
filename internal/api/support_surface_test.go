package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/model"
)

// Report writing and verification are operator-side post-processing. A harness
// session (commander or executor) only does the test itself, so the support runs
// and the review queue must be absent from its API surface — not merely denied.

func agentRequest(s *Server, method, path string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	return req.WithContext(WithIdentity(req.Context(), collab.Identity{
		ProjectID:     s.Project.ID,
		ParticipantID: collab.CommanderID,
	}))
}

func serveMux(t *testing.T, s *Server, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	s.Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestAgentRunListHidesReportAndVerificationRuns(t *testing.T) {
	s, _ := newBindingServer(t)
	intentRun, err := s.Store.AddRun(model.Run{IntentID: "in_visible", Purpose: ""})
	if err != nil {
		t.Fatal(err)
	}
	reportRun, err := s.Store.AddRun(model.Run{Purpose: "report"})
	if err != nil {
		t.Fatal(err)
	}
	verifyRun, err := s.Store.AddRun(model.Run{Purpose: "verifier"})
	if err != nil {
		t.Fatal(err)
	}

	decode := func(rec *httptest.ResponseRecorder) map[string]bool {
		t.Helper()
		var body struct {
			Runs []model.Run `json:"runs"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode runs: %v (%s)", err, rec.Body.String())
		}
		seen := map[string]bool{}
		for _, run := range body.Runs {
			seen[run.ID] = true
		}
		return seen
	}

	agent := decode(serveMux(t, s, agentRequest(s, http.MethodGet, "/api/runs")))
	if !agent[intentRun.ID] || agent[reportRun.ID] || agent[verifyRun.ID] {
		t.Fatalf("agent runs = %+v (intent %s, report %s, verify %s)", agent, intentRun.ID, reportRun.ID, verifyRun.ID)
	}

	operator := decode(serveMux(t, s, httptest.NewRequest(http.MethodGet, "/api/runs", nil)))
	for _, id := range []string{intentRun.ID, reportRun.ID, verifyRun.ID} {
		if !operator[id] {
			t.Fatalf("operator list must keep every run (%s missing): %+v", id, operator)
		}
	}
}

func TestAgentCannotReadOrCancelASupportRun(t *testing.T) {
	s, _ := newBindingServer(t)
	reportRun, err := s.Store.AddRun(model.Run{Purpose: "report"})
	if err != nil {
		t.Fatal(err)
	}
	// A real journal exists: the 404 the agent gets must be the surface boundary,
	// not a missing-file accident.
	runsDir := filepath.Join(s.DataDir, "runs")
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runsDir, reportRun.ID+".jsonl"), []byte("{\"kind\":\"message\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	agentEvents := serveMux(t, s, agentRequest(s, http.MethodGet, "/api/runs/"+reportRun.ID+"/events"))
	if agentEvents.Code != http.StatusNotFound {
		t.Fatalf("agent report events = %d, want 404: %s", agentEvents.Code, agentEvents.Body.String())
	}
	operatorEvents := serveMux(t, s, httptest.NewRequest(http.MethodGet, "/api/runs/"+reportRun.ID+"/events", nil))
	if operatorEvents.Code != http.StatusOK {
		t.Fatalf("operator report events = %d, want 200: %s", operatorEvents.Code, operatorEvents.Body.String())
	}

	agentCancel := serveMux(t, s, agentRequest(s, http.MethodPost, "/api/runs/"+reportRun.ID+"/cancel"))
	if agentCancel.Code != http.StatusNotFound {
		t.Fatalf("agent report cancel = %d, want 404: %s", agentCancel.Code, agentCancel.Body.String())
	}
}

// The guard cadence and last wake are control-plane supervision. The commander
// gets the battlefield, not the watchdog's clock.
func TestCommanderBoardHidesTheGuardAndHeartbeat(t *testing.T) {
	s, _ := newBindingServer(t)
	decode := func(req *http.Request) map[string]any {
		t.Helper()
		rec := serveMux(t, s, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("board = %d: %s", rec.Code, rec.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	operator := decode(httptest.NewRequest(http.MethodGet, "/api/board", nil))
	if _, ok := operator["guard"]; !ok {
		t.Fatal("operator board lost the guard pulse")
	}
	commander := decode(harnessRequest(http.MethodGet, "/api/board"))
	if _, ok := commander["guard"]; ok {
		t.Fatalf("commander board leaked the guard: %v", commander["guard"])
	}
	// The commander sees only the operational state it can act on; every other
	// decider field (rounds, tokens, uptime, last wake, attention batches, live
	// activity) is operator supervision and must not cross the boundary (§8).
	allowed := map[string]bool{"enabled": true, "paused": true, "paused_reason": true}
	d, ok := commander["decider"].(map[string]any)
	if !ok {
		t.Fatalf("commander board lost the decider state: %v", commander["decider"])
	}
	for key := range d {
		if !allowed[key] {
			t.Fatalf("commander board leaked decider.%s: %v", key, d[key])
		}
	}
	if _, ok := d["enabled"]; !ok {
		t.Fatalf("commander board must keep decider.enabled: %v", d)
	}
}

func TestAgentBoardHidesTheReviewQueue(t *testing.T) {
	s, _ := newBindingServer(t)
	s.SetReviewsPaused(true)

	read := func(req *http.Request) reviewSummary {
		t.Helper()
		rec := serveMux(t, s, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("board = %d: %s", rec.Code, rec.Body.String())
		}
		var body struct {
			Reviews reviewSummary `json:"reviews"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode board: %v", err)
		}
		return body.Reviews
	}

	if got := read(httptest.NewRequest(http.MethodGet, "/api/board", nil)); !got.Paused {
		t.Fatalf("operator board must show the review queue (paused=%v): %+v", got.Paused, got)
	}
	if got := read(agentRequest(s, http.MethodGet, "/api/board")); got != (reviewSummary{}) {
		t.Fatalf("agent board leaked the review queue: %+v", got)
	}
}

// harnessRequest is the commander's real credential path: the project mux
// reached with the derived agent token, which has no per-run identity. The auth
// gate marks the request (WithAgentSurface) instead, and every support-surface
// handler must treat it exactly like a participant identity.
func harnessRequest(method, path string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	return req.WithContext(WithAgentSurface(req.Context()))
}

func readRunIDs(t *testing.T, rec *httptest.ResponseRecorder) map[string]bool {
	t.Helper()
	var body struct {
		Runs []model.Run `json:"runs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode runs: %v (%s)", err, rec.Body.String())
	}
	seen := map[string]bool{}
	for _, run := range body.Runs {
		seen[run.ID] = true
	}
	return seen
}

// The commander is a harness session too: it talks to the project mux with the
// derived agent token, where identityOf is empty. Hiding support runs only for
// the participant identity would leave the commander able to list report and
// verification runs, read their events/context and see the review queue.
func TestCommanderProjectSurfaceHidesSupportRunsAndReviewQueue(t *testing.T) {
	s, _ := newBindingServer(t)
	s.SetReviewsPaused(true)
	in, err := s.Store.CreateIntent(model.Intent{Title: "对外门户评估"})
	if err != nil {
		t.Fatal(err)
	}
	directionRun, err := s.Store.AddRun(model.Run{IntentID: in.ID})
	if err != nil {
		t.Fatal(err)
	}
	verifyRun, err := s.Store.AddRun(model.Run{IntentID: in.ID, Purpose: "verifier"})
	if err != nil {
		t.Fatal(err)
	}
	reportRun, err := s.Store.AddRun(model.Run{Purpose: "report"})
	if err != nil {
		t.Fatal(err)
	}
	// Real journals exist: a 404 below must be the surface boundary, not a
	// missing-file accident.
	runsDir := filepath.Join(s.DataDir, "runs")
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{verifyRun.ID, reportRun.ID, directionRun.ID} {
		if err := os.WriteFile(filepath.Join(runsDir, id+".jsonl"), []byte("{\"kind\":\"message\"}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	harness := readRunIDs(t, serveMux(t, s, harnessRequest(http.MethodGet, "/api/runs")))
	if !harness[directionRun.ID] || harness[verifyRun.ID] || harness[reportRun.ID] {
		t.Fatalf("commander run list = %+v (direction %s, verifier %s, report %s)",
			harness, directionRun.ID, verifyRun.ID, reportRun.ID)
	}

	for _, surface := range []string{"/events", "/context"} {
		got := serveMux(t, s, harnessRequest(http.MethodGet, "/api/runs/"+verifyRun.ID+surface))
		if got.Code != http.StatusNotFound {
			t.Fatalf("commander %s of a verifier run = %d, want 404: %s", surface, got.Code, got.Body.String())
		}
		operator := serveMux(t, s, httptest.NewRequest(http.MethodGet, "/api/runs/"+verifyRun.ID+surface, nil))
		if operator.Code != http.StatusOK {
			t.Fatalf("operator %s of a verifier run = %d, want 200: %s", surface, operator.Code, operator.Body.String())
		}
	}

	var board struct {
		Reviews reviewSummary `json:"reviews"`
	}
	rec := serveMux(t, s, harnessRequest(http.MethodGet, "/api/board"))
	if rec.Code != http.StatusOK {
		t.Fatalf("commander board = %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &board); err != nil {
		t.Fatal(err)
	}
	if board.Reviews != (reviewSummary{}) {
		t.Fatalf("commander board leaked the review queue: %+v", board.Reviews)
	}
}

// A verifier run inherits the reviewed fact's intent id. The roster must report
// the direction's own run as its holder, never the operator-side verification
// run that finished last.
func TestParticipantRosterDoesNotExposeSupportRuns(t *testing.T) {
	s, _ := newBindingServer(t)
	in, err := s.Store.CreateIntent(model.Intent{Title: "方向"})
	if err != nil {
		t.Fatal(err)
	}
	directionRun, err := s.Store.AddRun(model.Run{IntentID: in.ID})
	if err != nil {
		t.Fatal(err)
	}
	verifyRun, err := s.Store.AddRun(model.Run{IntentID: in.ID, Purpose: "verifier"})
	if err != nil {
		t.Fatal(err)
	}

	rec := serveMux(t, s, harnessRequest(http.MethodGet, "/api/participants"))
	if rec.Code != http.StatusOK {
		t.Fatalf("participants = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Participants []struct {
			ID    string `json:"id"`
			RunID string `json:"run_id"`
		} `json:"participants"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range body.Participants {
		if p.ID != collab.ParticipantID(in.ID) {
			continue
		}
		found = true
		if p.RunID == verifyRun.ID {
			t.Fatalf("roster exposed the verifier run as the direction's run: %+v", p)
		}
		if p.RunID != directionRun.ID {
			t.Fatalf("roster run_id = %q, want the direction's run %q", p.RunID, directionRun.ID)
		}
	}
	if !found {
		t.Fatalf("direction %s missing from the roster: %+v", in.ID, body.Participants)
	}
}

// The manual "met" seal is the operator's: it stamps a goal by hand with no
// citation rule. A session must close its goal through complete, which enforces
// the ledger contract, so pin is refused on both harness credentials.
func TestGoalManualPinIsOperatorOnly(t *testing.T) {
	s, _ := newBindingServer(t)
	goal, err := s.Store.AddGoal(model.Goal{Statement: "拿到一条可复核证据"})
	if err != nil {
		t.Fatal(err)
	}
	evidence := addEvidenceFile(t, s.Store, s.CaseRoot, "in_1/proof.txt", "proof.txt", "text/plain", []byte("proof"))
	support, err := s.Store.AddFact(model.Fact{Kind: "vuln", Title: "支持目标", EvidenceIDs: []string{evidence.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.ReviewFact(model.Review{FactID: support.ID, Verdict: "pass", VerifierRun: "run-support"}); err != nil {
		t.Fatal(err)
	}
	pin := func(req *http.Request) *httptest.ResponseRecorder {
		rec := serveMux(t, s, req)
		return rec
	}
	operatorReq := httptest.NewRequest(http.MethodPost, "/api/goals/"+goal.ID+"/met",
		strings.NewReader(`{"evidence_id":"`+evidence.ID+`","note":"操作员手盖章"}`))
	if rec := pin(operatorReq); rec.Code != http.StatusOK {
		t.Fatalf("operator met = %d: %s", rec.Code, rec.Body.String())
	}

	// A fresh server for the refusal cases so the goal is still open.
	s2, _ := newBindingServer(t)
	goal2, err := s2.Store.AddGoal(model.Goal{Statement: "拿到一条可复核证据"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Store.AddEvidence(model.Evidence{URI: "proof.txt"}); err != nil {
		t.Fatal(err)
	}
	for name, req := range map[string]*http.Request{
		"commander":   harnessRequest(http.MethodPost, "/api/goals/"+goal2.ID+"/met"),
		"participant": agentRequest(s2, http.MethodPost, "/api/goals/"+goal2.ID+"/met"),
	} {
		rec := serveMux(t, s2, req)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "goal_operator_only") {
			t.Fatalf("%s met = %d, want 403 goal_operator_only: %s", name, rec.Code, rec.Body.String())
		}
	}
	for _, g := range s2.Store.Goals() {
		if g.ID == goal2.ID && g.Met {
			t.Fatalf("a session stamped the operator's goal: %+v", g)
		}
	}
}

// The participant tail endpoint is on the /agent/ mux and serves any run in the
// project: a support run id obtained from reviews/readiness must still 404.
func TestParticipantTailHidesSupportRuns(t *testing.T) {
	s, _ := newBindingServer(t)
	in, err := s.Store.CreateIntent(model.Intent{Title: "方向"})
	if err != nil {
		t.Fatal(err)
	}
	verifyRun, err := s.Store.AddRun(model.Run{IntentID: in.ID, Purpose: "verifier"})
	if err != nil {
		t.Fatal(err)
	}
	directionRun, err := s.Store.AddRun(model.Run{IntentID: in.ID})
	if err != nil {
		t.Fatal(err)
	}
	runsDir := filepath.Join(s.DataDir, "runs")
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{verifyRun.ID, directionRun.ID} {
		if err := os.WriteFile(filepath.Join(runsDir, id+".jsonl"), []byte("{\"kind\":\"message\"}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	tail := func(runID string) int {
		req := agentRequest(s, http.MethodGet, "/api/runs/"+runID+"/events/tail")
		rec := httptest.NewRecorder()
		s.AgentHandler().ServeHTTP(rec, req)
		return rec.Code
	}
	if code := tail(verifyRun.ID); code != http.StatusNotFound {
		t.Fatalf("participant tail of a verifier run = %d, want 404", code)
	}
	if code := tail(directionRun.ID); code != http.StatusOK {
		t.Fatalf("participant tail of a direction run = %d, want 200", code)
	}
	// The output reader shares the boundary (a guessed support run id must not
	// turn into tool output).
	req := agentRequest(s, http.MethodGet, "/api/runs/"+verifyRun.ID+"/output?call_id=call_1")
	rec := httptest.NewRecorder()
	s.AgentHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("participant output of a verifier run = %d, want 404", rec.Code)
	}
}

// Cancel goes through the same support-purpose gate on both surfaces.
func TestParticipantCannotCancelASupportRun(t *testing.T) {
	s, _ := newBindingServer(t)
	verifyRun, err := s.Store.AddRun(model.Run{Purpose: "verifier"})
	if err != nil {
		t.Fatal(err)
	}
	if rec := serveMux(t, s, agentRequest(s, http.MethodPost, "/api/runs/"+verifyRun.ID+"/cancel")); rec.Code != http.StatusNotFound {
		t.Fatalf("participant cancel of a verifier run = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	if rec := serveMux(t, s, harnessRequest(http.MethodPost, "/api/runs/"+verifyRun.ID+"/cancel")); rec.Code != http.StatusNotFound {
		t.Fatalf("commander cancel of a verifier run = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}
