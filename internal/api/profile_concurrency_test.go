package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/model"
)

// blockingProfilesExec keeps one run live so the per-profile capacity gate can
// observe an occupied slot. Cleanup cancels every run before TempDir removal.
func blockingProfilesExec(t *testing.T, s *Server) *blockingExec {
	t.Helper()
	ex := &blockingExec{}
	s.Exec = ex
	t.Cleanup(func() {
		s.CancelAll()
		s.WaitIdle(5 * time.Second)
		time.Sleep(150 * time.Millisecond)
	})
	return ex
}

func concurrencyProfiles() []ProfileInfo {
	return []ProfileInfo{
		{ID: "fast", Provider: "openai", Model: "fast-model", ContextWindow: 32000, Revision: "rev-fast", Runnable: true, Active: true, MaxConcurrency: 1},
		{ID: "slow", Provider: "anthropic", Model: "slow-model", ContextWindow: 64000, Revision: "rev-slow", Runnable: true, MaxConcurrency: 2},
	}
}

func concurrencyServer(t *testing.T) (*Server, *blockingExec) {
	t.Helper()
	s, p := profileServer(t, concurrencyProfiles()...)
	if err := p.SetNodeBinding([]string{"local"}, "local"); err != nil {
		t.Fatal(err)
	}
	return s, blockingProfilesExec(t, s)
}

func waitForRunStart(t *testing.T, s *Server, runID string) {
	t.Helper()
	waitFor(t, 5*time.Second, func() bool {
		run, ok := s.Store.GetRun(runID)
		if !ok {
			return false
		}
		for _, req := range s.Exec.(*blockingExec).requests() {
			if req.IntentID == run.IntentID {
				return true
			}
		}
		return false
	})
}

// One live run on the profile fills a max_concurrency of 1: the next NEW
// dispatch on that profile is refused with the structured message, while a
// different profile is still dispatchable.
func TestProfileConcurrencyRejectsSecondRunOnSameProfile(t *testing.T) {
	s, ex := concurrencyServer(t)
	p := s.Project
	first, err := p.Store.CreateIntent(model.Intent{Title: "第一个 fast", Profile: "fast"})
	if err != nil {
		t.Fatal(err)
	}
	runID, err := s.StartRun(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitForRunStart(t, s, runID)
	if got := s.runningProfileCount("fast"); got != 1 {
		t.Fatalf("running fast = %d", got)
	}

	second, _ := p.Store.CreateIntent(model.Intent{Title: "第二个 fast", Profile: "fast"})
	_, err = s.StartRun(second.ID)
	if err == nil {
		t.Fatal("a second run on a full profile was dispatched")
	}
	for _, want := range []string{"并发已到上限 1", "max_concurrency", "fast"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err, want)
		}
	}

	// Different profile: its own cap (2) is free.
	other, _ := p.Store.CreateIntent(model.Intent{Title: "slow", Profile: "slow"})
	otherRun, err := s.StartRun(other.ID)
	if err != nil {
		t.Fatalf("a different profile was blocked: %v", err)
	}
	waitForRunStart(t, s, otherRun)
	if len(ex.requests()) < 2 {
		t.Fatalf("requests = %d", len(ex.requests()))
	}

	// The HTTP surface returns the same structured refusal (409 + message).
	third, _ := p.Store.CreateIntent(model.Intent{Title: "第三个 fast", Profile: "fast"})
	rec := call(t, s, http.MethodPost, "/api/intents/"+third.ID+"/run", nil)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "max_concurrency") {
		t.Fatalf("runIntent = %d %s", rec.Code, rec.Body.String())
	}
}

// 0 means unlimited: two live runs on the same profile are fine.
func TestProfileConcurrencyZeroIsUnlimited(t *testing.T) {
	s, _ := concurrencyServer(t)
	s.Profiles = func() ([]ProfileInfo, string, error) {
		return []ProfileInfo{{ID: "fast", Provider: "openai", Model: "m", Revision: "r", Runnable: true, MaxConcurrency: 0}}, "fast", nil
	}
	p := s.Project
	for i := 0; i < 2; i++ {
		in, err := p.Store.CreateIntent(model.Intent{Title: "fast", Profile: "fast"})
		if err != nil {
			t.Fatal(err)
		}
		runID, err := s.StartRun(in.ID)
		if err != nil {
			t.Fatalf("run %d refused with an unlimited profile: %v", i, err)
		}
		waitForRunStart(t, s, runID)
	}
	if got := s.runningProfileCount("fast"); got != 2 {
		t.Fatalf("running fast = %d", got)
	}
}

// The gate uses the same default chain as dispatch: an intent without a profile
// runs on the project worker_profile and is capped there; a different profile
// (explicit) remains available.
func TestProfileConcurrencyAppliesToDefaultProfile(t *testing.T) {
	s, _ := concurrencyServer(t)
	s.ProjectProfiles = &ProfileDefaults{Worker: "slow", Strong: "fast"}
	p := s.Project
	first, _ := p.Store.CreateIntent(model.Intent{Title: "默认档 1"})
	runID, err := s.StartRun(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitForRunStart(t, s, runID)
	run, _ := p.Store.GetRun(runID)
	if run.ResolvedProfile != "slow" || run.ProfileSource != "project_worker" {
		t.Fatalf("default run binding = %+v", run)
	}
	// slow has cap 2, so one more default run is allowed and the third refused.
	second, _ := p.Store.CreateIntent(model.Intent{Title: "默认档 2"})
	run2, err := s.StartRun(second.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitForRunStart(t, s, run2)
	third, _ := p.Store.CreateIntent(model.Intent{Title: "默认档 3"})
	if _, err := s.StartRun(third.ID); err == nil || !strings.Contains(err.Error(), "slow") {
		t.Fatalf("third default run = %v, want the slow profile cap", err)
	}
	// An explicit fast run is a different profile (fast cap 1, free).
	explicit, _ := p.Store.CreateIntent(model.Intent{Title: "显式 fast", Profile: "fast"})
	if _, err := s.StartRun(explicit.ID); err != nil {
		t.Fatalf("explicit profile was blocked by the default profile's cap: %v", err)
	}
}

// With no project default the instance active profile is the target; the gate
// follows that binding too.
func TestProfileConcurrencyAppliesToActiveProfile(t *testing.T) {
	s, _ := concurrencyServer(t)
	if s.ProjectProfiles != nil {
		t.Fatalf("fixture should have no project profile default")
	}
	p := s.Project
	first, _ := p.Store.CreateIntent(model.Intent{Title: "active 1"})
	runID, err := s.StartRun(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitForRunStart(t, s, runID)
	run, _ := p.Store.GetRun(runID)
	if run.ResolvedProfile != "fast" || run.ProfileSource != "default" {
		t.Fatalf("active default binding = %+v", run)
	}
	second, _ := p.Store.CreateIntent(model.Intent{Title: "active 2"})
	if _, err := s.StartRun(second.ID); err == nil {
		t.Fatal("a second run on the full active profile was dispatched")
	}
}

// A resumed turn still starts a run and must acquire profile capacity.
func TestProfileConcurrencyAlsoGatesResume(t *testing.T) {
	s, _ := concurrencyServer(t)
	p := s.Project
	first, _ := p.Store.CreateIntent(model.Intent{Title: "占用 fast", Profile: "fast"})
	runID, err := s.StartRun(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitForRunStart(t, s, runID)

	resumed, _ := p.Store.CreateIntent(model.Intent{Title: "续话", Profile: "fast"})
	if _, err := s.startRunWith(resumed.ID, "继续原来的方向", "", true, true); err == nil {
		t.Fatal("resume bypassed the full profile cap")
	}

	fresh, _ := p.Store.CreateIntent(model.Intent{Title: "新派发", Profile: "fast"})
	if _, err := s.StartRun(fresh.ID); err == nil {
		t.Fatal("new dispatch on the full profile was allowed")
	}
}

// Purpose runs (commander/verifier/report) are not workers and do not consume a
// profile's worker capacity.
func TestProfileConcurrencyIgnoresPurposeRuns(t *testing.T) {
	s, _ := concurrencyServer(t)
	p := s.Project
	if _, err := p.Store.AddRun(model.Run{IntentID: "in_cmd", Purpose: "commander", ResolvedProfile: "fast"}); err != nil {
		t.Fatal(err)
	}
	in, _ := p.Store.CreateIntent(model.Intent{Title: "worker", Profile: "fast"})
	if _, err := s.StartRun(in.ID); err != nil {
		t.Fatalf("purpose run consumed worker capacity: %v", err)
	}
}

// Without a profile catalogue (flat/env deployment) the gate is skipped: the
// instance keeps the old behaviour.
func TestProfileConcurrencySkippedWithoutProfileCatalogue(t *testing.T) {
	s, p := newBindingServer(t)
	if err := p.SetNodeBinding([]string{"local"}, "local"); err != nil {
		t.Fatal(err)
	}
	s.LocalNode = "local"
	blockingProfilesExec(t, s)
	for i := 0; i < 2; i++ {
		in, _ := p.Store.CreateIntent(model.Intent{Title: "profile-less"})
		if _, err := s.StartRun(in.ID); err != nil {
			t.Fatalf("profile-less dispatch %d refused: %v", i, err)
		}
	}
}

// The model-profiles view reports the cap and live occupancy, so an operator or
// an agent can see "1/1" before choosing a profile.
func TestModelProfilesReportMaxConcurrencyAndRunning(t *testing.T) {
	s, _ := concurrencyServer(t)
	p := s.Project
	in, _ := p.Store.CreateIntent(model.Intent{Title: "占用", Profile: "fast"})
	runID, err := s.StartRun(in.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitForRunStart(t, s, runID)

	rec := call(t, s, http.MethodGet, "/api/model-profiles", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("model-profiles: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Profiles []ProfileInfo `json:"profiles"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	byID := map[string]ProfileInfo{}
	for _, p := range resp.Profiles {
		byID[p.ID] = p
	}
	if got := byID["fast"]; got.MaxConcurrency != 1 || got.Running != 1 {
		t.Fatalf("fast view = %+v", got)
	}
	if got := byID["slow"]; got.MaxConcurrency != 2 || got.Running != 0 {
		t.Fatalf("slow view = %+v", got)
	}
	for _, want := range []string{`"max_concurrency":1`, `"running":1`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("model-profiles body missing %q: %s", want, rec.Body.String())
		}
	}
}
