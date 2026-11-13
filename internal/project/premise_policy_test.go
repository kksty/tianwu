package project

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPremisePolicyDefaultsToStandardAndPersistsVerifiedOnly(t *testing.T) {
	root := t.TempDir()
	m, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	p, err := m.Create("premise policy project", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := p.PremisePolicyView(); got != PremisePolicyStandard {
		t.Fatalf("default policy = %q, want standard", got)
	}
	if _, err := p.SetPremisePolicy("sometimes"); err == nil {
		t.Fatal("invalid policy accepted")
	}
	if got := p.PremisePolicyView(); got != PremisePolicyStandard {
		t.Fatalf("invalid set changed the policy: %q", got)
	}
	if _, err := p.SetPremisePolicy(PremisePolicyVerifiedOnly); err != nil {
		t.Fatal(err)
	}
	// Re-open from disk: the operator's mode survives a restart.
	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Get(p.ID).PremisePolicyView(); got != PremisePolicyVerifiedOnly {
		t.Fatalf("persisted policy = %q, want verified_only", got)
	}
}

// A manifest written before the middle mode was removed still stores "strict".
// It must read as verified_only — the superset of strict's guarantee — instead
// of silently weakening an existing operator constraint to standard. Writing
// "strict" afresh is an error, and the rejected write leaves the manifest
// untouched: only the next explicit settings save persists verified_only.
func TestLegacyStrictManifestReadsAsVerifiedOnlyAndRejectsWrites(t *testing.T) {
	root := t.TempDir()
	m, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	p, err := m.Create("legacy strict project", "")
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a manifest an older binary wrote.
	path := filepath.Join(p.Dir, ManifestFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest["premise_policy"] = "strict"
	legacy, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, legacy, 0o644); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	legacyProject := reopened.Get(p.ID)
	if legacyProject == nil {
		t.Fatalf("project %s was not loaded", p.ID)
	}
	if got := legacyProject.PremisePolicyView(); got != PremisePolicyVerifiedOnly {
		t.Fatalf("legacy strict read as %q, want verified_only", got)
	}

	_, err = legacyProject.SetPremisePolicy("strict")
	if err == nil {
		t.Fatal("writing the removed strict mode was accepted")
	}
	if !strings.Contains(err.Error(), "strict 模式已移除") || !strings.Contains(err.Error(), "verified_only") {
		t.Fatalf("removal error does not explain the migration: %v", err)
	}
	// The failed write did not rewrite the manifest, and the effective reading
	// is still the safe one.
	if got := legacyProject.PremisePolicyView(); got != PremisePolicyVerifiedOnly {
		t.Fatalf("rejected strict write changed the effective policy: %q", got)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stored), `"premise_policy": "strict"`) {
		t.Fatalf("rejected strict write rewrote the manifest:\n%s", stored)
	}
}
