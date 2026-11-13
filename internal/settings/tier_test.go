package settings

// Tier and key-pool metadata are Tianwu routing state stored next to the
// harness profile. They must round-trip through the operator's file, never
// leak into the per-host configuration the harness parses, and never move the
// binding revision (a tier edit or a key rotation must not restart a long
// commander session).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const tieredProfiles = `llm:
    active_profile: strong
    providers:
        - id: strong
          provider: openai
          base_url: http://strong:8080
          api_key: strong-secret
          model: strong-model
          tier: strong
          pricing:
              currency: USD
              input_per_mtoken: 3
              output_per_mtoken: 12
        - id: cheap
          provider: openai
          base_url: http://cheap:8080
          api_key: cheap-secret
          model: cheap-model
          tier: worker
`

func TestProfileTierRoundTripsThroughYAML(t *testing.T) {
	s, harness := loadProfiles(t, tieredProfiles)
	cat, err := s.Profiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Profiles) != 2 || cat.Profiles[0].Tier != "strong" || cat.Profiles[1].Tier != "worker" {
		t.Fatalf("tiers = %+v", cat.Profiles)
	}
	if _, err := s.SaveProfile(Profile{
		ID: "cheap", Provider: "openai", BaseURL: "http://cheap:8080",
		Model: "cheap-model", Tier: "strong",
	}); err != nil {
		t.Fatal(err)
	}
	saved, err := s.SavedProfile("cheap")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Tier != "strong" {
		t.Fatalf("saved tier = %q", saved.Tier)
	}
	data, err := os.ReadFile(harness)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "tier: strong") {
		t.Fatalf("operator file lost the tier:\n%s", data)
	}
}

func TestProfileTierIsStrippedFromHostConfig(t *testing.T) {
	s, harness := loadProfiles(t, tieredProfiles)
	dest := filepath.Join(t.TempDir(), "host.yaml")
	if err := s.WriteProfileConfig("strong", dest); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"tier", "pricing", "input_per_mtoken", "api_keys"} {
		if strings.Contains(string(data), banned) {
			t.Fatalf("host config carries Tianwu-only metadata %q:\n%s", banned, data)
		}
	}
	for _, want := range []string{"model: strong-model", "api_key: strong-secret"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("host config lost harness field %q:\n%s", want, data)
		}
	}
	if src, err := os.ReadFile(harness); err != nil || !strings.Contains(string(src), "tier: strong") {
		t.Fatal("the operator's file must keep its tier metadata")
	}
}

func TestUnknownTierIsRejected(t *testing.T) {
	s, _ := loadProfiles(t, tieredProfiles)
	_, err := s.SaveProfile(Profile{
		ID: "cheap", Provider: "openai", BaseURL: "http://cheap:8080",
		Model: "cheap-model", Tier: "smart",
	})
	if err == nil || !strings.Contains(err.Error(), "tier") {
		t.Fatalf("unknown tier = %v, want a validation error", err)
	}
	// A file already carrying an unknown tier is a configuration error too: a
	// silent read as "unset" would hide the typo.
	s2, _ := loadProfiles(t, `llm:
    providers:
        - {id: a, base_url: http://a:1, model: m, api_key: k, tier: smart}
`)
	if _, err := s2.Profiles(); err == nil {
		t.Fatal("an unknown stored tier must be reported, not silently dropped")
	}
}

func TestTierChangeDoesNotChangeRevision(t *testing.T) {
	s, _ := loadProfiles(t, tieredProfiles)
	before, err := s.SavedProfile("cheap")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveProfile(Profile{
		ID: "cheap", Provider: "openai", BaseURL: "http://cheap:8080",
		Model: "cheap-model", Tier: "strong",
	}); err != nil {
		t.Fatal(err)
	}
	after, err := s.SavedProfile("cheap")
	if err != nil {
		t.Fatal(err)
	}
	if before.Revision() != after.Revision() {
		t.Fatalf("revision moved on a tier edit: %s -> %s", before.Revision(), after.Revision())
	}
}

func TestKeyPoolRotatesWithoutChangingRevision(t *testing.T) {
	s, harness := loadProfiles(t, `llm:
    active_profile: pooled
    providers:
        - id: pooled
          provider: openai
          base_url: http://pooled:8080
          api_key: first-secret
          model: pooled-model
          api_keys: [second-secret, third-secret]
`)
	before, err := s.SavedProfile("pooled")
	if err != nil {
		t.Fatal(err)
	}
	view, rotated, err := s.RotateProfileKey("pooled")
	if err != nil || !rotated {
		t.Fatalf("rotate = %v rotated=%v", err, rotated)
	}
	if view.APIKeyCount != 3 || view.APIKeySet != true {
		t.Fatalf("view = %+v", view)
	}
	if view.Revision != before.Revision() {
		t.Fatalf("a rotation moved the binding revision: %s -> %s", before.Revision(), view.Revision)
	}
	if count, index := s.CurrentProfileKey("pooled"); count != 3 || index != 1 {
		t.Fatalf("cursor = %d/%d, want 1/3", index, count)
	}

	// The materialised host config carries the selected credential, not the
	// pool: the harness reads exactly one api_key.
	dest := filepath.Join(t.TempDir(), "host.yaml")
	writeHostConfig := func() string {
		t.Helper()
		if err := s.WriteProfileConfig("pooled", dest); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(dest)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	data := writeHostConfig()
	if !strings.Contains(data, "api_key: second-secret") {
		t.Fatalf("host config did not use the rotated key:\n%s", data)
	}
	for _, banned := range []string{"api_keys", "third-secret", "first-secret"} {
		if strings.Contains(data, banned) {
			t.Fatalf("host config leaked pool material %q:\n%s", banned, data)
		}
	}

	// Rotation walks the pool in order and wraps back to the operator's primary
	// api_key, so a rotation never strands the profile on a stale credential.
	if _, rotated, err := s.RotateProfileKey("pooled"); err != nil || !rotated {
		t.Fatalf("second rotate = %v rotated=%v", err, rotated)
	}
	if data := writeHostConfig(); !strings.Contains(data, "api_key: third-secret") {
		t.Fatalf("rotation did not advance:\n%s", data)
	}
	if _, rotated, err := s.RotateProfileKey("pooled"); err != nil || !rotated {
		t.Fatalf("third rotate = %v rotated=%v", err, rotated)
	}
	if data := writeHostConfig(); !strings.Contains(data, "api_key: first-secret") {
		t.Fatalf("rotation did not wrap:\n%s", data)
	}
	if src, err := os.ReadFile(harness); err != nil || !strings.Contains(string(src), "api_keys") {
		t.Fatal("the operator's file must keep its key pool")
	}

	// The host is launched with an environment that overrides the file, so the
	// env contract must carry the same rotated credential; otherwise a rotation
	// would be silently undone by the primary api_key in CYBER_API_KEY. Rotate
	// once more (cursor back on a pool entry, not the primary) to see it.
	if _, rotated, err := s.RotateProfileKey("pooled"); err != nil || !rotated {
		t.Fatalf("rotate for env = %v rotated=%v", err, rotated)
	}
	env, err := s.ProfileEnv("pooled")
	if err != nil {
		t.Fatal(err)
	}
	want := "CYBER_API_KEY=second-secret"
	found := false
	for _, kv := range env {
		if kv == want {
			found = true
		}
		if strings.HasPrefix(kv, "CYBER_API_KEY=") && kv != want {
			t.Fatalf("env carries a stale credential: %s", kv)
		}
	}
	if !found {
		t.Fatalf("env does not carry the current rotated key: %v", env)
	}
}

func TestSingleKeyRotationIsNoOp(t *testing.T) {
	s, _ := loadProfiles(t, tieredProfiles)
	if _, rotated, err := s.RotateProfileKey("cheap"); err != nil || rotated {
		t.Fatalf("single-key rotate = %v rotated=%v, want a no-op", err, rotated)
	}
	if _, _, err := s.RotateProfileKey("missing"); err == nil {
		t.Fatal("rotating an unknown profile must report an error")
	}
}
