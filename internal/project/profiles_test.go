package project

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The tier defaults live in the project manifest: they are a pointer to the
// instance's profile catalogue, not a copy of it, and "" means "inherit the
// instance active profile". They must survive a restart and be clearable.
func TestProjectProfileDefaultsPersistAndClear(t *testing.T) {
	root := t.TempDir()
	m, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	p, err := m.Create("tiered", "scope")
	if err != nil {
		t.Fatal(err)
	}
	if worker, strong := p.ProfileDefaults(); worker != "" || strong != "" {
		t.Fatalf("new project has defaults %q/%q", worker, strong)
	}
	if err := m.SetProfileDefaults(p.ID, "cheap", "strong"); err != nil {
		t.Fatal(err)
	}
	if worker, strong := p.ProfileDefaults(); worker != "cheap" || strong != "strong" {
		t.Fatalf("defaults = %q/%q", worker, strong)
	}

	// A restart reads them back from project.json.
	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	again := reopened.Get(p.ID)
	if again == nil {
		t.Fatal("project missing after reopen")
	}
	if worker, strong := again.ProfileDefaults(); worker != "cheap" || strong != "strong" {
		t.Fatalf("persisted defaults = %q/%q", worker, strong)
	}
	data, err := os.ReadFile(filepath.Join(p.Dir, ManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest["worker_profile"] != "cheap" || manifest["strong_profile"] != "strong" {
		t.Fatalf("manifest = %v", manifest)
	}

	// Empty strings clear both, restoring the old inherit-active behaviour.
	if err := m.SetProfileDefaults(p.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	if worker, strong := p.ProfileDefaults(); worker != "" || strong != "" {
		t.Fatalf("cleared defaults = %q/%q", worker, strong)
	}
	if err := m.SetProfileDefaults("missing", "x", "y"); err == nil {
		t.Fatal("setting defaults on an unknown project must error")
	}
}
