package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/project"
)

// The console edits a project's concurrency cap through PUT /api/projects/{id}.
// The call must touch only max_runs: a form that saves concurrency must not
// wipe the scope it never sent, and null must restore "inherit".
func TestUpdateProjectMaxRuns(t *testing.T) {
	m, err := project.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := m.Create("Acme", "acme.example")
	if err != nil {
		t.Fatal(err)
	}
	h := &hub{mgr: m, cfg: hubConfig{MaxRuns: 3}, kits: map[string]*kit{}}

	put := func(body string) (int, map[string]any) {
		r := httptest.NewRequest(http.MethodPut, "/api/projects/"+p.ID, strings.NewReader(body))
		w := httptest.NewRecorder()
		h.updateProject(w, r, p.ID)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}

	code, out := put(`{"max_runs":1}`)
	if code != http.StatusOK {
		t.Fatalf("override = %d %v", code, out)
	}
	if out["max_runs"] != float64(1) || out["max_runs_effective"] != float64(1) {
		t.Fatalf("override response = %v", out)
	}
	if v, ok := m.Get(p.ID).MaxRunsOverride(); !ok || v != 1 {
		t.Fatalf("manifest override = %d, %v; want 1, true", v, ok)
	}
	if got := m.Get(p.ID).ScopeText(); got != "acme.example" {
		t.Fatalf("a concurrency-only save cleared the scope: %q", got)
	}

	code, out = put(`{"max_runs":null}`)
	if code != http.StatusOK {
		t.Fatalf("clear = %d %v", code, out)
	}
	if out["max_runs"] != nil || out["max_runs_effective"] != float64(3) {
		t.Fatalf("cleared override response = %v, want null/3", out)
	}
	if _, ok := m.Get(p.ID).MaxRunsOverride(); ok {
		t.Fatal("cleared override is still in the manifest")
	}

	if code, _ := put(`{"max_runs":-1}`); code != http.StatusBadRequest {
		t.Fatalf("negative max_runs = %d, want 400", code)
	}
	if got := m.Get(p.ID).ScopeText(); got != "acme.example" {
		t.Fatalf("scope changed on a rejected save: %q", got)
	}
}
