package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/model"
)

// newSpawnFixture is an agent fixture that can actually dispatch: a local node
// is bound and the executor returns immediately unless a test swaps in the
// blocking one.
func newSpawnFixture(t *testing.T) *agentFixture {
	t.Helper()
	f := newAgentFixture(t)
	if err := f.p.SetNodeBinding([]string{"local"}, "local"); err != nil {
		t.Fatal(err)
	}
	f.srv.LocalNode = "local"
	f.srv.Exec = &recordExec{}
	return f
}

func spawnBody(key string, extra map[string]any) map[string]any {
	body := map[string]any{
		"title": "独立核对", "description": "只读核对，不要改数据",
		"idempotency_key": key,
	}
	for k, v := range extra {
		body[k] = v
	}
	return body
}

type spawnResp struct {
	Created bool         `json:"created"`
	Started bool         `json:"started"`
	Reason  string       `json:"reason"`
	Intent  model.Intent `json:"intent"`
	RunID   string       `json:"run_id"`
	Worker  workerView   `json:"worker"`
}

// The spawn primitive is one call: the ledger gets a worker intent, the run
// starts when there is capacity, and a retry with the same key finds that worker
// instead of creating a second one.
func TestAgentSpawnCreatesWorkerAndStartsRun(t *testing.T) {
	f := newSpawnFixture(t)
	rec := f.call(t, f.srcID, http.MethodPost, "/api/spawn", spawnBody("spawn-1", nil))
	if rec.Code != http.StatusCreated {
		t.Fatalf("spawn: %d %s", rec.Code, rec.Body.String())
	}
	var resp spawnResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Created || !resp.Started || resp.RunID == "" || resp.Intent.ID == "" {
		t.Fatalf("resp = %+v", resp)
	}
	if resp.Intent.CreatedBy != f.srcID.ParticipantID {
		t.Fatalf("created_by = %q", resp.Intent.CreatedBy)
	}
	if resp.Worker.IntentID != resp.Intent.ID || resp.Worker.ParticipantID != "pt_"+resp.Intent.ID {
		t.Fatalf("worker view = %+v", resp.Worker)
	}
	waitRun(t, f.p, resp.RunID)
	if run, ok := f.p.Store.GetRun(resp.RunID); !ok || run.Status != model.RunDone {
		t.Fatalf("run = %+v ok=%v", run, ok)
	}

	// A retry with the same key is not a second individual.
	rec = f.call(t, f.srcID, http.MethodPost, "/api/spawn", spawnBody("spawn-1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("retry: %d %s", rec.Code, rec.Body.String())
	}
	var again spawnResp
	if err := json.Unmarshal(rec.Body.Bytes(), &again); err != nil {
		t.Fatal(err)
	}
	if again.Created || again.Intent.ID != resp.Intent.ID {
		t.Fatalf("retry resp = %+v", again)
	}
	if n := len(f.p.Store.ListIntents()); n != 3 {
		t.Fatalf("intents = %d, want 3 (fixture 2 + one spawn)", n)
	}
}

func TestAgentSpawnRequiresIdempotencyKey(t *testing.T) {
	f := newSpawnFixture(t)
	rec := f.call(t, f.srcID, http.MethodPost, "/api/spawn", map[string]any{"title": "没有键"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "idempotency_key") {
		t.Fatalf("missing key: %d %s", rec.Code, rec.Body.String())
	}
}

// Capacity is reported, not hidden: the worker exists, started=false explains
// why, and the same key retried later starts that worker.
func TestAgentSpawnWhenCapacityIsFull(t *testing.T) {
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

	rec := f.call(t, f.srcID, http.MethodPost, "/api/spawn", spawnBody("spawn-full", nil))
	if rec.Code != http.StatusCreated {
		t.Fatalf("spawn full: %d %s", rec.Code, rec.Body.String())
	}
	var resp spawnResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Started || resp.RunID != "" || !strings.Contains(resp.Reason, "并发已满") {
		t.Fatalf("resp = %+v", resp)
	}
	got, _ := f.p.Store.GetIntent(resp.Intent.ID)
	if got.Status != model.StatusPending {
		t.Fatalf("queued worker status = %q", got.Status)
	}

	// Retry the same key: still one worker, still not started (capacity held).
	rec = f.call(t, f.srcID, http.MethodPost, "/api/spawn", spawnBody("spawn-full", nil))
	var again spawnResp
	if err := json.Unmarshal(rec.Body.Bytes(), &again); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || again.Created || again.Intent.ID != resp.Intent.ID || again.Started {
		t.Fatalf("retry = %d %+v", rec.Code, again)
	}
	if n := len(f.p.Store.ListIntents()); n != 4 {
		t.Fatalf("intents = %d, want 4 (no duplicate worker)", n)
	}
}

func TestAgentSpawnParentMustBeOwnRunningIntent(t *testing.T) {
	f := newSpawnFixture(t)
	rec := f.call(t, f.srcID, http.MethodPost, "/api/spawn",
		spawnBody("spawn-parent", map[string]any{"parent_intent_id": f.target.ID}))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "自己的意图") {
		t.Fatalf("foreign parent: %d %s", rec.Code, rec.Body.String())
	}
	// Own intent, but not the live turn: spawn is only valid mid-run.
	rec = f.call(t, f.srcID, http.MethodPost, "/api/spawn",
		spawnBody("spawn-parent2", map[string]any{"parent_intent_id": f.src.ID}))
	if rec.Code != http.StatusConflict {
		t.Fatalf("idle parent: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAgentSpawnResolvesTierAndProfile(t *testing.T) {
	f := newSpawnFixture(t)
	f.srv.Profiles = func() ([]ProfileInfo, string, error) {
		return []ProfileInfo{{ID: "cheap", Model: "cheap-model", Revision: "r1", Runnable: true, Tier: "worker"}}, "cheap", nil
	}
	f.srv.ProjectProfiles = &ProfileDefaults{Worker: "cheap", Strong: "cheap"}
	rec := f.call(t, f.srcID, http.MethodPost, "/api/spawn",
		spawnBody("spawn-tier", map[string]any{"tier": "cheap"}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("spawn tier: %d %s", rec.Code, rec.Body.String())
	}
	var resp spawnResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Intent.Profile != "cheap" {
		t.Fatalf("profile = %q", resp.Intent.Profile)
	}
	if resp.Worker.Tier != "worker" {
		t.Fatalf("tier view = %q", resp.Worker.Tier)
	}
	// A tier with no configured project profile is an error, not a silent
	// fallback to a different model.
	f.srv.ProjectProfiles = nil
	rec = f.call(t, f.srcID, http.MethodPost, "/api/spawn",
		spawnBody("spawn-tier-missing", map[string]any{"tier": "strong"}))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "strong") {
		t.Fatalf("missing tier: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAgentWorkersListShowsRunStateAndCanKill(t *testing.T) {
	f := newSpawnFixture(t)
	rec := f.call(t, f.srcID, http.MethodPost, "/api/spawn", spawnBody("spawn-list", nil))
	var spawned spawnResp
	if err := json.Unmarshal(rec.Body.Bytes(), &spawned); err != nil {
		t.Fatal(err)
	}
	waitRun(t, f.p, spawned.RunID)
	// A direction created by someone else in the same project is visible but not
	// killable by this run.
	if _, err := f.p.Store.UpdateIntent(f.target.ID, func(i *model.Intent) {
		i.CreatedBy = "pt_in_someone_else"
	}); err != nil {
		t.Fatal(err)
	}
	// A still-active worker this run created is killable.
	owned, err := f.p.Store.CreateIntent(model.Intent{Title: "待跑的自己的 worker", CreatedBy: f.srcID.ParticipantID})
	if err != nil {
		t.Fatal(err)
	}

	rec = f.call(t, f.srcID, http.MethodGet, "/api/workers", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("workers: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Workers []workerView `json:"workers"`
		Count   int          `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Count != len(resp.Workers) || resp.Count < 3 {
		t.Fatalf("count=%d workers=%d", resp.Count, len(resp.Workers))
	}
	byID := map[string]workerView{}
	for _, w := range resp.Workers {
		byID[w.IntentID] = w
	}
	mine, ok := byID[spawned.Intent.ID]
	if !ok || mine.CanKill || mine.Model == "" {
		// The spawned run already finished: can_kill is false because kill would
		// answer 409, not because the caller lost ownership.
		t.Fatalf("finished worker view = %+v ok=%v", mine, ok)
	}
	active, ok := byID[owned.ID]
	if !ok || !active.CanKill {
		t.Fatalf("active owned worker view = %+v ok=%v", active, ok)
	}
	other, ok := byID[f.target.ID]
	if !ok || other.CanKill {
		t.Fatalf("foreign worker view = %+v ok=%v", other, ok)
	}
}

func TestAgentKillWorkerCancelsAndHolds(t *testing.T) {
	f := newSpawnFixture(t)
	blocking := &blockingExec{}
	f.srv.Exec = blocking
	rec := f.call(t, f.srcID, http.MethodPost, "/api/spawn", spawnBody("spawn-kill", nil))
	if rec.Code != http.StatusCreated {
		t.Fatalf("spawn: %d %s", rec.Code, rec.Body.String())
	}
	var spawned spawnResp
	if err := json.Unmarshal(rec.Body.Bytes(), &spawned); err != nil {
		t.Fatal(err)
	}
	if !spawned.Started {
		t.Fatalf("spawn did not start: %+v", spawned)
	}
	waitFor(t, 5*time.Second, func() bool {
		got, ok := f.p.Store.GetIntent(spawned.Intent.ID)
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

	rec = f.call(t, f.srcID, http.MethodPost, "/api/workers/"+spawned.Intent.ID+"/kill", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("kill: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"cancel_cause":"worker_kill"`) {
		t.Fatalf("kill body = %s", rec.Body.String())
	}
	waitRun(t, f.p, spawned.RunID)
	run, _ := f.p.Store.GetRun(spawned.RunID)
	if run.Status != model.RunCanceled || run.CancelCause != model.CancelCauseWorkerKill {
		t.Fatalf("run = %s/%s", run.Status, run.CancelCause)
	}
	got, _ := f.p.Store.GetIntent(spawned.Intent.ID)
	if got.Status != model.StatusFailed || !got.Hold {
		t.Fatalf("killed worker = %s hold=%v", got.Status, got.Hold)
	}
	// The scheduler must not revive a killed worker.
	f.srv.pump()
	after, _ := f.p.Store.GetIntent(spawned.Intent.ID)
	if after.Status != model.StatusFailed || !after.Hold {
		t.Fatalf("pump revived the killed worker: %s hold=%v", after.Status, after.Hold)
	}
}

func TestAgentKillWorkerAuthorization(t *testing.T) {
	f := newAgentFixture(t)
	other, err := f.p.Store.CreateIntent(model.Intent{Title: "别人建的", CreatedBy: "pt_in_someone_else"})
	if err != nil {
		t.Fatal(err)
	}
	rec := f.call(t, f.srcID, http.MethodPost, "/api/workers/"+other.ID+"/kill", nil)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "worker_forbidden") {
		t.Fatalf("foreign kill: %d %s", rec.Code, rec.Body.String())
	}
	// A descendant of the caller's direction is killable even when its
	// CreatedBy names another participant: ownership follows the subtree.
	child, _, err := f.p.Store.CreateChildIntent(f.src.ID,
		model.Intent{Title: "子方向", CreatedBy: "pt_in_someone_else"})
	if err != nil {
		t.Fatal(err)
	}
	grandchild, _, err := f.p.Store.CreateChildIntent(child.ID,
		model.Intent{Title: "孙方向", CreatedBy: "pt_in_third"})
	if err != nil {
		t.Fatal(err)
	}
	rec = f.call(t, f.srcID, http.MethodPost, "/api/workers/"+grandchild.ID+"/kill", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"killed":true`) {
		t.Fatalf("descendant kill: %d %s", rec.Code, rec.Body.String())
	}
	got, _ := f.p.Store.GetIntent(grandchild.ID)
	if got.Status != model.StatusFailed || !got.Hold {
		t.Fatalf("descendant = %s hold=%v", got.Status, got.Hold)
	}
}
