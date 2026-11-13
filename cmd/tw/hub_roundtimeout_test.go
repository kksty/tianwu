package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/project"
)

// The console edits the per-project commander round wall clock through the same
// PUT as concurrency. A blank value clears the override (inherit the instance
// --round-timeout), "0" is a deliberate unlimited, and a typo is a 400 that
// leaves the manifest alone (D91).
func TestUpdateProjectRoundTimeout(t *testing.T) {
	m, err := project.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := m.Create("Acme", "acme.example")
	if err != nil {
		t.Fatal(err)
	}
	h := &hub{mgr: m, cfg: hubConfig{RoundTimeout: 15 * time.Minute}, kits: map[string]*kit{}}

	put := func(body string) (int, map[string]any) {
		r := httptest.NewRequest(http.MethodPut, "/api/projects/"+p.ID, strings.NewReader(body))
		w := httptest.NewRecorder()
		h.updateProject(w, r, p.ID)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}

	code, out := put(`{"round_timeout":"10m"}`)
	if code != http.StatusOK {
		t.Fatalf("override = %d %v", code, out)
	}
	if out["round_timeout"] != "10m" {
		t.Fatalf("override response = %v", out)
	}
	if raw := m.Get(p.ID).RoundTimeoutRaw(); raw != "10m" {
		t.Fatalf("manifest override = %q, want 10m", raw)
	}
	// The decider uses the override, not the instance default.
	if got := h.commanderRoundTimeout(m.Get(p.ID)); got != 10*time.Minute {
		t.Fatalf("effective round timeout = %s, want 10m", got)
	}
	if got := m.Get(p.ID).ScopeText(); got != "acme.example" {
		t.Fatalf("a round-timeout-only save cleared the scope: %q", got)
	}

	code, out = put(`{"round_timeout":""}`)
	if code != http.StatusOK {
		t.Fatalf("clear = %d %v", code, out)
	}
	if out["round_timeout"] != nil {
		t.Fatalf("cleared override response = %v, want omitted", out)
	}
	if raw := m.Get(p.ID).RoundTimeoutRaw(); raw != "" {
		t.Fatalf("cleared override = %q, want inherit", raw)
	}
	if got := h.commanderRoundTimeout(m.Get(p.ID)); got != 15*time.Minute {
		t.Fatalf("after clear the effective value = %s, want the instance 15m", got)
	}

	code, _ = put(`{"round_timeout":"ten minutes"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("malformed override = %d, want 400", code)
	}
	if raw := m.Get(p.ID).RoundTimeoutRaw(); raw != "" {
		t.Fatalf("a rejected save wrote %q", raw)
	}

	// "0" is a deliberate unlimited, not "inherit".
	code, out = put(`{"round_timeout":"0"}`)
	if code != http.StatusOK {
		t.Fatalf("zero = %d %v", code, out)
	}
	if got := h.commanderRoundTimeout(m.Get(p.ID)); got != 0 {
		t.Fatalf("effective round timeout = %s, want 0 (unlimited)", got)
	}
}

// The instance default rides along in the list response so the settings form can
// say "inherit 15m" instead of guessing.
func TestProjectsListCarriesInstanceRoundTimeout(t *testing.T) {
	m, err := project.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create("Acme", ""); err != nil {
		t.Fatal(err)
	}
	h := &hub{mgr: m, cfg: hubConfig{MaxRuns: 3, RoundTimeout: 15 * time.Minute}, kits: map[string]*kit{}}

	r := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	w := httptest.NewRecorder()
	h.serveProjects(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("list = %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Projects            []map[string]any `json:"projects"`
		MaxRuns             int              `json:"max_runs"`
		RoundTimeoutSeconds int              `json:"round_timeout_seconds"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.RoundTimeoutSeconds != 900 {
		t.Fatalf("round_timeout_seconds = %d, want 900", out.RoundTimeoutSeconds)
	}
	if len(out.Projects) != 1 {
		t.Fatalf("projects = %v", out.Projects)
	}
}
