package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/project"
)

// The console edits a project's review policy through PUT /api/projects/{id}.
// The call must touch only verify_auto: null restores "inherit the instance
// --verify-auto", and the response reports the value runs actually use now
// (D100).
func TestUpdateProjectVerifyAuto(t *testing.T) {
	m, err := project.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := m.Create("Acme", "acme.example")
	if err != nil {
		t.Fatal(err)
	}
	h := &hub{mgr: m, cfg: hubConfig{VerifyAuto: true}, kits: map[string]*kit{}}

	put := func(body string) (int, map[string]any) {
		r := httptest.NewRequest(http.MethodPut, "/api/projects/"+p.ID, strings.NewReader(body))
		w := httptest.NewRecorder()
		h.updateProject(w, r, p.ID)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}

	// A new project inherits the instance default (here: on).
	info := h.info(m.Get(p.ID))
	if info.VerifyAuto != nil || !info.VerifyAutoEffective {
		t.Fatalf("fresh project = %+v, want inherit + effective on", info)
	}

	code, out := put(`{"verify_auto":false}`)
	if code != http.StatusOK {
		t.Fatalf("override = %d %v", code, out)
	}
	if out["verify_auto"] != false || out["verify_auto_effective"] != false {
		t.Fatalf("override response = %v, want false/false", out)
	}
	if v, ok := m.Get(p.ID).VerifyAutoOverride(); !ok || v {
		t.Fatalf("manifest override = %v, %v; want false, true", v, ok)
	}
	if got := m.Get(p.ID).ScopeText(); got != "acme.example" {
		t.Fatalf("a review-only save cleared the scope: %q", got)
	}

	// null goes back to inherit, which is on for this hub.
	code, out = put(`{"verify_auto":null}`)
	if code != http.StatusOK {
		t.Fatalf("clear = %d %v", code, out)
	}
	if out["verify_auto"] != nil || out["verify_auto_effective"] != true {
		t.Fatalf("cleared response = %v, want null/true", out)
	}
	if _, ok := m.Get(p.ID).VerifyAutoOverride(); ok {
		t.Fatal("cleared override is still in the manifest")
	}

	// A string is a bad form value, not a boolean.
	if code, _ := put(`{"verify_auto":"yes"}`); code != http.StatusBadRequest {
		t.Fatalf("string verify_auto = %d, want 400", code)
	}
	if got := m.Get(p.ID).ScopeText(); got != "acme.example" {
		t.Fatalf("scope changed on a rejected save: %q", got)
	}
}
