package resource

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/access"
	"github.com/kksty/tianwu/internal/model"
)

// fakeLedger is the read side of a project ledger. The projection is not
// allowed to write through it, so a fake is enough and keeps these tests free
// of a database.
type fakeLedger struct {
	runs    map[string]model.Run
	runIDs  []string
	intents map[string]model.Intent
	ids     []string
	actions map[string]model.ExternalAction
	actIDs  []string
}

func newFakeLedger() *fakeLedger {
	return &fakeLedger{
		runs:    map[string]model.Run{},
		intents: map[string]model.Intent{},
		actions: map[string]model.ExternalAction{},
	}
}

func (f *fakeLedger) addRun(run model.Run) {
	f.runs[run.ID] = run
	f.runIDs = append(f.runIDs, run.ID)
}

func (f *fakeLedger) addIntent(in model.Intent) {
	f.intents[in.ID] = in
	f.ids = append(f.ids, in.ID)
}

func (f *fakeLedger) addAction(action model.ExternalAction) {
	f.actions[action.ID] = action
	f.actIDs = append(f.actIDs, action.ID)
}

func (f *fakeLedger) Runs() []model.Run {
	out := make([]model.Run, 0, len(f.runIDs))
	for _, id := range f.runIDs {
		out = append(out, f.runs[id])
	}
	return out
}

func (f *fakeLedger) GetRun(id string) (model.Run, bool) {
	run, ok := f.runs[id]
	return run, ok
}

func (f *fakeLedger) ListIntents() []model.Intent {
	out := make([]model.Intent, 0, len(f.ids))
	for _, id := range f.ids {
		out = append(out, f.intents[id])
	}
	return out
}

func (f *fakeLedger) GetIntent(id string) (model.Intent, bool) {
	in, ok := f.intents[id]
	return in, ok
}

func (f *fakeLedger) Actions(intentID string) []model.ExternalAction {
	out := []model.ExternalAction{}
	for _, id := range f.actIDs {
		if action := f.actions[id]; intentID == "" || action.IntentID == intentID {
			out = append(out, action)
		}
	}
	return out
}

func (f *fakeLedger) GetAction(id string) (model.ExternalAction, bool) {
	action, ok := f.actions[id]
	return action, ok
}

// fakeRuntime is the server's non-persisted browser view: a native command in
// flight or an unconfirmed control marker.
type fakeRuntime struct {
	active  map[string]bool
	pending map[string]bool
	err     map[string]error
}

func (f *fakeRuntime) BrowserCommandActive(accessID string) bool { return f.active[accessID] }

func (f *fakeRuntime) BrowserControlPending(accessID string) (bool, error) {
	return f.pending[accessID], f.err[accessID]
}

// accessRoot creates a temp project case root with a fake `.access/` tree.
func accessRoot(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

func newBrowser(t *testing.T, cases, intentID string) access.Entry {
	t.Helper()
	entry, err := access.Create(cases, access.Input{
		Name:     "共享现场",
		Kind:     "browser",
		Origin:   "https://example.test",
		IntentID: intentID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

func mustAcquire(t *testing.T, cases, accessID string, owner access.BrowserLease) access.BrowserLease {
	t.Helper()
	lease, err := access.AcquireBrowser(cases, accessID, owner)
	if err != nil {
		t.Fatal(err)
	}
	return lease
}

func findResource(t *testing.T, rows []model.Resource, id string) model.Resource {
	t.Helper()
	for _, row := range rows {
		if row.ID == id {
			return row
		}
	}
	t.Fatalf("resource %s not in projection (%d rows): %+v", id, len(rows), rows)
	return model.Resource{}
}

func TestBrowserProjectionStatesFreeHeldBusyAndRecovery(t *testing.T) {
	cases := accessRoot(t)
	entry := newBrowser(t, cases, "in_one")
	ledger := newFakeLedger()
	ledger.addIntent(model.Intent{ID: "in_one", Title: "one", RunID: ""})
	ledger.addIntent(model.Intent{ID: "in_two", Title: "two", RunID: ""})
	ledger.addRun(model.Run{ID: "r_live", IntentID: "in_one", Status: model.RunRunning, Generation: 1})
	ledger.addRun(model.Run{ID: "r_ended", IntentID: "in_two", Status: model.RunDone, Generation: 2})
	runtime := &fakeRuntime{active: map[string]bool{}}
	service := &Service{Cases: cases, Ledger: ledger, Runtime: runtime}

	// No lease: the site is free, but that is never a claim that it is safe.
	rows, err := service.List("", model.ResourceKindBrowser)
	if err != nil {
		t.Fatal(err)
	}
	free := findResource(t, rows, "browser:"+entry.ID)
	if free.State != model.ResourceStateFree || free.Version != "" || free.HolderRunID != "" {
		t.Fatalf("free browser = %+v", free)
	}

	// A live holder run makes it held; Version is the lease id fence.
	lease := mustAcquire(t, cases, entry.ID, access.BrowserLease{
		Owner: "pt_in_one", RunID: "r_live", IntentID: "in_one", Generation: 1,
	})
	rows, err = service.List("", model.ResourceKindBrowser)
	if err != nil {
		t.Fatal(err)
	}
	held := findResource(t, rows, "browser:"+entry.ID)
	if held.State != model.ResourceStateHeld {
		t.Fatalf("held state = %q", held.State)
	}
	if held.Version != lease.ID || held.HolderRunID != "r_live" || held.IntentID != "in_one" || held.Generation != 1 {
		t.Fatalf("held browser = %+v", held)
	}

	// A native command in flight is busy even while the holder run is live.
	runtime.active[entry.ID] = true
	rows, err = service.List("", model.ResourceKindBrowser)
	if err != nil {
		t.Fatal(err)
	}
	if busy := findResource(t, rows, "browser:"+entry.ID); busy.State != model.ResourceStateBusy {
		t.Fatalf("busy state = %q", busy.State)
	}
	runtime.active[entry.ID] = false

	// The holder ended: no auto-release, the site projects recovery and the
	// lease file is still there.
	if err := access.ReleaseBrowser(cases, entry.ID, lease.ID, access.BrowserLease{
		Owner: "pt_in_one", RunID: "r_live", IntentID: "in_one", Generation: 1,
	}); err != nil {
		t.Fatal(err)
	}
	mustAcquire(t, cases, entry.ID, access.BrowserLease{
		Owner: "pt_in_two", RunID: "r_ended", IntentID: "in_two", Generation: 2,
	})
	rows, err = service.List("", model.ResourceKindBrowser)
	if err != nil {
		t.Fatal(err)
	}
	recovery := findResource(t, rows, "browser:"+entry.ID)
	if recovery.State != model.ResourceStateRecovery {
		t.Fatalf("ended holder state = %q", recovery.State)
	}
	if recovery.HolderRunID != "r_ended" {
		t.Fatalf("recovery lost the holder: %+v", recovery)
	}
	current, err := access.Get(cases, entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Lease == nil {
		t.Fatal("projection released the lease instead of reporting recovery")
	}
}

func TestUnconfirmedBrowserCommandProjectsRecoveryWithoutRewritingTheLease(t *testing.T) {
	cases := accessRoot(t)
	entry := newBrowser(t, cases, "in_one")
	ledger := newFakeLedger()
	ledger.addRun(model.Run{ID: "r_live", IntentID: "in_one", Status: model.RunRunning, Generation: 1})
	runtime := &fakeRuntime{
		active:  map[string]bool{},
		pending: map[string]bool{entry.ID: true},
	}
	mustAcquire(t, cases, entry.ID, access.BrowserLease{
		Owner: "pt_in_one", RunID: "r_live", IntentID: "in_one", Generation: 1,
	})
	service := &Service{Cases: cases, Ledger: ledger, Runtime: runtime}
	rows, err := service.List("", "")
	if err != nil {
		t.Fatal(err)
	}
	row := findResource(t, rows, "browser:"+entry.ID)
	if row.State != model.ResourceStateRecovery {
		t.Fatalf("pending control state = %q", row.State)
	}
	// Both the lease and the marker are the operator's to resolve, not ours.
	current, err := access.Get(cases, entry.ID)
	if err != nil || current.Lease == nil {
		t.Fatalf("lease changed: %+v %v", current.Lease, err)
	}
}

func TestStaleHolderCannotReleaseOrOverwriteANewerLeaseWithTheSameGeneration(t *testing.T) {
	cases := accessRoot(t)
	entry := newBrowser(t, cases, "in_one")
	first := mustAcquire(t, cases, entry.ID, access.BrowserLease{
		Owner: "pt_in_one", RunID: "r_one", IntentID: "in_one", Generation: 2,
	})
	if err := access.ReleaseBrowser(cases, entry.ID, first.ID, access.BrowserLease{
		Owner: "pt_in_one", RunID: "r_one", IntentID: "in_one", Generation: 2,
	}); err != nil {
		t.Fatal(err)
	}
	// Attempts can repeat, so generation 2 is legal for the next holder too.
	second := mustAcquire(t, cases, entry.ID, access.BrowserLease{
		Owner: "pt_in_two", RunID: "r_two", IntentID: "in_two", Generation: 2,
	})
	if second.ID == first.ID {
		t.Fatal("each acquisition must get a fresh lease id")
	}
	if err := access.ReleaseBrowser(cases, entry.ID, first.ID, access.BrowserLease{
		Owner: "pt_in_one", RunID: "r_one", IntentID: "in_one", Generation: 2,
	}); err == nil {
		t.Fatal("the old holder released the newer lease by replaying its generation")
	}
	ledger := newFakeLedger()
	ledger.addRun(model.Run{ID: "r_two", IntentID: "in_two", Status: model.RunRunning, Generation: 2})
	service := &Service{Cases: cases, Ledger: ledger}
	row, ok, err := service.Get("browser:" + entry.ID)
	if err != nil || !ok {
		t.Fatalf("get: %+v %v", ok, err)
	}
	if row.Version != second.ID || row.HolderRunID != "r_two" {
		t.Fatalf("projection reports the stale holder: %+v", row)
	}
}

// Ownership must be self-consistent: a lease whose recorded direction disagrees
// with its holder run, or that is no longer the direction's current run, is not
// reported as effectively held. It becomes recovery (operator must confirm the
// site is idle) instead of silently granting or hiding the resource.
func TestBrowserProjectionFlagsInconsistentHolderIdentity(t *testing.T) {
	cases := accessRoot(t)
	// Case 1: the lease names in_a, its holder run belongs to in_b.
	mismatch := newBrowser(t, cases, "in_a")
	mustAcquire(t, cases, mismatch.ID, access.BrowserLease{
		Owner: "pt_in_a", RunID: "r_foreign", IntentID: "in_a", Generation: 1,
	})
	// Case 2: lease matches the holder run, but the direction moved on to a
	// newer run (handoff/retry): the old lease is not its current site.
	stale := newBrowser(t, cases, "in_c")
	mustAcquire(t, cases, stale.ID, access.BrowserLease{
		Owner: "pt_in_c", RunID: "r_old", IntentID: "in_c", Generation: 1,
	})
	ledger := newFakeLedger()
	ledger.addRun(model.Run{ID: "r_foreign", IntentID: "in_b", Status: model.RunRunning, Generation: 1})
	ledger.addIntent(model.Intent{ID: "in_b", RunID: "r_foreign"})
	ledger.addRun(model.Run{ID: "r_old", IntentID: "in_c", Status: model.RunRunning, Generation: 1})
	ledger.addIntent(model.Intent{ID: "in_c", RunID: "r_new"})
	service := &Service{Cases: cases, Ledger: ledger}

	row, ok, err := service.Get("browser:" + mismatch.ID)
	if err != nil || !ok {
		t.Fatalf("get mismatch: %v %v", ok, err)
	}
	if row.State != model.ResourceStateRecovery || !strings.Contains(row.Detail["issue"], "不一致") {
		t.Fatalf("inconsistent lease was not flagged: %+v", row)
	}
	row, ok, err = service.Get("browser:" + stale.ID)
	if err != nil || !ok {
		t.Fatalf("get stale: %v %v", ok, err)
	}
	if row.State != model.ResourceStateRecovery || !strings.Contains(row.Detail["issue"], "当前 run") {
		t.Fatalf("old-run lease was not flagged: %+v", row)
	}
}

func TestWaitProjectionIsPendingThenCompletedOrUnavailable(t *testing.T) {
	completedAt := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	ledger := newFakeLedger()
	ledger.addRun(model.Run{ID: "r_req", IntentID: "in_one", Status: model.RunDone, Generation: 3})
	ledger.addIntent(model.Intent{
		ID: "in_one", Status: model.StatusWaiting, RunID: "r_req",
		Wait: &model.Wait{
			RegisteredByRun: "r_req",
			RegisteredAt:    completedAt.Add(-time.Hour),
			Operator: &model.OperatorWait{
				ID: "op_1", Request: "登录并进入控制台", AccessIDs: []string{"ac_1", "ac_2"},
			},
		},
	})
	service := &Service{Ledger: ledger}

	rows, err := service.List("in_one", model.ResourceKindWait)
	if err != nil {
		t.Fatal(err)
	}
	pending := findResource(t, rows, "wait:op_1")
	if pending.State != model.ResourceStatePending || pending.Version != "" || pending.Generation != 3 {
		t.Fatalf("pending wait = %+v", pending)
	}

	ledger.intents["in_one"] = func() model.Intent {
		in := ledger.intents["in_one"]
		wait := *in.Wait
		operator := *wait.Operator
		operator.Resolution = model.OperatorUnavailable
		operator.Response = "没有账号"
		operator.CompletedAt = &completedAt
		wait.Operator = &operator
		in.Wait = &wait
		return in
	}()
	rows, err = service.List("in_one", model.ResourceKindWait)
	if err != nil {
		t.Fatal(err)
	}
	unavailable := findResource(t, rows, "wait:op_1")
	if unavailable.State != model.ResourceStateUnavailable {
		t.Fatalf("unavailable wait = %+v", unavailable)
	}
	if !strings.Contains(unavailable.Version, model.OperatorUnavailable) {
		t.Fatalf("version is not the receipt fence: %q", unavailable.Version)
	}
}

func TestUnknownActionStaysUnknownAndReadsCreateNoReplay(t *testing.T) {
	ledger := newFakeLedger()
	ledger.addAction(model.ExternalAction{
		ID: "ax_1", IntentID: "in_one", RunID: "r_one", Generation: 1,
		Key: "deploy-key", Name: "deploy", State: model.ActionUnknown, Version: 4,
		UpdatedAt: time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC),
	})
	service := &Service{Ledger: ledger}
	for i := 0; i < 3; i++ {
		rows, err := service.List("", model.ResourceKindAction)
		if err != nil {
			t.Fatal(err)
		}
		row := findResource(t, rows, "action:ax_1")
		if row.State != model.ResourceStateUnknown || row.Version != "4" {
			t.Fatalf("unknown action = %+v", row)
		}
	}
	if len(ledger.actions) != 1 || len(ledger.actIDs) != 1 {
		t.Fatalf("reads mutated the action ledger: %+v", ledger.actions)
	}
	// The operation name is a label; the result text and idempotency key are not
	// projected, so an unknown result cannot be mistaken for a recorded fact.
	if row := findResource(t, mustList(t, service, ""), "action:ax_1"); row.Detail["name"] != "deploy" {
		t.Fatalf("detail = %+v", row.Detail)
	}
}

func mustList(t *testing.T, service *Service, intentID string) []model.Resource {
	t.Helper()
	rows, err := service.List(intentID, "")
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestSessionProjectionFollowsTheCurrentRunAndItsStatus(t *testing.T) {
	ended := time.Date(2026, 11, 12, 11, 0, 0, 0, time.UTC)
	ledger := newFakeLedger()
	ledger.addRun(model.Run{ID: "r_done", IntentID: "in_one", Status: model.RunDone, Generation: 2, EndedAt: &ended})
	ledger.addRun(model.Run{ID: "r_live", IntentID: "in_two", Status: model.RunRunning, Generation: 1})
	ledger.addRun(model.Run{ID: "r_crash", IntentID: "in_three", Status: model.RunCanceled, CancelCause: model.CancelCauseRestart, Generation: 1})
	ledger.addRun(model.Run{ID: "r_review", IntentID: "in_two", Purpose: "verifier", Status: model.RunDone, Generation: 1})
	ledger.addIntent(model.Intent{ID: "in_one", RunID: "r_done"})
	ledger.addIntent(model.Intent{ID: "in_two", RunID: "r_live"})
	ledger.addIntent(model.Intent{ID: "in_three", RunID: "r_crash"})
	service := &Service{Ledger: ledger}

	rows := mustList(t, service, "in_one")
	if row := findResource(t, rows, "session:r_done"); row.State != model.ResourceStateFree || row.Version != "r_done" {
		t.Fatalf("done session = %+v", row)
	}
	rows = mustList(t, service, "in_two")
	if row := findResource(t, rows, "session:r_live"); row.State != model.ResourceStateBusy {
		t.Fatalf("live session = %+v", row)
	}
	rows = mustList(t, service, "in_three")
	if row := findResource(t, rows, "session:r_crash"); row.State != model.ResourceStateRecovery {
		t.Fatalf("restarted session = %+v", row)
	}
	if _, ok, err := service.Get("session:r_review"); err != nil || ok {
		t.Fatalf("a verifier run must not project as a direction session: %v %v", ok, err)
	}
}

func TestMaterialProjectionIsFreeAndNeverExposesSecretBytes(t *testing.T) {
	cases := accessRoot(t)
	entry, err := access.Create(cases, access.Input{
		Name:   "已登录状态",
		Kind:   "state",
		Origin: "https://state.test",
		Material: json.RawMessage(
			`{"cookies":[{"name":"sid","value":"super-secret-cookie"}],"origins":[]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	ledger := newFakeLedger()
	ledger.addIntent(model.Intent{ID: "in_one"})
	service := &Service{Cases: cases, Ledger: ledger}
	rows := mustList(t, service, "")
	row := findResource(t, rows, "material:"+entry.ID)
	if row.State != model.ResourceStateFree || row.Kind != model.ResourceKindMaterial {
		t.Fatalf("material = %+v", row)
	}
	encoded, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "super-secret-cookie") {
		t.Fatal("material projection leaked the material bytes")
	}
	// The browser identity for the same access id does not exist.
	if _, ok, err := service.Get("browser:" + entry.ID); err != nil || ok {
		t.Fatalf("state material projected as a browser: %v %v", ok, err)
	}
}

func TestKindAndIntentFiltersAndMalformedIDs(t *testing.T) {
	cases := accessRoot(t)
	entry := newBrowser(t, cases, "in_one")
	ledger := newFakeLedger()
	ledger.addRun(model.Run{ID: "r_one", IntentID: "in_one", Status: model.RunRunning, Generation: 1})
	ledger.addIntent(model.Intent{ID: "in_one", RunID: "r_one"})
	ledger.addAction(model.ExternalAction{ID: "ax_one", IntentID: "in_two", RunID: "r_two", Name: "send", State: model.ActionStarted, Version: 1})
	service := &Service{Cases: cases, Ledger: ledger}

	rows := mustList(t, service, "in_one")
	for _, row := range rows {
		if row.IntentID != "in_one" {
			t.Fatalf("intent filter leaked %+v", row)
		}
	}
	if len(mustList(t, service, "in_missing")) != 0 {
		t.Fatal("unknown intent must project no resources")
	}
	if _, err := service.List("", "not-a-kind"); !errors.Is(err, ErrUnknownKind) {
		t.Fatalf("unknown kind error = %v", err)
	}
	for _, id := range []string{"", "browser", "browser:", "nope:ac_x", "browser:../x", "browser:ac x"} {
		if _, _, err := service.Get(id); !errors.Is(err, ErrInvalidID) {
			t.Fatalf("Get(%q) error = %v, want ErrInvalidID", id, err)
		}
	}
	if row, ok, err := service.Get("browser:" + entry.ID); err != nil || !ok || row.Kind != model.ResourceKindBrowser {
		t.Fatalf("get browser: %+v %v", row, err)
	}
	if _, ok, err := service.Get("action:ax_missing"); err != nil || ok {
		t.Fatalf("missing action: %v %v", ok, err)
	}
}
