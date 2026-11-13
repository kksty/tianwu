package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/access"
	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/model"
)

func accessHandoffCall(s *Server, id *collab.Identity, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	mux := http.NewServeMux()
	if id != nil {
		req = req.WithContext(WithIdentity(req.Context(), *id))
		s.registerAgent(mux)
	} else {
		s.Register(mux)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestWorkerCanHandOffActualBrowserWithoutSelectingHost(t *testing.T) {
	s, _, worker, _ := leaseFixture(t)
	_, _ = s.Store.UpdateRun(worker.RunID, func(run *model.Run) { run.Profile = "worker-host" })
	body := `{"request":"请完成扫码，窗口使用账号 A","browser":{"origin":"https://example.test/login","session":"worker_login_A","profile":"wrong-host"}}`
	rec := accessHandoffCall(s, &worker, "POST", "/api/intents/"+worker.IntentID+"/operator", body)
	if rec.Code != 202 {
		t.Fatalf("handoff: %d %s", rec.Code, rec.Body.String())
	}
	in, _ := s.Store.GetIntent(worker.IntentID)
	ids := in.Wait.Operator.AccessIDs
	if len(ids) != 1 {
		t.Fatalf("no associated browser: %+v", in.Wait)
	}
	entry, err := access.Get(s.CaseRoot, ids[0])
	if err != nil || entry.Profile != "worker-host" || entry.Session != "worker_login_A" || entry.IntentID != worker.IntentID {
		t.Fatalf("browser = %+v, %v", entry, err)
	}
	raw, _ := access.Material(s.CaseRoot, entry.ID)
	if !strings.Contains(string(raw), `"existing_session":true`) {
		t.Fatalf("lost worker page: %s", raw)
	}
	// A replay cannot add another browser or change the pending request.
	rec = accessHandoffCall(s, &worker, "POST", "/api/intents/"+worker.IntentID+"/operator", body)
	if rec.Code != 409 {
		t.Fatalf("duplicate request: %d %s", rec.Code, rec.Body.String())
	}
	rows, _ := access.List(s.CaseRoot)
	if len(rows) != 2 {
		t.Fatalf("replay created another browser: %+v", rows)
	}
}

func TestConsolePreparesBrowserForExactRequestAndReturnsItBeforeResume(t *testing.T) {
	s, _, worker, _ := leaseFixture(t)
	_, _ = s.Store.UpdateRun(worker.RunID, func(run *model.Run) { run.Profile = "worker-host" })
	wait, err := s.Store.RegisterOperatorWait(worker.IntentID, worker.RunID, model.OperatorWait{Request: "请准备登录窗口"})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/access"
	body := `{"kind":"browser","origin":"https://example.test/login","intent_id":"` + worker.IntentID + `","request_id":"` + wait.ID + `","profile":"wrong-host"}`
	rec := accessHandoffCall(s, nil, "POST", path, body)
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var entry access.Entry
	if err := json.Unmarshal(rec.Body.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	if entry.Profile != "worker-host" || entry.Name != "example.test" {
		t.Fatalf("wrong automatic binding: %+v", entry)
	}
	in, _ := s.Store.GetIntent(worker.IntentID)
	if len(in.Wait.Operator.AccessIDs) != 1 || in.Wait.Operator.AccessIDs[0] != entry.ID {
		t.Fatal("prepared browser missing from durable request")
	}
	lease := leaseID(t, leaseCall(t, s, entry, nil, map[string]any{"action": "acquire"}))
	complete := func() *httptest.ResponseRecorder {
		return accessHandoffCall(s, nil, "POST", "/api/intents/"+worker.IntentID+"/operator/complete", `{"request_id":"`+wait.ID+`"}`)
	}
	if rec := complete(); rec.Code != 409 {
		t.Fatalf("resumed occupied browser: %d %s", rec.Code, rec.Body.String())
	}
	if rec := leaseCall(t, s, entry, nil, map[string]any{"action": "release", "lease_id": lease}); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if rec := complete(); rec.Code != 202 {
		t.Fatalf("complete: %d %s", rec.Code, rec.Body.String())
	}
	// A stale form cannot create an orphan browser after completion.
	if rec := accessHandoffCall(s, nil, "POST", path, body); rec.Code != 409 {
		t.Fatalf("stale form: %d %s", rec.Code, rec.Body.String())
	}
}

func TestInlineBrowserIsRemovedWhenHandoffCannotBeRegistered(t *testing.T) {
	s, _, worker, _ := leaseFixture(t)
	_, _ = s.Store.UpdateIntent(worker.IntentID, func(in *model.Intent) { in.Status = model.StatusDone })
	rec := accessHandoffCall(s, &worker, "POST", "/api/intents/"+worker.IntentID+"/operator", `{"request":"扫码","browser":{"origin":"https://example.test","session":"orphan"}}`)
	if rec.Code != 409 {
		t.Fatalf("invalid run: %d %s", rec.Code, rec.Body.String())
	}
	rows, _ := access.List(s.CaseRoot)
	if len(rows) != 1 {
		t.Fatalf("failed handoff left orphan: %+v", rows)
	}
}
