package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/model"
)

func consoleCall(t *testing.T, s *Server, method, path, actor string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	if actor != "" {
		req.Header.Set("X-Tw-Actor", actor)
	}
	mux := http.NewServeMux()
	s.Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// The operator spawns through the console token: the ledger records the actor
// (not an executor participant) and a retry finds the same worker.
func TestConsoleSpawnCreatesWorkerAsOperator(t *testing.T) {
	f := newSpawnFixture(t)
	rec := consoleCall(t, f.srv, http.MethodPost, "/api/spawn", "", spawnBody("console-1", nil))
	if rec.Code != http.StatusCreated {
		t.Fatalf("spawn: %d %s", rec.Code, rec.Body.String())
	}
	var resp spawnResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Created || !resp.Started || resp.Intent.CreatedBy != collab.OperatorID {
		t.Fatalf("resp = %+v", resp)
	}
	if !strings.Contains(rec.Body.String(), `"worker_id":"`+resp.Intent.ID+`"`) {
		t.Fatalf("response lost the flat worker aliases: %s", rec.Body.String())
	}
	waitRun(t, f.p, resp.RunID)

	rec = consoleCall(t, f.srv, http.MethodPost, "/api/spawn", "", spawnBody("console-1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("retry: %d %s", rec.Code, rec.Body.String())
	}
	var again spawnResp
	if err := json.Unmarshal(rec.Body.Bytes(), &again); err != nil {
		t.Fatal(err)
	}
	if again.Created || again.Intent.ID != resp.Intent.ID {
		t.Fatalf("retry = %+v", again)
	}
	if n := len(f.p.Store.ListIntents()); n != 3 {
		t.Fatalf("intents = %d, want 3", n)
	}
}

// The commander drives the same console API with X-Tw-Actor; the ledger should
// say commander, so operator- and commander-created workers stay distinguishable.
func TestConsoleSpawnRecordsCommanderActor(t *testing.T) {
	f := newSpawnFixture(t)
	rec := consoleCall(t, f.srv, http.MethodPost, "/api/spawn", collab.CommanderID,
		spawnBody("console-cmd", nil))
	if rec.Code != http.StatusCreated {
		t.Fatalf("spawn: %d %s", rec.Code, rec.Body.String())
	}
	var resp spawnResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Intent.CreatedBy != collab.CommanderID {
		t.Fatalf("created_by = %q, want %q", resp.Intent.CreatedBy, collab.CommanderID)
	}
}

// The console resolves profile/tier exactly like the participant route.
func TestConsoleSpawnResolvesTier(t *testing.T) {
	f := newSpawnFixture(t)
	f.srv.Profiles = func() ([]ProfileInfo, string, error) {
		return []ProfileInfo{{ID: "cheap", Model: "cheap-model", Revision: "r1", Runnable: true, Tier: "worker"}}, "cheap", nil
	}
	f.srv.ProjectProfiles = &ProfileDefaults{Worker: "cheap", Strong: "cheap"}
	rec := consoleCall(t, f.srv, http.MethodPost, "/api/spawn", "",
		spawnBody("console-tier", map[string]any{"tier": "worker"}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("spawn: %d %s", rec.Code, rec.Body.String())
	}
	var resp spawnResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Intent.Profile != "cheap" || resp.Worker.Tier != "worker" {
		t.Fatalf("profile/tier = %q/%q", resp.Intent.Profile, resp.Worker.Tier)
	}
}

// Capacity is reported the same way as on the agent route: the worker exists,
// started=false explains why, and a retry never duplicates it.
func TestConsoleSpawnWhenCapacityIsFull(t *testing.T) {
	f := newSpawnFixture(t)
	blocking := &blockingExec{}
	f.srv.Exec = blocking
	f.srv.MaxRuns = 1
	filler, err := f.p.Store.CreateIntent(model.Intent{Title: "占住唯一的槽"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.srv.StartRun(filler.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		got, ok := f.p.Store.GetIntent(filler.ID)
		return ok && got.Status == model.StatusRunning
	})
	t.Cleanup(func() {
		f.srv.CancelAll()
		f.srv.WaitIdle(5 * time.Second)
		time.Sleep(150 * time.Millisecond)
	})

	rec := consoleCall(t, f.srv, http.MethodPost, "/api/spawn", "", spawnBody("console-full", nil))
	if rec.Code != http.StatusCreated {
		t.Fatalf("spawn full: %d %s", rec.Code, rec.Body.String())
	}
	var resp spawnResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Started || !strings.Contains(resp.Reason, "并发已满") {
		t.Fatalf("resp = %+v", resp)
	}
	rec = consoleCall(t, f.srv, http.MethodPost, "/api/spawn", "", spawnBody("console-full", nil))
	var again spawnResp
	if err := json.Unmarshal(rec.Body.Bytes(), &again); err != nil {
		t.Fatal(err)
	}
	if again.Created || again.Intent.ID != resp.Intent.ID {
		t.Fatalf("retry duplicated the worker: %+v", again)
	}
}

// The console roster sees the whole project and may kill any still-active
// worker; a finished worker is not killable (kill answers 409).
func TestConsoleWorkersAndKill(t *testing.T) {
	f := newSpawnFixture(t)
	blocking := &blockingExec{}
	f.srv.Exec = blocking
	// A worker created by an executor (so ownership would not allow the operator
	// if the operator were subject to the participant rule).
	owned, err := f.p.Store.CreateIntent(model.Intent{
		Title: "执行者创建的 worker", CreatedBy: "pt_in_someone_else",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.srv.StartRun(owned.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		got, ok := f.p.Store.GetIntent(owned.ID)
		if !ok || got.Status != model.StatusRunning {
			return false
		}
		return f.srv.Lifecycle.RunActive(got.RunID)
	})
	t.Cleanup(func() {
		f.srv.CancelAll()
		f.srv.WaitIdle(5 * time.Second)
		time.Sleep(150 * time.Millisecond)
	})
	finished, err := f.p.Store.CreateIntent(model.Intent{Title: "已结束的 worker"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Store.UpdateIntent(finished.ID, func(i *model.Intent) { i.Status = model.StatusDone }); err != nil {
		t.Fatal(err)
	}

	rec := consoleCall(t, f.srv, http.MethodGet, "/api/workers", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("workers: %d %s", rec.Code, rec.Body.String())
	}
	var roster struct {
		Workers []workerView `json:"workers"`
		Count   int          `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &roster); err != nil {
		t.Fatal(err)
	}
	if roster.Count != len(roster.Workers) || roster.Count < 4 {
		t.Fatalf("count=%d workers=%d", roster.Count, len(roster.Workers))
	}
	byID := map[string]workerView{}
	for _, w := range roster.Workers {
		byID[w.IntentID] = w
	}
	if !byID[owned.ID].CanKill {
		t.Fatalf("active worker should be killable by operator: %+v", byID[owned.ID])
	}
	if byID[finished.ID].CanKill {
		t.Fatalf("finished worker should not be killable: %+v", byID[finished.ID])
	}

	rec = consoleCall(t, f.srv, http.MethodPost, "/api/workers/"+owned.ID+"/kill", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"cancel_cause":"worker_kill"`) {
		t.Fatalf("kill: %d %s", rec.Code, rec.Body.String())
	}
	waitRun(t, f.p, byID[owned.ID].RunID)
	run, _ := f.p.Store.GetRun(byID[owned.ID].RunID)
	if run.Status != model.RunCanceled || run.CancelCause != model.CancelCauseWorkerKill {
		t.Fatalf("run = %s/%s", run.Status, run.CancelCause)
	}
	got, _ := f.p.Store.GetIntent(owned.ID)
	if got.Status != model.StatusFailed || !got.Hold {
		t.Fatalf("killed worker = %s hold=%v", got.Status, got.Hold)
	}

	// A finished worker is a conflict, not a silent kill.
	rec = consoleCall(t, f.srv, http.MethodPost, "/api/workers/"+finished.ID+"/kill", "", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("terminal kill: %d %s", rec.Code, rec.Body.String())
	}
	if rec := consoleCall(t, f.srv, http.MethodPost, "/api/workers/in_missing/kill", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("missing worker: %d %s", rec.Code, rec.Body.String())
	}
}

// The operator may hang a spawned worker under any still-active direction; an
// executor-created parent is fine (unlike the participant route).
func TestConsoleSpawnUnderAnyActiveParent(t *testing.T) {
	f := newSpawnFixture(t)
	parent, err := f.p.Store.CreateIntent(model.Intent{Title: "指挥官的父方向", CreatedBy: collab.CommanderID})
	if err != nil {
		t.Fatal(err)
	}
	rec := consoleCall(t, f.srv, http.MethodPost, "/api/spawn", "",
		spawnBody("console-parent", map[string]any{"parent_intent_id": parent.ID}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("spawn under parent: %d %s", rec.Code, rec.Body.String())
	}
	var resp spawnResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Intent.ParentID != parent.ID {
		t.Fatalf("parent = %q", resp.Intent.ParentID)
	}
	// A finished parent is refused.
	if _, err := f.p.Store.UpdateIntent(parent.ID, func(i *model.Intent) { i.Status = model.StatusDone }); err != nil {
		t.Fatal(err)
	}
	rec = consoleCall(t, f.srv, http.MethodPost, "/api/spawn", "",
		spawnBody("console-parent2", map[string]any{"parent_intent_id": parent.ID}))
	if rec.Code != http.StatusConflict {
		t.Fatalf("finished parent: %d %s", rec.Code, rec.Body.String())
	}
}
