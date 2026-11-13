package settings

// The shared credential owner's state machine: generation accounting, stale and
// duplicate failures, the single-flight rotation claim, cooling/no_spare/
// exhausted distinctions, and the success reset. These are the non-sensitive
// facts every project's api.Server needs; the actual pool and cursor stay in
// profiles.go and are tested there.

import (
	"strings"
	"testing"
	"time"
)

const credentialProfiles = `llm:
    active_profile: pooled
    providers:
        - id: pooled
          provider: openai
          base_url: http://pooled:8080
          api_key: pooled-primary
          api_keys:
              - pooled-spare
          model: pooled-model
          tier: worker
        - id: single
          provider: openai
          base_url: http://single:8080
          api_key: single-primary
          model: single-model
          tier: worker
`

func credentialStore(t *testing.T) *Store {
	t.Helper()
	s, _ := loadProfiles(t, credentialProfiles)
	return s
}

func TestCredentialGenerationIncrementsOnRotation(t *testing.T) {
	s := credentialStore(t)
	if got := s.CredentialGeneration("pooled"); got != 0 {
		t.Fatalf("initial generation = %d, want 0", got)
	}

	plan, err := s.BeginCredentialFailure("pooled", 0)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Stale || !plan.Rotate || plan.State != CredentialRotating {
		t.Fatalf("first failure plan = %+v, want a claimed rotation", plan)
	}
	if plan.PoolSize != 2 || plan.Attempted != 1 {
		t.Fatalf("pool/attempted = %d/%d, want 2/1", plan.PoolSize, plan.Attempted)
	}

	// A second failure of the same generation while the rotation is in flight is
	// a duplicate: it must not claim a second rotation.
	if again, err := s.BeginCredentialFailure("pooled", 0); err != nil || again.Rotate || again.State != CredentialRotating {
		t.Fatalf("in-flight duplicate = %+v err=%v, want rotating without a claim", again, err)
	}

	if gen := completeCredentialForTest(t, s, "pooled"); gen != 1 {
		t.Fatalf("generation after rotation = %d, want 1", gen)
	}
	if state, gen := s.CredentialState("pooled"); state != CredentialCooling || gen != 1 {
		t.Fatalf("state after rotation = %s/%d, want cooling/1", state, gen)
	}
}

// A failure from a run dispatched before the rotation is stale: it must not
// claim another rotation even though it reports the same profile.
func TestStaleGenerationFailureIsNotRotated(t *testing.T) {
	s := credentialStore(t)
	if _, err := s.BeginCredentialFailure("pooled", 0); err != nil {
		t.Fatal(err)
	}
	completeCredentialForTest(t, s, "pooled")

	stale, err := s.BeginCredentialFailure("pooled", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !stale.Stale || stale.Rotate {
		t.Fatalf("old-generation failure = %+v, want stale without a rotation", stale)
	}
	if stale.Generation != 1 || stale.RunGeneration != 0 {
		t.Fatalf("stale generations = %d/%d, want 1/0", stale.Generation, stale.RunGeneration)
	}
}

// A failure inside the cooling window is not proof the pool is dead: no rotation,
// no exhaustion.
func TestCoolingWindowSuppressesRotation(t *testing.T) {
	s := credentialStore(t)
	if _, err := s.BeginCredentialFailure("pooled", 0); err != nil {
		t.Fatal(err)
	}
	completeCredentialForTest(t, s, "pooled")

	plan, err := s.BeginCredentialFailure("pooled", 1)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Rotate || plan.State != CredentialCooling {
		t.Fatalf("failure in cooling window = %+v, want cooling without a rotation", plan)
	}
	if plan.Attempted != 2 {
		t.Fatalf("attempted = %d, want 2", plan.Attempted)
	}
}

// After the cooldown passes, every pool entry having failed in the episode is
// "exhausted"; a single-credential profile is "no spare" without ever rotating.
func TestNoSpareAndExhaustedAreDistinct(t *testing.T) {
	s := credentialStore(t)
	s.SetCredentialCooldown(0) // exercise the post-cooling transitions now

	// no_spare: one credential, nothing to rotate to.
	single, err := s.BeginCredentialFailure("single", 0)
	if err != nil {
		t.Fatal(err)
	}
	if single.Rotate || single.State != CredentialNoSpare {
		t.Fatalf("single-credential plan = %+v, want no_spare", single)
	}

	// exhausted: two credentials, both generations failed.
	first, err := s.BeginCredentialFailure("pooled", 0)
	if err != nil || !first.Rotate {
		t.Fatalf("first pooled failure = %+v err=%v, want a rotation", first, err)
	}
	completeCredentialForTest(t, s, "pooled")
	second, err := s.BeginCredentialFailure("pooled", 1)
	if err != nil {
		t.Fatal(err)
	}
	if second.Rotate || second.State != CredentialExhausted {
		t.Fatalf("second pooled failure = %+v, want exhausted", second)
	}
	if second.Attempted != 2 || second.PoolSize != 2 {
		t.Fatalf("attempted/pool = %d/%d, want 2/2", second.Attempted, second.PoolSize)
	}
}

// Adding a credential is the operator's fix for no_spare/exhausted: the stored
// terminal state must not keep the profile stuck.
func TestAddingCredentialReopensEpisode(t *testing.T) {
	s := credentialStore(t)
	if plan, err := s.BeginCredentialFailure("single", 0); err != nil || plan.State != CredentialNoSpare {
		t.Fatalf("single plan = %+v err=%v, want no_spare", plan, err)
	}
	if _, err := s.SaveProfile(Profile{
		ID: "single", Provider: "openai", BaseURL: "http://single:8080",
		Model: "single-model", Tier: "worker",
		APIKeys: []string{"single-spare"},
	}); err != nil {
		t.Fatal(err)
	}
	plan, err := s.BeginCredentialFailure("single", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Rotate || plan.State != CredentialRotating {
		t.Fatalf("after adding a key = %+v, want a claimed rotation", plan)
	}
}

// A successful run ends the episode: the next failure may rotate again instead
// of inheriting "every key was tried" from an outage that is over.
func TestSuccessResetsEpisodeButKeepsGeneration(t *testing.T) {
	s := credentialStore(t)
	s.SetCredentialCooldown(0)
	if _, err := s.BeginCredentialFailure("pooled", 0); err != nil {
		t.Fatal(err)
	}
	completeCredentialForTest(t, s, "pooled")
	if plan, err := s.BeginCredentialFailure("pooled", 1); err != nil || plan.State != CredentialExhausted {
		t.Fatalf("second plan = %+v err=%v, want exhausted", plan, err)
	}

	s.NoteCredentialSuccess("pooled", 1)
	if state, gen := s.CredentialState("pooled"); state != CredentialReady || gen != 1 {
		t.Fatalf("after success = %s/%d, want ready with generation kept at 1", state, gen)
	}
	plan, err := s.BeginCredentialFailure("pooled", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Rotate || plan.Attempted != 1 {
		t.Fatalf("failure after reset = %+v, want a fresh episode (one attempted generation)", plan)
	}
}

// An expired cooling window returns to ready exactly once and without a timer.
func TestCoolingExpiresBackToReady(t *testing.T) {
	s := credentialStore(t)
	s.SetCredentialCooldown(10 * time.Millisecond)
	if _, err := s.BeginCredentialFailure("pooled", 0); err != nil {
		t.Fatal(err)
	}
	completeCredentialForTest(t, s, "pooled")
	if state, _ := s.CredentialState("pooled"); state != CredentialCooling {
		t.Fatalf("state = %s, want cooling", state)
	}
	time.Sleep(20 * time.Millisecond)
	if state, _ := s.CredentialState("pooled"); state != CredentialReady {
		t.Fatalf("state after window = %s, want ready", state)
	}
}

func TestCredentialFailureRejectsUnknownProfile(t *testing.T) {
	s := credentialStore(t)
	if _, err := s.BeginCredentialFailure("missing", 0); err == nil || !strings.Contains(err.Error(), "模型配置") {
		t.Fatalf("unknown profile error = %v", err)
	}
	if got := s.CredentialGeneration("missing"); got != 0 {
		t.Fatalf("unknown profile generation = %d, want 0", got)
	}
}

func completeCredentialForTest(t *testing.T, s *Store, profile string) uint64 {
	t.Helper()
	s.creds.mu.Lock()
	claim := s.creds.episodes[profile].claim
	s.creds.mu.Unlock()
	generation, ok := s.CompleteCredentialRotation(profile, claim)
	if !ok {
		t.Fatal("fixture rotation lost ownership")
	}
	return generation
}
