package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/access"
	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/model"
)

// resourceIntent creates one claimed direction (intent + running run) and the
// participant identity a running executor would carry.
func resourceIntent(t *testing.T, s *Server, title string) (model.Intent, model.Run, collab.Identity) {
	t.Helper()
	in, err := s.Store.CreateIntent(model.Intent{Title: title})
	if err != nil {
		t.Fatal(err)
	}
	claimed, run, err := s.Store.StartIntentRun(in.ID, "local", "", model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	return claimed, run, collab.Identity{
		IntentID:      claimed.ID,
		RunID:         run.ID,
		ParticipantID: collab.ParticipantID(claimed.ID),
		Generation:    claimed.Attempts,
	}
}

// resourceMux mounts the participant surface. The resource routes are wired by
// the Lead into registerAgent, so using the real mux also proves the routes are
// reachable there and do not collide with existing participant paths.
func resourceMux(s *Server) *http.ServeMux {
	mux := http.NewServeMux()
	s.registerAgent(mux)
	return mux
}

// projectMux mounts the operator/commander surface, which gets the same routes
// from Register(mux).
func projectMux(s *Server) *http.ServeMux {
	mux := http.NewServeMux()
	s.Register(mux)
	return mux
}

func resourceCall(t *testing.T, mux *http.ServeMux, method, path string, id *collab.Identity, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if id != nil {
		req = req.WithContext(WithIdentity(req.Context(), *id))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func decodeResourceList(t *testing.T, rec *httptest.ResponseRecorder) (int, []model.Resource) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("resource list = %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Resources []model.Resource `json:"resources"`
		Total     int              `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Total != len(body.Resources) {
		t.Fatalf("total %d != rows %d", body.Total, len(body.Resources))
	}
	return body.Total, body.Resources
}

func acquireBrowser(t *testing.T, s *Server, entry access.Entry, id collab.Identity) string {
	t.Helper()
	rec := resourceCall(t, resourceMux(s), http.MethodPost,
		"/api/access/"+entry.ID+"/lease", &id, `{"action":"acquire"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("acquire = %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Lease access.BrowserLease `json:"lease"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Lease.ID == "" {
		t.Fatal("acquire returned no lease id")
	}
	return body.Lease.ID
}

func TestResourceAPIProjectsTheLiveHolderAndScopesParticipants(t *testing.T) {
	s, _ := newBindingServer(t)
	inA, runA, idA := resourceIntent(t, s, "方向 A")
	inB, runB, idB := resourceIntent(t, s, "方向 B")
	entry, err := access.Create(s.CaseRoot, access.Input{
		Name: "共享现场", Kind: "browser", Origin: "https://example.test", IntentID: inA.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	leaseID := acquireBrowser(t, s, entry, idA)

	// The owner sees its own browser, held by the current run, fenced by lease id.
	rec := resourceCall(t, resourceMux(s), http.MethodGet, "/api/resources?kind=browser", &idA, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("owner list = %d %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache-control = %q, want no-store", got)
	}
	_, rows := decodeResourceList(t, rec)
	if len(rows) != 1 {
		t.Fatalf("owner rows = %+v", rows)
	}
	if rows[0].ID != "browser:"+entry.ID || rows[0].State != model.ResourceStateHeld ||
		rows[0].Version != leaseID || rows[0].HolderRunID != runA.ID || rows[0].Generation != idA.Generation {
		t.Fatalf("owner browser = %+v", rows[0])
	}

	// The other direction sees only its own intent's rows: the browser is not
	// there, and its own session is.
	_, rows = decodeResourceList(t, resourceCall(t, resourceMux(s), http.MethodGet, "/api/resources", &idB, ""))
	for _, row := range rows {
		if row.IntentID != inB.ID {
			t.Fatalf("participant B learned another direction: %+v", row)
		}
	}
	if len(rows) != 1 || rows[0].ID != "session:"+runB.ID {
		t.Fatalf("participant B rows = %+v", rows)
	}

	// Asking for another direction's intent is refused, not silently filtered.
	rec = resourceCall(t, resourceMux(s), http.MethodGet, "/api/resources?intent="+inA.ID, &idB, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-direction list = %d %s", rec.Code, rec.Body.String())
	}

	// A single resource of another direction is indistinguishable from missing.
	rec = resourceCall(t, resourceMux(s), http.MethodGet, "/api/resources/browser:"+entry.ID, &idB, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-direction get = %d %s", rec.Code, rec.Body.String())
	}
	rec = resourceCall(t, resourceMux(s), http.MethodGet, "/api/resources/session:"+runA.ID, &idB, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-direction session = %d %s", rec.Code, rec.Body.String())
	}

	// The owner can read the same projection by identity.
	rec = resourceCall(t, resourceMux(s), http.MethodGet, "/api/resources/browser:"+entry.ID, &idA, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("owner get = %d %s", rec.Code, rec.Body.String())
	}
	var one struct {
		Resource model.Resource `json:"resource"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &one); err != nil {
		t.Fatal(err)
	}
	if one.Resource.Version != leaseID || one.Resource.State != model.ResourceStateHeld {
		t.Fatalf("owner get body = %+v", one.Resource)
	}

	// Operator/commander (no participant identity) sees the project.
	total, _ := decodeResourceList(t, resourceCall(t, projectMux(s), http.MethodGet, "/api/resources", nil, ""))
	if total < 3 {
		t.Fatalf("project-wide rows = %d, want browser + two sessions", total)
	}
}

func TestResourceRoutesAreReachableThroughBothMuxesAndReadsWriteNothing(t *testing.T) {
	s, _ := newBindingServer(t)
	in, run, id := resourceIntent(t, s, "只读方向")
	entry, err := access.Create(s.CaseRoot, access.Input{
		Name: "共享现场", Kind: "browser", Origin: "https://example.test", IntentID: in.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	acquireBrowser(t, s, entry, id)
	leasePath := filepath.Join(s.CaseRoot, ".access", entry.ID, "lease.json")
	leaseBefore, err := os.ReadFile(leasePath)
	if err != nil {
		t.Fatal(err)
	}
	revisionBefore := s.Store.Revision()
	runsBefore := len(s.Store.Runs())
	actionsBefore := len(s.Store.Actions(in.ID))

	// Both surfaces expose the same read model: the participant mux (with an
	// identity) and the operator/commander project mux (without one).
	if rec := resourceCall(t, resourceMux(s), http.MethodGet, "/api/resources", &id, ""); rec.Code != http.StatusOK {
		t.Fatalf("participant mux list = %d %s", rec.Code, rec.Body.String())
	}
	if rec := resourceCall(t, projectMux(s), http.MethodGet, "/api/resources", nil, ""); rec.Code != http.StatusOK {
		t.Fatalf("project mux list = %d %s", rec.Code, rec.Body.String())
	}
	if rec := resourceCall(t, resourceMux(s), http.MethodGet, "/api/resources/browser:"+entry.ID, &id, ""); rec.Code != http.StatusOK {
		t.Fatalf("participant mux get = %d %s", rec.Code, rec.Body.String())
	}
	if rec := resourceCall(t, projectMux(s), http.MethodGet, "/api/resources/session:"+run.ID, nil, ""); rec.Code != http.StatusOK {
		t.Fatalf("project mux get = %d %s", rec.Code, rec.Body.String())
	}

	// A read-only projection cannot move the ledger revision, start a run, seed
	// an action or rewrite the lease file it reports.
	if got := s.Store.Revision(); got != revisionBefore {
		t.Fatalf("ledger revision moved on reads: %d -> %d", revisionBefore, got)
	}
	if got := len(s.Store.Runs()); got != runsBefore {
		t.Fatalf("reads created a run: %d -> %d", runsBefore, got)
	}
	if got := len(s.Store.Actions(in.ID)); got != actionsBefore {
		t.Fatalf("reads created an action: %d -> %d", actionsBefore, got)
	}
	leaseAfter, err := os.ReadFile(leasePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(leaseBefore, leaseAfter) {
		t.Fatal("reads rewrote the browser lease")
	}
}

func TestResourceAPIReportsRecoveryAndLeavesRecoveryToTheOperator(t *testing.T) {
	s, _ := newBindingServer(t)
	in, run, id := resourceIntent(t, s, "浏览器方向")
	entry, err := access.Create(s.CaseRoot, access.Input{
		Name: "共享现场", Kind: "browser", Origin: "https://example.test", IntentID: in.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	leaseID := acquireBrowser(t, s, entry, id)
	// A live participant still cannot recover: that authority is the operator's,
	// independent of the new resource projection.
	rec := resourceCall(t, resourceMux(s), http.MethodPost,
		"/api/access/"+entry.ID+"/lease", &id, `{"action":"recover","lease_id":"`+leaseID+`","confirmed_idle":true,"note":"x"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("participant recover = %d %s", rec.Code, rec.Body.String())
	}
	if _, err := s.Store.UpdateRun(run.ID, func(r *model.Run) {
		r.Status = model.RunCanceled
		r.CancelCause = model.CancelCauseOperator
	}); err != nil {
		t.Fatal(err)
	}

	// The ended holder projects recovery; reading must not release the lease.
	_, rows := decodeResourceList(t, resourceCall(t, resourceMux(s), http.MethodGet, "/api/resources?kind=browser", &id, ""))
	if len(rows) != 1 || rows[0].State != model.ResourceStateRecovery || rows[0].Version != leaseID {
		t.Fatalf("recovery projection = %+v", rows)
	}
	current, err := access.Get(s.CaseRoot, entry.ID)
	if err != nil || current.Lease == nil {
		t.Fatalf("projection released the lease: %+v %v", current.Lease, err)
	}

	// Participants still cannot recover, and an operator recover must carry the
	// existing gates: lease id, confirmed idle and a note.
	operatorMux := http.NewServeMux()
	s.Register(operatorMux)
	recoverCall := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/access/"+entry.ID+"/lease", strings.NewReader(body))
		rec := httptest.NewRecorder()
		operatorMux.ServeHTTP(rec, req)
		return rec
	}
	if rec := recoverCall(`{"action":"recover","lease_id":"` + leaseID + `"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("recover without confirmation = %d %s", rec.Code, rec.Body.String())
	}
	if rec := recoverCall(`{"action":"recover","lease_id":"bl_stale","confirmed_idle":true,"note":"已确认空闲"}`); rec.Code != http.StatusConflict {
		t.Fatalf("recover with stale lease = %d %s", rec.Code, rec.Body.String())
	}
	if rec := recoverCall(`{"action":"recover","lease_id":"` + leaseID + `","confirmed_idle":true,"note":"已确认空闲"}`); rec.Code != http.StatusOK {
		t.Fatalf("operator recover = %d %s", rec.Code, rec.Body.String())
	}
	_, rows = decodeResourceList(t, resourceCall(t, resourceMux(s), http.MethodGet, "/api/resources?kind=browser", &id, ""))
	if len(rows) != 1 || rows[0].State != model.ResourceStateFree || rows[0].Version != "" {
		t.Fatalf("after recovery = %+v", rows)
	}
}

func TestResourceAPIUnknownActionIsReadOnlyAndNeverReplayed(t *testing.T) {
	s, _ := newBindingServer(t)
	in, run, id := resourceIntent(t, s, "外部动作方向")
	action, created, err := s.Store.BeginAction(in.ID, run.ID, "deploy-key-1", "deploy", "target-a")
	if err != nil || !created {
		t.Fatalf("begin action: created=%v err=%v", created, err)
	}
	if _, err := s.Store.ResolveAction(action.ID, action.Version, model.ActionUnknown, "结果未确认", "", run.ID); err != nil {
		t.Fatal(err)
	}
	runsBefore := len(s.Store.Runs())

	_, rows := decodeResourceList(t, resourceCall(t, resourceMux(s), http.MethodGet, "/api/resources?kind=action", &id, ""))
	if len(rows) != 1 || rows[0].ID != "action:"+action.ID || rows[0].State != model.ResourceStateUnknown {
		t.Fatalf("unknown action projection = %+v", rows)
	}
	if rows[0].Version != "2" {
		t.Fatalf("fence version = %q, want the CAS version", rows[0].Version)
	}
	// Repeated reads create no retry and no duplicate action.
	for i := 0; i < 3; i++ {
		resourceCall(t, resourceMux(s), http.MethodGet, "/api/resources/action:"+action.ID, &id, "")
	}
	if got := len(s.Store.Actions(in.ID)); got != 1 {
		t.Fatalf("actions after reads = %d", got)
	}
	if got := len(s.Store.Runs()); got != runsBefore {
		t.Fatalf("reads started a run: %d -> %d", runsBefore, got)
	}
}

func TestResourceAPIProjectsOperatorWaitsOnlyForTheirDirection(t *testing.T) {
	s, _ := newBindingServer(t)
	in, run, id := resourceIntent(t, s, "人工等待方向")
	other, _, otherID := resourceIntent(t, s, "其他方向")
	request, err := s.Store.RegisterOperatorWait(in.ID, run.ID, model.OperatorWait{Request: "扫码登录", AccessIDs: []string{}})
	if err != nil {
		t.Fatal(err)
	}

	_, rows := decodeResourceList(t, resourceCall(t, resourceMux(s), http.MethodGet, "/api/resources?kind=wait", &id, ""))
	if len(rows) != 1 || rows[0].ID != "wait:"+request.ID || rows[0].State != model.ResourceStatePending {
		t.Fatalf("pending wait projection = %+v", rows)
	}
	rec := resourceCall(t, resourceMux(s), http.MethodGet, "/api/resources/wait:"+request.ID, &otherID, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("other direction read the wait: %d %s", rec.Code, rec.Body.String())
	}
	if err := s.Store.CompleteOperatorWait(in.ID, request.ID, "已完成"); err != nil {
		t.Fatal(err)
	}
	_, rows = decodeResourceList(t, resourceCall(t, resourceMux(s), http.MethodGet, "/api/resources?intent="+other.ID+"&kind=wait", &otherID, ""))
	if len(rows) != 0 {
		t.Fatalf("other direction saw a wait: %+v", rows)
	}
	_, rows = decodeResourceList(t, resourceCall(t, resourceMux(s), http.MethodGet, "/api/resources?kind=wait", &id, ""))
	if len(rows) != 1 || rows[0].State != model.ResourceStateCompleted || rows[0].Version == "" {
		t.Fatalf("completed wait projection = %+v", rows)
	}
}

func TestResourceAPIRejectsMalformedIDsAndKinds(t *testing.T) {
	s, _ := newBindingServer(t)
	_, _, id := resourceIntent(t, s, "校验方向")
	mux := resourceMux(s)
	if rec := resourceCall(t, mux, http.MethodGet, "/api/resources?kind=not-a-kind", &id, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad kind = %d %s", rec.Code, rec.Body.String())
	}
	for _, path := range []string{"/api/resources/nope:ac_1", "/api/resources/browser:", "/api/resources/browser:ac%20x"} {
		if rec := resourceCall(t, mux, http.MethodGet, path, &id, ""); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d %s", path, rec.Code, rec.Body.String())
		}
	}
	if rec := resourceCall(t, mux, http.MethodGet, "/api/resources/browser:ac_missing", &id, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("missing browser = %d %s", rec.Code, rec.Body.String())
	}
}
