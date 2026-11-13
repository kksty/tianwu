package settings

// max_concurrency is Tianwu capacity metadata stored next to the harness
// profile: it round-trips through the operator's file, never reaches the
// per-host configuration, and never moves the binding revision (changing a cap
// must not restart a host or abandon a long session).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const concurrencyProfiles = `llm:
    active_profile: fast
    providers:
        - id: fast
          provider: openai
          base_url: http://fast:8080
          api_key: fast-secret
          model: fast-model
          max_concurrency: 2
        - id: slow
          provider: openai
          base_url: http://slow:8080
          api_key: slow-secret
          model: slow-model
`

func TestProfileMaxConcurrencyRoundTrips(t *testing.T) {
	s, harness := loadProfiles(t, concurrencyProfiles)
	cat, err := s.Profiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Profiles) != 2 || cat.Profiles[0].MaxConcurrency != 2 || cat.Profiles[1].MaxConcurrency != 0 {
		t.Fatalf("max_concurrency views = %+v", cat.Profiles)
	}
	if _, err := s.SaveProfile(Profile{
		ID: "slow", Provider: "openai", BaseURL: "http://slow:8080",
		Model: "slow-model", MaxConcurrency: 3,
	}); err != nil {
		t.Fatal(err)
	}
	saved, err := s.SavedProfile("slow")
	if err != nil {
		t.Fatal(err)
	}
	if saved.MaxConcurrency != 3 {
		t.Fatalf("saved max_concurrency = %d", saved.MaxConcurrency)
	}
	data, err := os.ReadFile(harness)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "max_concurrency: 3") {
		t.Fatalf("operator file lost max_concurrency:\n%s", data)
	}

	// Clearing the cap removes the key instead of leaving a stale number.
	if _, err := s.SaveProfile(Profile{
		ID: "slow", Provider: "openai", BaseURL: "http://slow:8080", Model: "slow-model",
	}); err != nil {
		t.Fatal(err)
	}
	if saved, _ := s.SavedProfile("slow"); saved.MaxConcurrency != 0 {
		t.Fatalf("cleared cap = %d", saved.MaxConcurrency)
	}
	data, _ = os.ReadFile(harness)
	if strings.Contains(string(data), "max_concurrency: 3") {
		t.Fatalf("cleared cap still in the operator file:\n%s", data)
	}
}

func TestMaxConcurrencyIsStrippedFromHostConfig(t *testing.T) {
	s, harness := loadProfiles(t, concurrencyProfiles)
	dest := filepath.Join(t.TempDir(), "host.yaml")
	if err := s.WriteProfileConfig("fast", dest); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "max_concurrency") {
		t.Fatalf("host config carries Tianwu capacity metadata:\n%s", data)
	}
	if !strings.Contains(string(data), "model: fast-model") {
		t.Fatalf("host config lost harness fields:\n%s", data)
	}
	// The operator's file keeps it.
	if src, err := os.ReadFile(harness); err != nil || !strings.Contains(string(src), "max_concurrency: 2") {
		t.Fatal("the operator's file must keep max_concurrency")
	}
}

func TestMaxConcurrencyChangeDoesNotChangeRevision(t *testing.T) {
	s, _ := loadProfiles(t, concurrencyProfiles)
	before, err := s.SavedProfile("fast")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveProfile(Profile{
		ID: "fast", Provider: "openai", BaseURL: "http://fast:8080",
		Model: "fast-model", MaxConcurrency: 9,
	}); err != nil {
		t.Fatal(err)
	}
	after, err := s.SavedProfile("fast")
	if err != nil {
		t.Fatal(err)
	}
	if before.Revision() != after.Revision() {
		t.Fatalf("revision moved on a capacity edit: %s -> %s", before.Revision(), after.Revision())
	}
	// Rotation's view carries the cap too (the console shows it everywhere).
	view, _, err := s.RotateProfileKey("fast")
	if err != nil {
		t.Fatal(err)
	}
	if view.MaxConcurrency != 9 {
		t.Fatalf("rotated view max_concurrency = %d", view.MaxConcurrency)
	}
}

func TestMaxConcurrencyValidation(t *testing.T) {
	s, _ := loadProfiles(t, concurrencyProfiles)
	if _, err := s.SaveProfile(Profile{
		ID: "fast", Provider: "openai", BaseURL: "http://fast:8080",
		Model: "fast-model", MaxConcurrency: -1,
	}); err == nil || !strings.Contains(err.Error(), "max_concurrency") {
		t.Fatalf("negative cap = %v, want a validation error", err)
	}
	// A stored negative value is a configuration error, not silently clamped.
	bad, _ := loadProfiles(t, `llm:
    providers:
        - {id: a, base_url: http://a:1, model: m, api_key: k, max_concurrency: -2}
`)
	if _, err := bad.Profiles(); err == nil {
		t.Fatal("a stored negative max_concurrency must be reported")
	}
}
