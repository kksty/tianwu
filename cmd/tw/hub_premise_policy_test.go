package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/project"
)

// The console edits a project's premise admission mode through the same
// PUT /api/projects/{id} form as the other project defaults: omitted fields
// keep their value, an invalid mode rejects the whole request before any setter
// writes, and the response reports the stored mode. The removed middle mode
// "strict" is not writable; an older manifest that still stores it reads as
// verified_only.
func TestUpdateProjectPremisePolicy(t *testing.T) {
	root := t.TempDir()
	m, err := project.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	p, err := m.Create("Acme", "acme.example")
	if err != nil {
		t.Fatal(err)
	}
	h := &hub{mgr: m, kits: map[string]*kit{}}

	put := func(hh *hub, body string) (int, map[string]any) {
		r := httptest.NewRequest(http.MethodPut, "/api/projects/"+p.ID, strings.NewReader(body))
		w := httptest.NewRecorder()
		hh.updateProject(w, r, p.ID)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}

	// Every project is verified_only; there is no default standard mode.
	if got := h.info(m.Get(p.ID)).PremisePolicy; got != project.PremisePolicyVerifiedOnly {
		t.Fatalf("fresh project premise_policy = %q, want %q", got, project.PremisePolicyVerifiedOnly)
	}

	code, out := put(h, `{"premise_policy":"verified_only"}`)
	if code != http.StatusOK {
		t.Fatalf("set verified_only = %d %v", code, out)
	}
	if out["premise_policy"] != project.PremisePolicyVerifiedOnly {
		t.Fatalf("response premise_policy = %v, want verified_only", out["premise_policy"])
	}
	if got := m.Get(p.ID).PremisePolicyView(); got != project.PremisePolicyVerifiedOnly {
		t.Fatalf("stored policy = %q, want verified_only", got)
	}
	if got := m.Get(p.ID).ScopeText(); got != "acme.example" {
		t.Fatalf("a policy-only save cleared the scope: %q", got)
	}

	// A typo or the removed standard mode is a 400.
	code, _ = put(h, `{"premise_policy":"sometimes"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("invalid policy = %d, want 400", code)
	}
	if got := m.Get(p.ID).PremisePolicyView(); got != project.PremisePolicyVerifiedOnly {
		t.Fatalf("invalid save changed the policy: %q", got)
	}

	// Validation runs before any setter: a bad policy must not let an earlier
	// field in the same request land behind a 400.
	code, _ = put(h, `{"scope":"changed.example","premise_policy":"sometimes"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("mixed invalid save = %d, want 400", code)
	}
	if got := m.Get(p.ID).ScopeText(); got != "acme.example" {
		t.Fatalf("scope changed on a rejected save: %q", got)
	}
	if got := m.Get(p.ID).PremisePolicyView(); got != project.PremisePolicyVerifiedOnly {
		t.Fatalf("policy changed on a rejected save: %q", got)
	}

	// Omitting the field keeps the policy.
	code, out = put(h, `{"scope":"acme.example"}`)
	if code != http.StatusOK || out["premise_policy"] != project.PremisePolicyVerifiedOnly {
		t.Fatalf("omitted policy = %d %v", code, out)
	}

	// The stored value survives reopening the project manager (manifest write).
	reopened, err := project.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Get(p.ID).PremisePolicyView(); got != project.PremisePolicyVerifiedOnly {
		t.Fatalf("policy after reopen = %q, want verified_only", got)
	}

	// A legacy standard/strict manifest is migrated to verified_only on load;
	// the console project view must show the migrated value.
	path := filepath.Join(p.Dir, project.ManifestFile)
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
	legacyMgr, err := project.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	legacyProject := legacyMgr.Get(p.ID)
	if legacyProject == nil {
		t.Fatalf("legacy project %s was not loaded", p.ID)
	}
	legacyHub := &hub{mgr: legacyMgr, kits: map[string]*kit{}}
	if got := legacyHub.info(legacyProject).PremisePolicy; got != project.PremisePolicyVerifiedOnly {
		t.Fatalf("legacy strict premise_policy = %q, want verified_only", got)
	}

	// Writing the removed mode afresh is a 400 and does not change the
	// migrated manifest.
	code, out = put(legacyHub, `{"premise_policy":"strict"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("set strict = %d %v, want 400", code, out)
	}
	if got := legacyProject.PremisePolicyView(); got != project.PremisePolicyVerifiedOnly {
		t.Fatalf("rejected strict save changed the effective policy: %q", got)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stored), `"premise_policy": "verified_only"`) {
		t.Fatalf("legacy manifest was not migrated:\n%s", stored)
	}
}
