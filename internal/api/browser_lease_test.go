package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kksty/tianwu/internal/access"
	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/model"
)

func leaseFixture(t *testing.T) (*Server, access.Entry, collab.Identity, collab.Identity) {
	t.Helper()
	s, _ := newBindingServer(t)
	entry, err := access.Create(s.CaseRoot, access.Input{Name: "共享现场", Kind: "browser", Origin: "https://example.test"})
	if err != nil {
		t.Fatal(err)
	}
	identities := make([]collab.Identity, 2)
	for i := range identities {
		in, err := s.Store.CreateIntent(model.Intent{Title: fmt.Sprintf("浏览器执行者 %d", i)})
		if err != nil {
			t.Fatal(err)
		}
		claimed, run, err := s.Store.StartIntentRun(in.ID, "local", "", model.Run{})
		if err != nil {
			t.Fatal(err)
		}
		identities[i] = collab.Identity{IntentID: in.ID, RunID: run.ID, ParticipantID: collab.ParticipantID(in.ID), Generation: claimed.Attempts}
	}
	return s, entry, identities[0], identities[1]
}

func leaseCall(t *testing.T, s *Server, entry access.Entry, id *collab.Identity, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/access/"+entry.ID+"/lease", strings.NewReader(string(raw)))
	if id != nil {
		req = req.WithContext(WithIdentity(req.Context(), *id))
	}
	mux := http.NewServeMux()
	if id != nil {
		s.registerAgent(mux)
	} else {
		s.Register(mux)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func leaseID(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var result struct {
		Lease access.BrowserLease `json:"lease"`
	}
	if rec.Code != 200 {
		t.Fatalf("lease response %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result.Lease.ID
}

func TestConcurrentBrowserClaimsHaveOneOwnerAndConsoleCannotInterrupt(t *testing.T) {
	s, entry, a, b := leaseFixture(t)
	var group sync.WaitGroup
	results := make([]*httptest.ResponseRecorder, 2)
	for i, id := range []collab.Identity{a, b} {
		group.Add(1)
		go func(i int, id collab.Identity) {
			defer group.Done()
			results[i] = leaseCall(t, s, entry, &id, map[string]any{"action": "acquire"})
		}(i, id)
	}
	group.Wait()
	wins := 0
	for _, rec := range results {
		if rec.Code == 200 {
			wins++
		} else if rec.Code != 409 {
			t.Fatalf("claim: %d %s", rec.Code, rec.Body.String())
		}
	}
	if wins != 1 {
		t.Fatalf("successful owners = %d", wins)
	}
	current, _ := access.Get(s.CaseRoot, entry.ID)
	mux := http.NewServeMux()
	s.Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/access/"+entry.ID+"/browser", strings.NewReader(`{"action":"inspect","lease_id":"`+current.Lease.ID+`"}`)))
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "先领取") {
		t.Fatalf("console interrupted worker: %d %s", rec.Code, rec.Body.String())
	}
}

func TestEndedRunKeepsBrowserReservedUntilExplicitOperatorRecovery(t *testing.T) {
	s, entry, a, b := leaseFixture(t)
	first := leaseID(t, leaseCall(t, s, entry, &a, map[string]any{"action": "acquire"}))
	_, err := s.Store.UpdateRun(a.RunID, func(run *model.Run) { run.Status = model.RunCanceled })
	if err != nil {
		t.Fatal(err)
	}
	reopened := New(s.Store, nil, s.DataDir, s.CaseRoot)
	if rec := leaseCall(t, reopened, entry, &b, map[string]any{"action": "acquire"}); rec.Code != 409 {
		t.Fatal("restart released the old holder")
	}
	current, _ := access.Get(s.CaseRoot, entry.ID)
	reopened.browserLeaseView(&current)
	if current.Lease.State != "recovery" {
		t.Fatalf("lease state = %s", current.Lease.State)
	}
	if rec := leaseCall(t, reopened, entry, &b, map[string]any{"action": "recover", "lease_id": first, "confirmed_idle": true, "note": "idle"}); rec.Code != 403 {
		t.Fatalf("participant recovered ownership: %d", rec.Code)
	}
	if rec := leaseCall(t, reopened, entry, nil, map[string]any{"action": "recover", "lease_id": first}); rec.Code != 400 {
		t.Fatal("recovery did not require an idle confirmation")
	}
	if rec := leaseCall(t, reopened, entry, nil, map[string]any{"action": "recover", "lease_id": first, "confirmed_idle": true, "note": "已检查原现场无在途操作"}); rec.Code != 200 {
		t.Fatalf("recovery: %d %s", rec.Code, rec.Body.String())
	}
	second := leaseID(t, leaseCall(t, reopened, entry, &b, map[string]any{"action": "acquire"}))
	if second == first {
		t.Fatal("new lease reused old identity")
	}
	if rec := leaseCall(t, reopened, entry, nil, map[string]any{"action": "release", "lease_id": first}); rec.Code != 409 {
		t.Fatal("stale operator returned newer lease")
	}
}

func TestPendingNativeCommandNeedsRecoveryAndKeepsTheOldControlMarker(t *testing.T) {
	s, entry, _, _ := leaseFixture(t)
	id := leaseID(t, leaseCall(t, s, entry, nil, map[string]any{"action": "acquire"}))
	dir := filepath.Join(s.CaseRoot, ".access", entry.ID)
	marker := filepath.Join(dir, "control-session.json")
	if err := os.WriteFile(marker, []byte(`{"session":"old-session","pending":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if rec := leaseCall(t, s, entry, nil, map[string]any{"action": "release", "lease_id": id}); rec.Code != 409 {
		t.Fatal("uncertain native command was released")
	}
	if rec := leaseCall(t, s, entry, nil, map[string]any{"action": "recover", "lease_id": id, "confirmed_idle": true, "note": "已检查原窗口"}); rec.Code != 200 {
		t.Fatalf("recovery: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("uncertain control session can still replay")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "control-recovery-*.json"))
	if len(files) != 1 {
		t.Fatal("old control marker was not preserved")
	}
}

func TestBrowserBindingRequiresCurrentLeaseAndMatchingHost(t *testing.T) {
	s, entry, a, b := leaseFixture(t)
	_, _ = s.Store.UpdateRun(a.RunID, func(run *model.Run) { run.Profile = "other-host" })
	if rec := leaseCall(t, s, entry, &a, map[string]any{"action": "acquire"}); rec.Code != 409 {
		t.Fatal("worker acquired a browser on another host")
	}
	id := leaseID(t, leaseCall(t, s, entry, &b, map[string]any{"action": "acquire"}))
	mux := http.NewServeMux()
	s.registerAgent(mux)
	for _, item := range []struct {
		id    collab.Identity
		lease string
		want  int
	}{{a, id, 409}, {b, "stale", 409}, {b, id, 200}} {
		req := httptest.NewRequest("GET", "/api/access/"+entry.ID+"/material?lease_id="+item.lease, nil)
		req = req.WithContext(WithIdentity(req.Context(), item.id))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != item.want {
			t.Fatalf("binding read: %d %s", rec.Code, rec.Body.String())
		}
	}
	work := filepath.Join(s.CaseRoot, "new-worker")
	if _, err := s.accessSection(work); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(work, ".tw", "access", entry.ID+".json")); !os.IsNotExist(err) {
		t.Fatal("browser binding copied without a lease")
	}
}

func TestBrowserLeaseRejectsMissingIdentityAndRecoveryOfLiveHolder(t *testing.T) {
	s, entry, a, _ := leaseFixture(t)
	mux := http.NewServeMux()
	s.registerAgent(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/access/"+entry.ID+"/lease", strings.NewReader(`{"action":"acquire"}`)))
	if rec.Code != 401 {
		t.Fatal("missing participant identity acquired an operator lease")
	}
	id := leaseID(t, leaseCall(t, s, entry, &a, map[string]any{"action": "acquire"}))
	if rec := leaseCall(t, s, entry, nil, map[string]any{"action": "recover", "lease_id": id, "confirmed_idle": true, "note": "idle"}); rec.Code != 409 {
		t.Fatal("operator recovered a still-running holder")
	}
}
