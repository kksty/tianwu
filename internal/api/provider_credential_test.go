package api

// F6: a provider failure is attributed to the credential generation frozen on
// the run at dispatch, and the shared profile owner (settings.Store) decides
// whether an old-key/duplicate failure rotates, falls back, or neither. These
// tests pin the api layer's reaction to the owner's states; the state machine
// itself is covered in internal/settings/credentials_test.go.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/settings"
)

const sharedCredentialProfiles = `llm:
    active_profile: fast
    providers:
        - id: fast
          provider: openai
          base_url: http://fast:8080
          api_key: fast-primary
          api_keys:
              - fast-spare
          model: fast-model
          tier: worker
          fallback: backup
        - id: backup
          provider: anthropic
          base_url: http://backup:8080
          api_key: backup-primary
          model: backup-model
          tier: worker
`

const singleCredentialProfiles = `llm:
    active_profile: fast
    providers:
        - id: fast
          provider: openai
          base_url: http://fast:8080
          api_key: fast-primary
          model: fast-model
          tier: worker
`

func credentialStoreFrom(t *testing.T, body string) *settings.Store {
	t.Helper()
	dir := t.TempDir()
	harness := filepath.Join(dir, "cyber.yaml")
	if err := os.WriteFile(harness, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := settings.LoadAt(filepath.Join(dir, "settings.json"), harness)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// sharedProviderTestServer is providerTestServer plus the shared credential
// owner: the shape the hub wires, where every project's Server shares one
// settings store.
func sharedProviderTestServer(t *testing.T, profile string) (*Server, *settings.Store, model.Intent, model.Run) {
	t.Helper()
	s, in, run := providerTestServer(t, profile)
	creds := credentialStoreFrom(t, sharedCredentialProfiles)
	s.Credentials = creds
	setRunGeneration(t, s, run.ID, creds.CredentialGeneration(profile))
	return s, creds, in, run
}

func setRunGeneration(t *testing.T, s *Server, runID string, generation uint64) {
	t.Helper()
	if _, err := s.Store.UpdateRun(runID, func(r *model.Run) { r.CredentialGeneration = generation }); err != nil {
		t.Fatal(err)
	}
}

// addRunAtGeneration creates one settled run for the same intent, bound to the
// generation it claims to have used.
func addRunAtGeneration(t *testing.T, s *Server, intentID, profile string, generation uint64, status string) model.Run {
	t.Helper()
	run, err := s.Store.AddRun(model.Run{
		IntentID: intentID, Node: "local", Profile: profile,
		ResolvedProfile: profile, ProfileSource: "explicit",
		CredentialGeneration: generation,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.UpdateRun(run.ID, func(r *model.Run) { r.Status = status }); err != nil {
		t.Fatal(err)
	}
	return run
}

// The watchdog hook can be called twice for the same settled run. After a
// successful rotation the second call is a stale generation: it must not rotate
// again, retry again, or pause the project.
func TestDuplicateProviderFailureAfterRotationIsStale(t *testing.T) {
	s, creds, in, run := sharedProviderTestServer(t, "fast")
	var rotations, restarts, retries int
	s.RotateProfileKey = func(string) (bool, error) { rotations++; return true, nil }
	s.RestartProfileHost = func(string) error { restarts++; return nil }
	s.retryProviderIntentFn = func(_, note, profileOverride, fallbackFrom string) (string, error) {
		if note != providerRetryNote || profileOverride != "" || fallbackFrom != "" {
			t.Fatalf("unexpected retry %q/%q/%q", note, profileOverride, fallbackFrom)
		}
		retries++
		return "r_retry", nil
	}

	s.afterProviderFailure(run.ID, in.ID, model.RunFailed, authFailure())
	if gen := creds.CredentialGeneration("fast"); gen != 1 {
		t.Fatalf("generation after rotation = %d, want 1", gen)
	}
	// Duplicate finish callback for the same run: the run used generation 0,
	// which the rotation has already superseded.
	s.afterProviderFailure(run.ID, in.ID, model.RunFailed, authFailure())

	if rotations != 1 || restarts != 1 || retries != 1 {
		t.Fatalf("rotations=%d restarts=%d retries=%d, want 1/1/1", rotations, restarts, retries)
	}
	if s.paused.Load() {
		t.Fatal("a stale duplicate callback must not pause the project")
	}
}

// A run dispatched before the rotation can fail concurrently afterwards. Its
// failure is on the old key, so it must not rotate a second time nor pause the
// project that now runs on the new key.
func TestConcurrentOldGenerationFailureDoesNotPause(t *testing.T) {
	s, creds, in, run := sharedProviderTestServer(t, "fast")
	// This run was already dispatched on generation 0 when the rotation happens.
	oldRun := addRunAtGeneration(t, s, in.ID, "fast", 0, model.RunFailed)
	var rotations, restarts int
	s.RotateProfileKey = func(string) (bool, error) { rotations++; return true, nil }
	s.RestartProfileHost = func(string) error { restarts++; return nil }
	s.retryProviderIntentFn = func(string, string, string, string) (string, error) { return "r_retry", nil }

	s.afterProviderFailure(run.ID, in.ID, model.RunFailed, authFailure())
	if gen := creds.CredentialGeneration("fast"); gen != 1 {
		t.Fatalf("generation after rotation = %d, want 1", gen)
	}
	s.afterProviderFailure(oldRun.ID, in.ID, model.RunFailed, authFailure())

	if rotations != 1 || restarts != 1 {
		t.Fatalf("rotations=%d restarts=%d, want 1/1", rotations, restarts)
	}
	if s.paused.Load() {
		t.Fatal("an old-generation concurrent failure must not pause the project")
	}
}

// A failure inside the shared cooling window proves neither the new key nor the
// pool is dead: no rotation, no pause.
func TestCoolingFailureDoesNotPauseOrRotate(t *testing.T) {
	s, creds, in, run := sharedProviderTestServer(t, "fast")
	var rotations int
	s.RotateProfileKey = func(string) (bool, error) { rotations++; return true, nil }
	s.RestartProfileHost = func(string) error { return nil }
	s.retryProviderIntentFn = func(string, string, string, string) (string, error) { return "r_retry", nil }

	s.afterProviderFailure(run.ID, in.ID, model.RunFailed, authFailure())
	if state, gen := creds.CredentialState("fast"); state != settings.CredentialCooling || gen != 1 {
		t.Fatalf("state after rotation = %s/%d, want cooling/1", state, gen)
	}
	// A new run on the rotated credential fails within the cooling window.
	fresh := addRunAtGeneration(t, s, in.ID, "fast", 1, model.RunFailed)
	s.afterProviderFailure(fresh.ID, in.ID, model.RunFailed, authFailure())

	if rotations != 1 {
		t.Fatalf("rotations = %d, want 1: a cooling failure must not rotate", rotations)
	}
	if s.paused.Load() {
		t.Fatal("a cooling failure must not pause the project")
	}
}

// While one rotation is in flight, a second failure for the same profile must
// neither start another rotation nor pause the project.
func TestInFlightRotationDoesNotPauseOrDuplicate(t *testing.T) {
	s, creds, in, run := sharedProviderTestServer(t, "fast")
	claim, err := creds.BeginCredentialFailure("fast", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !claim.Rotate {
		t.Fatalf("claim = %+v, want the single-flight rotation claim", claim)
	}
	var rotations int
	s.RotateProfileKey = func(string) (bool, error) { rotations++; return true, nil }
	s.RestartProfileHost = func(string) error { t.Fatal("an in-flight rotation restarted the host twice"); return nil }
	s.retryProviderIntentFn = func(string, string, string, string) (string, error) {
		t.Fatal("an in-flight rotation retried the intent twice")
		return "", nil
	}

	s.afterProviderFailure(run.ID, in.ID, model.RunFailed, authFailure())
	if rotations != 0 {
		t.Fatalf("rotations = %d, want 0 while another rotation is in flight", rotations)
	}
	if s.paused.Load() {
		t.Fatal("an in-flight rotation must not pause the project")
	}
	creds.AbortCredentialRotation("fast", claim.Claim)
}

// A single-credential profile has nothing to rotate to: it goes straight to the
// one-hop fallback without calling the rotation hook.
func TestNoSpareProfileFallsBackWithoutRotating(t *testing.T) {
	s, in, run := providerTestServer(t, "fast")
	s.Credentials = credentialStoreFrom(t, singleCredentialProfiles)
	var rotations, fallbacks int
	s.RotateProfileKey = func(string) (bool, error) { rotations++; return true, nil }
	s.ProfileFallback = func(source string) (string, error) {
		if source != "fast" {
			t.Fatalf("fallback source = %q", source)
		}
		return "backup", nil
	}
	s.retryProviderIntentFn = func(_, _, profileOverride, fallbackFrom string) (string, error) {
		if profileOverride == "backup" && fallbackFrom == "fast" {
			fallbacks++
		}
		return "r_fb", nil
	}

	s.afterProviderFailure(run.ID, in.ID, model.RunFailed, authFailure())
	if rotations != 0 {
		t.Fatalf("rotations = %d, want 0 for a single-credential profile", rotations)
	}
	if fallbacks != 1 {
		t.Fatalf("fallbacks = %d, want exactly 1", fallbacks)
	}
	if s.paused.Load() {
		t.Fatal("a successful fallback must not pause the project")
	}
}

// A pool whose every credential failed in the episode reaches the one-hop
// fallback exactly once; it must not keep rotating or pause when a fallback is
// configured.
func TestExhaustedPoolFallsBackExactlyOnce(t *testing.T) {
	s, creds, in, run := sharedProviderTestServer(t, "fast")
	creds.SetCredentialCooldown(0) // reach exhaustion without waiting ten minutes
	var rotations, fallbackStarts int
	s.RotateProfileKey = func(string) (bool, error) { rotations++; return true, nil }
	s.RestartProfileHost = func(string) error { return nil }
	s.ProfileFallback = func(string) (string, error) { return "backup", nil }
	s.retryProviderIntentFn = func(_, _, profileOverride, _ string) (string, error) {
		if profileOverride != "" {
			fallbackStarts++
		}
		return "r_next", nil
	}

	s.afterProviderFailure(run.ID, in.ID, model.RunFailed, authFailure()) // generation 0 -> 1
	if gen := creds.CredentialGeneration("fast"); gen != 1 {
		t.Fatalf("generation after rotation = %d, want 1", gen)
	}
	second := addRunAtGeneration(t, s, in.ID, "fast", 1, model.RunFailed)
	s.afterProviderFailure(second.ID, in.ID, model.RunFailed, authFailure()) // both keys tried

	if rotations != 1 {
		t.Fatalf("rotations = %d, want 1 (the pool is exhausted)", rotations)
	}
	if fallbackStarts != 1 {
		t.Fatalf("fallback starts = %d, want exactly 1", fallbackStarts)
	}
	if s.paused.Load() {
		t.Fatal("an exhausted pool with a configured fallback must not pause")
	}
}

// The same exhausted pool without a configured fallback is the point where D67
// pauses the project: no credential and nowhere to switch.
func TestExhaustedPoolWithoutFallbackPauses(t *testing.T) {
	s, creds, in, run := sharedProviderTestServer(t, "fast")
	creds.SetCredentialCooldown(0)
	var rotations int
	s.RotateProfileKey = func(string) (bool, error) { rotations++; return true, nil }
	s.RestartProfileHost = func(string) error { return nil }
	s.retryProviderIntentFn = func(string, string, string, string) (string, error) { return "r_next", nil }

	s.afterProviderFailure(run.ID, in.ID, model.RunFailed, authFailure())
	second := addRunAtGeneration(t, s, in.ID, "fast", 1, model.RunFailed)
	s.afterProviderFailure(second.ID, in.ID, model.RunFailed, authFailure())

	if rotations != 1 {
		t.Fatalf("rotations = %d, want 1", rotations)
	}
	if !s.paused.Load() {
		t.Fatal("an exhausted pool without a fallback must pause the project")
	}
}

// 429/rate is not a dead credential: the shared owner must not even record a
// failed generation, let alone rotate or pause.
func TestRateLimitNeverRotatesWithSharedOwner(t *testing.T) {
	s, creds, in, run := sharedProviderTestServer(t, "fast")
	var rotations int
	s.RotateProfileKey = func(string) (bool, error) { rotations++; return true, nil }
	s.RestartProfileHost = func(string) error { t.Fatal("rate limit restarted a host"); return nil }
	s.afterProviderFailure(run.ID, in.ID, model.RunFailed, executor.Result{
		IsError: true, StopReason: "error", Output: "API error (429): rate limit reached",
	})
	if rotations != 0 {
		t.Fatalf("rotations = %d, want 0", rotations)
	}
	if state, gen := creds.CredentialState("fast"); state != settings.CredentialReady || gen != 0 {
		t.Fatalf("shared state = %s/%d, want untouched ready/0", state, gen)
	}
	if s.paused.Load() {
		t.Fatal("a plain rate limit must not pause the project")
	}
}

// Dispatch freezes the shared generation on the run, and a rotation performed
// elsewhere advances it for the next dispatch.
func TestDispatchFreezesSharedCredentialGeneration(t *testing.T) {
	s, p := profileServer(t, testProfiles()...)
	if err := p.SetNodeBinding([]string{"local"}, "local"); err != nil {
		t.Fatal(err)
	}
	creds := credentialStoreFrom(t, sharedCredentialProfiles)
	s.Credentials = creds
	s.Exec = &recordExec{}

	first, err := p.Store.CreateIntent(model.Intent{Title: "generation", Profile: "fast"})
	if err != nil {
		t.Fatal(err)
	}
	firstRun, err := s.StartRun(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, p, firstRun)
	if run, _ := p.Store.GetRun(firstRun); run.CredentialGeneration != 0 {
		t.Fatalf("first run generation = %d, want 0", run.CredentialGeneration)
	}

	// Another project (or this one) rotates the shared profile: the generation
	// is instance-wide, so the next dispatch must freeze the new value.
	rotation, err := creds.BeginCredentialFailure("fast", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := creds.CompleteCredentialRotation("fast", rotation.Claim); !ok {
		t.Fatal("rotation lost ownership")
	}

	second, err := p.Store.CreateIntent(model.Intent{Title: "generation 2", Profile: "fast"})
	if err != nil {
		t.Fatal(err)
	}
	secondRun, err := s.StartRun(second.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, p, secondRun)
	run, _ := p.Store.GetRun(secondRun)
	if run.CredentialGeneration != 1 {
		t.Fatalf("second run generation = %d, want the rotated generation 1", run.CredentialGeneration)
	}
}

// Two projects share one profile. A rotation in project A is visible to project
// B: B's old-generation failure is stale (no second rotation, no pause), and a
// B failure on the new generation is cooling (no rotation either).
func TestCredentialGenerationSharedAcrossProjects(t *testing.T) {
	a, creds, inA, runA := sharedProviderTestServer(t, "fast")
	var rotations int
	a.RotateProfileKey = func(string) (bool, error) { rotations++; return true, nil }
	a.RestartProfileHost = func(string) error { return nil }
	a.retryProviderIntentFn = func(string, string, string, string) (string, error) { return "r_a", nil }
	a.afterProviderFailure(runA.ID, inA.ID, model.RunFailed, authFailure())
	if gen := creds.CredentialGeneration("fast"); gen != 1 {
		t.Fatalf("generation after A's rotation = %d, want 1", gen)
	}

	// Project B still has a run from before the rotation.
	b, inB, runB := providerTestServer(t, "fast")
	b.Credentials = creds
	setRunGeneration(t, b, runB.ID, 0)
	b.RotateProfileKey = func(string) (bool, error) { rotations++; return true, nil }
	b.RestartProfileHost = func(string) error { t.Fatal("stale project rotated a host"); return nil }
	b.afterProviderFailure(runB.ID, inB.ID, model.RunFailed, authFailure())
	if rotations != 1 {
		t.Fatalf("rotations = %d, want 1 shared across projects", rotations)
	}
	if b.paused.Load() {
		t.Fatal("project B paused on a stale generation")
	}

	// B's next failure is on the new generation, inside the shared cooling
	// window: still no rotation and no pause.
	cooling := addRunAtGeneration(t, b, inB.ID, "fast", 1, model.RunFailed)
	b.afterProviderFailure(cooling.ID, inB.ID, model.RunFailed, authFailure())
	if rotations != 1 {
		t.Fatalf("rotations = %d, want the cooling window to be shared across projects", rotations)
	}
	if b.paused.Load() {
		t.Fatal("project B paused during the shared cooling window")
	}
}

// A successful run of the profile ends its failure episode: the next failure
// starts a fresh episode and may rotate again.
func TestSuccessfulRunResetsCredentialEpisode(t *testing.T) {
	s, creds, in, run := sharedProviderTestServer(t, "fast")
	creds.SetCredentialCooldown(0)
	var rotations int
	s.RotateProfileKey = func(string) (bool, error) { rotations++; return true, nil }
	s.RestartProfileHost = func(string) error { return nil }
	s.retryProviderIntentFn = func(string, string, string, string) (string, error) { return "r_next", nil }

	s.afterProviderFailure(run.ID, in.ID, model.RunFailed, authFailure())
	second := addRunAtGeneration(t, s, in.ID, "fast", 1, model.RunFailed)
	s.afterProviderFailure(second.ID, in.ID, model.RunFailed, authFailure())
	if state, _ := creds.CredentialState("fast"); state != settings.CredentialExhausted {
		t.Fatalf("state after both keys failed = %s, want exhausted", state)
	}

	done := addRunAtGeneration(t, s, in.ID, "fast", 1, model.RunDone)
	s.afterProviderFailure(done.ID, in.ID, model.RunDone, executor.Result{ExitCode: 0})
	if state, gen := creds.CredentialState("fast"); state != settings.CredentialReady || gen != 1 {
		t.Fatalf("state after a successful run = %s/%d, want ready/1", state, gen)
	}

	third := addRunAtGeneration(t, s, in.ID, "fast", 1, model.RunFailed)
	s.afterProviderFailure(third.ID, in.ID, model.RunFailed, authFailure())
	if rotations != 2 {
		t.Fatalf("rotations = %d, want a fresh episode to rotate again (2)", rotations)
	}
}
