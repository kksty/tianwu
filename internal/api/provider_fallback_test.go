package api

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/decider"
	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
)

// A run on the instance default host cannot be fixed by the key-rotation
// cursor: that host is not a profile host and its environment keeps the primary
// key. The hook must say so and stop the project instead of rotating a host
// that did not run this run (D67/D92).
func TestDefaultHostRunSkipsRotationAndPauses(t *testing.T) {
	s, in, run := providerTestServer(t, "fast")
	if _, err := s.Store.UpdateRun(run.ID, func(r *model.Run) {
		r.Profile = ""
		r.ProfileSource = "default"
	}); err != nil {
		t.Fatal(err)
	}
	rotations := 0
	s.RotateProfileKey = func(string) (bool, error) { rotations++; return true, nil }
	s.retryProviderIntentFn = func(string, string, string, string) (string, error) {
		t.Fatal("a default-host run must not be retried by the rotation path")
		return "", nil
	}
	s.afterProviderFailure(run.ID, in.ID, model.RunFailed, authFailure())
	if rotations != 0 {
		t.Fatalf("rotations = %d, want 0 (the default host has no profile pool to rotate)", rotations)
	}
	if !s.paused.Load() {
		t.Fatal("default-host credential failure with no fallback must pause the project (D67)")
	}
}

// No spare key on a profile-bound run: the operator-configured fallback is the
// second action, and it is a NEW run on the target profile, not a rewrite of
// the failed binding.
func TestFallbackSwitchesProfileOnce(t *testing.T) {
	s, in, run := providerTestServer(t, "fast")
	s.RotateProfileKey = func(string) (bool, error) { return false, nil } // no spare key
	s.ProfileFallback = func(source string) (string, error) {
		if source != "fast" {
			t.Fatalf("fallback source = %q, want fast", source)
		}
		return "slow", nil
	}
	var gotProfile, gotFrom, gotNote string
	s.retryProviderIntentFn = func(_, note, profileOverride, fallbackFrom string) (string, error) {
		gotNote, gotProfile, gotFrom = note, profileOverride, fallbackFrom
		return "r_fb", nil
	}
	s.afterProviderFailure(run.ID, in.ID, model.RunFailed, authFailure())
	if gotProfile != "slow" || gotFrom != "fast" {
		t.Fatalf("fallback target = %q from %q, want slow from fast", gotProfile, gotFrom)
	}
	if gotNote != providerFallbackNote {
		t.Fatalf("fallback note = %q", gotNote)
	}
	if s.paused.Load() {
		t.Fatal("a successful fallback must not pause the project")
	}
	got, ok := s.Store.GetRun(run.ID)
	if !ok {
		t.Fatal("failed run disappeared")
	}
	warnings := strings.Join(got.Warnings, "\n")
	if !strings.Contains(warnings, "已自动切到 slow") || !strings.Contains(warnings, "fast") {
		t.Fatalf("fallback warning missing from the failed run: %q", warnings)
	}
}

// A fallback run already hopped once. Its own credential failure must never
// resolve another hop: that would hide the depth of the outage and can
// ping-pong between two dead profiles.
func TestFallbackIsOneHopOnly(t *testing.T) {
	s, in, run := providerTestServer(t, "slow")
	if _, err := s.Store.UpdateRun(run.ID, func(r *model.Run) { r.FallbackFrom = "fast" }); err != nil {
		t.Fatal(err)
	}
	s.RotateProfileKey = func(string) (bool, error) { return false, nil }
	s.ProfileFallback = func(string) (string, error) {
		t.Fatal("must not resolve a second fallback hop")
		return "", nil
	}
	s.retryProviderIntentFn = func(string, string, string, string) (string, error) {
		t.Fatal("must not start a second fallback run")
		return "", nil
	}
	s.afterProviderFailure(run.ID, in.ID, model.RunFailed, authFailure())
	if !s.paused.Load() {
		t.Fatal("an exhausted fallback chain must pause the project (D67)")
	}
}

// A misconfigured fallback is an error the operator must see, not a silent
// "stay put": warn on the run, then stop the project.
func TestFallbackInvalidPausesAndWarns(t *testing.T) {
	s, in, run := providerTestServer(t, "fast")
	s.RotateProfileKey = func(string) (bool, error) { return false, nil }
	s.ProfileFallback = func(string) (string, error) {
		return "", errors.New("目标不存在")
	}
	s.afterProviderFailure(run.ID, in.ID, model.RunFailed, authFailure())
	if !s.paused.Load() {
		t.Fatal("an invalid fallback must pause the project")
	}
	got, ok := s.Store.GetRun(run.ID)
	if !ok {
		t.Fatal("failed run disappeared")
	}
	if warnings := strings.Join(got.Warnings, "\n"); !strings.Contains(warnings, "fallback 配置不可用") {
		t.Fatalf("invalid fallback warning missing: %q", warnings)
	}
}

// D67 wiring: pausing the project is a visible state (the console reads
// Decider.Status), not just a dispatch gate.
func TestNoSpareKeyPausesCommanderWithReason(t *testing.T) {
	s, in, run := providerTestServer(t, "fast")
	s.Decider = decider.NewRunner(s.Store, nil, filepath.Join(s.DataDir, "decisions.jsonl"))
	s.RotateProfileKey = func(string) (bool, error) { return false, nil }
	s.afterProviderFailure(run.ID, in.ID, model.RunFailed, authFailure())
	if !s.Decider.Paused() {
		t.Fatal("credential exhaustion must pause the commander")
	}
	if reason := s.Decider.Status().PausedReason; !strings.Contains(reason, "额度/key") {
		t.Fatalf("paused reason = %q, want the D67 wording", reason)
	}
}

// A fallback-only instance (no profile host pool wired) must still be able to
// switch; the rotation hook being nil must not panic or skip the switch.
func TestFallbackWorksWithoutRotationHook(t *testing.T) {
	s, in, run := providerTestServer(t, "fast")
	s.RotateProfileKey = nil
	s.ProfileFallback = func(string) (string, error) { return "slow", nil }
	var gotProfile, gotFrom string
	s.retryProviderIntentFn = func(_, _, profileOverride, fallbackFrom string) (string, error) {
		gotProfile, gotFrom = profileOverride, fallbackFrom
		return "r_fb", nil
	}
	s.afterProviderFailure(run.ID, in.ID, model.RunFailed, authFailure())
	if gotProfile != "slow" || gotFrom != "fast" {
		t.Fatalf("fallback = %q from %q, want slow from fast", gotProfile, gotFrom)
	}
	if s.paused.Load() {
		t.Fatal("a successful fallback must not pause the project")
	}
}

// failingExec is a commander round whose provider returned an auth/quota error.
type failingExec struct{ res executor.Result }

func (f *failingExec) Name() string { return "failing" }

func (f *failingExec) Run(_ context.Context, _ executor.Request, _ func(executor.Event)) (executor.Result, error) {
	return f.res, nil
}

func (f *failingExec) CanResume(string, string) bool { return false }

// The commander does not go through finish()/afterProviderFailure (it settles
// its own purpose run). A dead key on a commander round must still hit D67:
// pause the project with a visible reason, or the heartbeat keeps spending on
// the same key.
func TestCommanderAuthFailurePausesProject(t *testing.T) {
	s, _, st := newCommanderServer(t)
	s.Exec = &failingExec{res: executor.Result{
		IsError: true, StopReason: "error", Output: "API error (401): invalid api key",
	}}
	s.Decider = decider.NewRunner(st, nil, filepath.Join(filepath.Dir(s.CaseRoot), "decisions.jsonl"))
	if _, _, err := s.StartCommander(context.Background(), "## 本轮", false); err == nil {
		t.Fatal("a failed commander round must return the error")
	}
	if !s.paused.Load() {
		t.Fatal("commander auth failure must pause the project (D67)")
	}
	if !s.Decider.Paused() {
		t.Fatal("commander auth failure must pause the decider loop")
	}
	status := s.Decider.Status()
	if !strings.Contains(status.PausedReason, "额度/key") {
		t.Fatalf("paused reason = %q, want the D67 wording", status.PausedReason)
	}
	runs := st.Runs()
	if len(runs) != 1 {
		t.Fatalf("runs = %+v", runs)
	}
	if warnings := strings.Join(runs[0].Warnings, "\n"); !strings.Contains(warnings, "指挥官模型凭据不可用") {
		t.Fatalf("commander run has no provider warning: %q", warnings)
	}
}

// A rate limit is not a dead credential: the harness retries it, and tianwu
// must not pause the project for it.
func TestCommanderRateLimitDoesNotPause(t *testing.T) {
	s, _, st := newCommanderServer(t)
	s.Exec = &failingExec{res: executor.Result{
		IsError: true, StopReason: "error", Output: "API error (429): rate limit reached",
	}}
	s.Decider = decider.NewRunner(st, nil, filepath.Join(filepath.Dir(s.CaseRoot), "decisions.jsonl"))
	if _, _, err := s.StartCommander(context.Background(), "## 本轮", false); err == nil {
		t.Fatal("a failed commander round must return the error")
	}
	if s.paused.Load() || s.Decider.Paused() {
		t.Fatal("a plain rate limit must not pause the project")
	}
}

// The fallback run is bound to the target profile at dispatch time; the failed
// run's record and the intent's own profile are not rewritten. This is the
// shape the console and cost accounting rely on.
func TestFallbackRunBindsTargetProfile(t *testing.T) {
	s, p := profileServer(t, testProfiles()...)
	if err := p.SetNodeBinding([]string{"local"}, "local"); err != nil {
		t.Fatal(err)
	}
	in, err := s.Store.CreateIntent(model.Intent{Title: "切档测试", Profile: "fast"})
	if err != nil {
		t.Fatal(err)
	}
	runID, _, err := s.retryIntentRunProfile(in.ID, "fallback test", false, "slow", "fast")
	if err != nil {
		t.Fatal(err)
	}
	run, ok := s.Store.GetRun(runID)
	if !ok {
		t.Fatal("fallback run not created")
	}
	if run.Profile != "slow" || run.ResolvedProfile != "slow" {
		t.Fatalf("run profile = %q/%q, want slow/slow", run.Profile, run.ResolvedProfile)
	}
	if run.ProfileSource != "fallback" || run.FallbackFrom != "fast" {
		t.Fatalf("profile source = %q fallback_from = %q, want fallback/fast", run.ProfileSource, run.FallbackFrom)
	}
	if run.Provider != "anthropic" || run.Model != "slow-model" {
		t.Fatalf("run binding = %s/%s, want the fallback profile's provider/model", run.Provider, run.Model)
	}
	// The fallback binding's tier is frozen on the new run too: a run that
	// switched profiles must not report the source tier or none at all.
	if run.Tier != "strong" {
		t.Fatalf("fallback run tier = %q, want the target profile's strong", run.Tier)
	}
	if fresh, _ := s.Store.GetIntent(in.ID); fresh.Profile != "fast" {
		t.Fatalf("intent profile was rewritten to %q; the switch is per-run", fresh.Profile)
	}
}
