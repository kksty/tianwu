package project

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func newPeerProject(t *testing.T) (*Manager, *Project) {
	t.Helper()
	m, err := Open(filepath.Join(t.TempDir(), "projects"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := m.Create("peer policy", "")
	if err != nil {
		t.Fatal(err)
	}
	return m, p
}

// An old project (manifest without peer_policy) keeps the pre-policy behaviour:
// every participant may message every other one.
func TestPeerPolicyDefaultsToOpen(t *testing.T) {
	_, p := newPeerProject(t)
	if got := p.PeerPolicyView().Mode; got != PeerPolicyOpen {
		t.Fatalf("default mode = %q", got)
	}
	if !p.PeerAllowed("pt_in_a", "pt_in_b") || !p.PeerAllowed("pt_in_a", "commander") {
		t.Fatal("default policy must authorize every direction")
	}
}

// Allowlist mode is directional and persisted: the pair survives a restart
// because it is part of the project manifest, not process memory.
func TestPeerPolicyAllowlistPersistsAndIsDirectional(t *testing.T) {
	m, p := newPeerProject(t)
	pol, err := p.SetPeerPolicy(PeerPolicy{
		Mode: PeerPolicyAllowlist,
		Allow: []PeerRule{
			{From: "pt_in_a", To: "pt_in_b"},
			{From: "pt_in_a", To: "pt_in_b"}, // duplicate is collapsed
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if pol.Mode != PeerPolicyAllowlist || len(pol.Allow) != 1 {
		t.Fatalf("normalised policy = %+v", pol)
	}
	if !p.PeerAllowed("pt_in_a", "pt_in_b") {
		t.Fatal("listed direction denied")
	}
	if p.PeerAllowed("pt_in_b", "pt_in_a") {
		t.Fatal("reverse direction must not be implied")
	}
	if p.PeerAllowed("pt_in_a", "pt_in_c") {
		t.Fatal("unlisted direction allowed")
	}
	// Reload from disk: the manifest is the source of truth.
	data, err := os.ReadFile(filepath.Join(p.Dir, ManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(data) {
		t.Fatalf("manifest is not JSON: %s", data)
	}
	reopened, err := Open(m.Root)
	if err != nil {
		t.Fatal(err)
	}
	again := reopened.Get(p.ID)
	if again == nil {
		t.Fatal("project missing after reload")
	}
	if again.PeerPolicyView().Mode != PeerPolicyAllowlist || !again.PeerAllowed("pt_in_a", "pt_in_b") {
		t.Fatalf("policy lost on reload: %+v", again.PeerPolicyView())
	}
}

// An explicit open mode is legal (the operator may switch back) and ignores the
// stale allowlist.
func TestPeerPolicyOpenModeIgnoresAllowlist(t *testing.T) {
	_, p := newPeerProject(t)
	if _, err := p.SetPeerPolicy(PeerPolicy{
		Mode:  PeerPolicyAllowlist,
		Allow: []PeerRule{{From: "pt_in_a", To: "pt_in_b"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.SetPeerPolicy(PeerPolicy{Mode: PeerPolicyOpen}); err != nil {
		t.Fatal(err)
	}
	if !p.PeerAllowed("pt_in_c", "pt_in_d") {
		t.Fatal("open mode must authorize every direction")
	}
}

// A typo must not silently open or close the project's conversation.
func TestPeerPolicyRejectsInvalidRules(t *testing.T) {
	_, p := newPeerProject(t)
	for name, policy := range map[string]PeerPolicy{
		"unknown mode": {Mode: "sometimes"},
		"empty from":   {Mode: PeerPolicyAllowlist, Allow: []PeerRule{{To: "pt_in_b"}}},
		"empty to":     {Mode: PeerPolicyAllowlist, Allow: []PeerRule{{From: "pt_in_a"}}},
		"self pair":    {Mode: PeerPolicyAllowlist, Allow: []PeerRule{{From: "pt_in_a", To: "pt_in_a"}}},
	} {
		if _, err := p.SetPeerPolicy(policy); err == nil {
			t.Fatalf("%s: accepted an invalid policy", name)
		}
	}
	if got := p.PeerPolicyView().Mode; got != PeerPolicyOpen {
		t.Fatalf("invalid writes changed the policy: %q", got)
	}
}
