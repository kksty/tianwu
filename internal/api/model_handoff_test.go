package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
)

func handoffProfiles() func() ([]ProfileInfo, string, error) {
	return func() ([]ProfileInfo, string, error) {
		return []ProfileInfo{
			{ID: "fast", Provider: "openai", Model: "fast-model", Revision: "rev-fast", Runnable: true},
			{ID: "slow", Provider: "anthropic", Model: "slow-model", Revision: "rev-slow", Runnable: true},
		}, "fast", nil
	}
}

func TestHandoffStartsNewSessionOnNewProfile(t *testing.T) {
	f := newParentFixture(t)
	f.srv.Profiles = handoffProfiles()
	// Create the child while the fixture is still "running": a waiting parent
	// cannot delegate.
	child, _, err := f.p.Store.CreateChildIntent(f.ident.IntentID, model.Intent{Title: "子", CreatedBy: f.ident.ParticipantID})
	if err != nil {
		t.Fatal(err)
	}
	// The fixture's run was never dispatched; make the direction waiting so the
	// handoff exercises the no-live-turn path and still keeps a from-run id.
	if _, err := f.p.Store.UpdateIntent(f.ident.IntentID, func(i *model.Intent) {
		i.Status = model.StatusWaiting
		i.RunID = f.run.ID
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Store.RegisterWait(f.ident.IntentID, f.run.ID, model.Wait{
		Children: []string{child.ID}, Mode: model.WaitAll,
	}); err != nil {
		t.Fatal(err)
	}
	rec := f.call(t, nil, http.MethodPost, "/api/intents/"+f.ident.IntentID+"/handoff", map[string]any{
		"profile": "slow", "note": "换到慢模型继续核对", "expected_version": 0,
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("handoff: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Intent  model.Intent  `json:"intent"`
		Handoff model.Handoff `json:"handoff"`
		RunID   string        `json:"run_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Intent.Profile != "slow" || resp.Intent.HandoffVersion != 1 || resp.Intent.Handoff == nil {
		t.Fatalf("intent = %+v", resp.Intent)
	}
	if resp.Handoff.ToProfile != "slow" || resp.Handoff.FromRun != f.run.ID ||
		resp.Handoff.Note != "换到慢模型继续核对" || len(resp.Handoff.Children) != 1 {
		t.Fatalf("handoff = %+v", resp.Handoff)
	}
	if resp.RunID == "" {
		t.Fatal("no replacement run")
	}
	run, ok := f.p.Store.GetRun(resp.RunID)
	if !ok || run.HandoffFrom != f.run.ID || run.HandoffVersion != 1 {
		t.Fatalf("replacement run = %+v", run)
	}
	cur, _ := f.p.Store.GetIntent(f.ident.IntentID)
	if cur.Wait == nil || cur.Wait.RegisteredByRun != resp.RunID {
		t.Fatalf("wait not rebound: %+v", cur.Wait)
	}
	if cur.Profile != "slow" {
		t.Fatalf("profile = %q", cur.Profile)
	}
	if _, err := f.p.Store.UpdateRun(f.run.ID, func(r *model.Run) {
		now := time.Now()
		r.Status = model.RunDone
		r.EndedAt = &now
	}); err != nil {
		t.Fatal(err)
	}
	waitNoRunning(t, f.p)
}

func TestHandoffVersionConflictAndFinishedIntent(t *testing.T) {
	f := newParentFixture(t)
	f.srv.Profiles = handoffProfiles()
	if _, err := f.p.Store.UpdateIntent(f.ident.IntentID, func(i *model.Intent) {
		i.Status = model.StatusPending
		i.RunID = ""
	}); err != nil {
		t.Fatal(err)
	}
	// Keep the direction waiting across both handoffs: a pending child would be
	// settled by the fake executor and the second request would be "已结束".
	child, _, err := f.p.Store.CreateChildIntent(f.ident.IntentID, model.Intent{Title: "保持打开", CreatedBy: f.ident.ParticipantID})
	if err != nil {
		t.Fatal(err)
	}
	// Keep the scheduler from running the child: this test is about the handoff
	// version CAS, not about the child finishing.
	if _, err := f.p.Store.UpdateIntent(child.ID, func(i *model.Intent) { i.Hold = true }); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Store.RegisterWait(f.ident.IntentID, "", model.Wait{
		Children: []string{child.ID}, Mode: model.WaitAll,
	}); err != nil {
		t.Fatal(err)
	}
	if rec := f.call(t, nil, http.MethodPost, "/api/intents/"+f.ident.IntentID+"/handoff", map[string]any{
		"profile": "slow", "expected_version": 0,
	}); rec.Code != http.StatusAccepted {
		t.Fatalf("first: %d %s", rec.Code, rec.Body.String())
	}
	// The fixture run was never dispatched; settle its row so the helper waits
	// only for the replacement run.
	if _, err := f.p.Store.UpdateRun(f.run.ID, func(r *model.Run) {
		now := time.Now()
		r.Status = model.RunDone
		r.EndedAt = &now
	}); err != nil {
		t.Fatal(err)
	}
	waitNoRunning(t, f.p)
	// The same stale version cannot overwrite; the new version can.
	rec := f.call(t, nil, http.MethodPost, "/api/intents/"+f.ident.IntentID+"/handoff", map[string]any{
		"profile": "fast", "expected_version": 0,
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale: %d %s", rec.Code, rec.Body.String())
	}
	rec = f.call(t, nil, http.MethodPost, "/api/intents/"+f.ident.IntentID+"/handoff", map[string]any{
		"profile": "fast", "expected_version": 1,
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("second: %d %s", rec.Code, rec.Body.String())
	}
	waitNoRunning(t, f.p)

	if _, err := f.p.Store.UpdateIntent(f.ident.IntentID, func(i *model.Intent) { i.Status = model.StatusDone }); err != nil {
		t.Fatal(err)
	}
	if rec := f.call(t, nil, http.MethodPost, "/api/intents/"+f.ident.IntentID+"/handoff", map[string]any{
		"profile": "slow", "expected_version": 2,
	}); rec.Code != http.StatusConflict {
		t.Fatalf("done handoff: %d %s", rec.Code, rec.Body.String())
	}
}

// A stale expected_version must fail before any side effect: the live run keeps
// running and the intent keeps its holder. Regression for the destructive CAS
// where the old ordering cancelled the run first and only then found the conflict.
func TestStaleHandoffDoesNotCancelLiveRun(t *testing.T) {
	f := newParentFixture(t)
	f.srv.Profiles = handoffProfiles()
	if _, err := f.p.Store.UpdateIntent(f.ident.IntentID, func(i *model.Intent) { i.HandoffVersion = 1 }); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Store.UpdateRun(f.run.ID, func(r *model.Run) { r.Status = model.RunRunning }); err != nil {
		t.Fatal(err)
	}
	rec := f.call(t, nil, http.MethodPost, "/api/intents/"+f.ident.IntentID+"/handoff", map[string]any{
		"profile": "slow", "expected_version": 0,
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale handoff: %d %s", rec.Code, rec.Body.String())
	}
	cur, _ := f.p.Store.GetIntent(f.ident.IntentID)
	if cur.Status != model.StatusRunning || cur.RunID != f.run.ID || cur.Hold || cur.HandoffVersion != 1 {
		t.Fatalf("stale handoff changed the intent: %+v", cur)
	}
	run, _ := f.p.Store.GetRun(f.run.ID)
	if run.Status != model.RunRunning {
		t.Fatalf("stale handoff stopped the live run: %+v", run)
	}
	// Let the fixture settle its own row before TempDir cleanup.
	now := time.Now()
	if _, err := f.p.Store.UpdateRun(f.run.ID, func(r *model.Run) {
		r.Status = model.RunDone
		r.EndedAt = &now
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Store.UpdateIntent(f.ident.IntentID, func(i *model.Intent) { i.Status = model.StatusPending }); err != nil {
		t.Fatal(err)
	}
	waitNoRunning(t, f.p)
}

func TestFinishRecordsUsageAndCostFromPricingSnapshot(t *testing.T) {
	f := newParentFixture(t)
	price := &model.Pricing{Currency: "USD", InputPerMToken: 2, OutputPerMToken: 10}
	run, err := f.p.Store.AddRun(model.Run{
		IntentID: f.ident.IntentID, Node: "local", Pricing: price,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Store.UpdateIntent(f.ident.IntentID, func(i *model.Intent) {
		i.Status = model.StatusRunning
		i.RunID = run.ID
	}); err != nil {
		t.Fatal(err)
	}
	f.srv.finish(run.ID, f.ident.IntentID, model.RunDone, "", executor.Result{
		ExitCode: 0, StopReason: "completed", UsageKnown: true,
		InputTokens: 1_000_000, OutputTokens: 100_000, CachedInputTokens: 400_000,
		UsageRequests: 2, Tokens: 1_100_000,
	}, 0)
	got, _ := f.p.Store.GetRun(run.ID)
	if !got.Usage.Known || got.Usage.InputTokens != 1_000_000 || got.Usage.OutputTokens != 100_000 ||
		got.Usage.CachedInputTokens != 400_000 || got.Usage.Requests != 2 {
		t.Fatalf("usage = %+v", got.Usage)
	}
	if got.Cost != nil {
		t.Fatal("new runs must not estimate monetary cost")
	}
	// 600k input*$2/M + 400k cached*$0/M (price 0) + 100k output*$10/M = 2.2
}

func TestIntentUsageSummarizesDirectAndSubtreeOnce(t *testing.T) {
	f := newParentFixture(t)
	child, _, err := f.p.Store.CreateChildIntent(f.ident.IntentID, model.Intent{Title: "子", CreatedBy: f.ident.ParticipantID})
	if err != nil {
		t.Fatal(err)
	}
	// Direct parent run (the fixture row) and a child run, plus an unknown child
	// run that must stay unknown rather than count as zero-cost known usage.
	if _, err := f.p.Store.UpdateRun(f.run.ID, func(r *model.Run) {
		r.Usage = model.Usage{InputTokens: 100, OutputTokens: 50, TotalTokens: 150, Requests: 1, Known: true}
		r.Cost = &model.Cost{Currency: "USD", Total: 0.1, Estimated: true}
	}); err != nil {
		t.Fatal(err)
	}
	childRun, err := f.p.Store.AddRun(model.Run{IntentID: child.ID, Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Store.UpdateRun(childRun.ID, func(r *model.Run) {
		r.Usage = model.Usage{InputTokens: 200, OutputTokens: 100, CachedInputTokens: 50, TotalTokens: 300, Requests: 2, Known: true}
		r.Cost = &model.Cost{Currency: "USD", Total: 0.2, Estimated: true}
	}); err != nil {
		t.Fatal(err)
	}
	unknown, err := f.p.Store.AddRun(model.Run{IntentID: child.ID, Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Store.UpdateRun(unknown.ID, func(r *model.Run) { r.Usage = model.Usage{} }); err != nil {
		t.Fatal(err)
	}

	w := call(t, f.srv, http.MethodGet, "/api/intents/"+f.ident.IntentID+"/usage", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("usage: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Direct  usageSummary `json:"direct"`
		Subtree usageSummary `json:"subtree"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Direct.Runs != 1 || resp.Direct.TotalTokens != 150 {
		t.Fatalf("direct = %+v", resp.Direct)
	}
	if resp.Subtree.Runs != 3 || resp.Subtree.TotalTokens != 450 || resp.Subtree.UnknownRuns != 1 {
		t.Fatalf("subtree = %+v", resp.Subtree)
	}
}
