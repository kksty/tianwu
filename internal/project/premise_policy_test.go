package project

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewProjectIsAlwaysVerifiedOnly(t *testing.T) {
	root := t.TempDir()
	m, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	p, err := m.Create("premise policy project", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := p.PremisePolicyView(); got != PremisePolicyVerifiedOnly {
		t.Fatalf("new project policy = %q, want verified_only", got)
	}
	if _, err := p.SetPremisePolicy("sometimes"); err == nil {
		t.Fatal("invalid policy accepted")
	}
	if _, err := p.SetPremisePolicy("standard"); err == nil {
		t.Fatal("removed standard mode accepted")
	}
	if got := p.PremisePolicyView(); got != PremisePolicyVerifiedOnly {
		t.Fatalf("invalid set changed the policy: %q", got)
	}
	// Re-open from disk: the single mode survives a restart.
	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Get(p.ID).PremisePolicyView(); got != PremisePolicyVerifiedOnly {
		t.Fatalf("persisted policy = %q, want verified_only", got)
	}
}

// A manifest written before the policy was unified may store standard or strict.
// It is migrated to verified_only on load and rewritten; there is no read-only
// compatibility mode left.
func TestLegacyManifestIsMigratedOnLoad(t *testing.T) {
	for _, legacy := range []string{"standard", "strict"} {
		t.Run(legacy, func(t *testing.T) {
			root := t.TempDir()
			m, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			p, err := m.Create("legacy project", "")
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(p.Dir, ManifestFile)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var manifest map[string]any
			if err := json.Unmarshal(raw, &manifest); err != nil {
				t.Fatal(err)
			}
			manifest["premise_policy"] = legacy
			legacyRaw, err := json.MarshalIndent(manifest, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, legacyRaw, 0o644); err != nil {
				t.Fatal(err)
			}

			reopened, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			migrated := reopened.Get(p.ID)
			if migrated == nil {
				t.Fatalf("project %s was not loaded", p.ID)
			}
			if got := migrated.PremisePolicyView(); got != PremisePolicyVerifiedOnly {
				t.Fatalf("legacy %s read as %q, want verified_only", legacy, got)
			}
			stored, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(stored), `"premise_policy": "verified_only"`) {
				t.Fatalf("legacy %s manifest was not migrated:\n%s", legacy, stored)
			}
		})
	}
}
