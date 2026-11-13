package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/project"
	"github.com/kksty/tianwu/internal/settings"
)

func profileHub(t *testing.T) (*hub, string) {
	t.Helper()
	dir := t.TempDir()
	harness := filepath.Join(dir, "cyber.yaml")
	body := `llm:
    active_profile: fast
    providers:
        - id: fast
          provider: openai
          base_url: http://fast:8080
          api_key: fast-secret
          model: fast-model
`
	if err := os.WriteFile(harness, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := settings.LoadAt(filepath.Join(dir, "settings.json"), harness)
	if err != nil {
		t.Fatal(err)
	}
	return &hub{settings: st}, harness
}

func callProfile(t *testing.T, h *hub, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	rec := httptest.NewRecorder()
	if strings.HasPrefix(path, "/api/model-profiles/") {
		h.serveModelProfile(rec, req)
	} else {
		h.serveModelProfiles(rec, req)
	}
	return rec
}

func TestProfileEndpointCreatesEditsDeletes(t *testing.T) {
	h, harness := profileHub(t)

	rec := callProfile(t, h, http.MethodGet, "/api/model-profiles", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "fast-secret") {
		t.Fatal("list leaked the key")
	}

	rec = callProfile(t, h, http.MethodPost, "/api/model-profiles", map[string]any{
		"id": "slow", "name": "Slow", "provider": "anthropic",
		"base_url": "http://slow:8080", "model": "slow-model", "api_key": "slow-secret",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"runnable":true`) {
		t.Fatalf("created view = %s", rec.Body.String())
	}

	// An edit without a key keeps the stored credential.
	rec = callProfile(t, h, http.MethodPut, "/api/model-profiles/slow", map[string]any{
		"name": "Slow v2", "provider": "anthropic",
		"base_url": "http://slow:8080", "model": "slow-v2",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	data, err := os.ReadFile(harness)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "slow-secret") || !strings.Contains(string(data), "slow-v2") {
		t.Fatalf("update did not preserve the key:\n%s", data)
	}

	// The active profile is the instance default: deleting it would leave the
	// decider pointing at nothing.
	rec = callProfile(t, h, http.MethodDelete, "/api/model-profiles/fast", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("deleted active: %d %s", rec.Code, rec.Body.String())
	}
	rec = callProfile(t, h, http.MethodDelete, "/api/model-profiles/slow", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(readProfileFile(t, harness), "slow-secret") {
		t.Fatal("delete left the entry behind")
	}
}

func TestProfileEndpointActivates(t *testing.T) {
	h, harness := profileHub(t)
	rec := callProfile(t, h, http.MethodPost, "/api/model-profiles", map[string]any{
		"id": "slow", "provider": "anthropic", "base_url": "http://slow:8080",
		"model": "slow-model", "api_key": "slow-secret",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	rec = callProfile(t, h, http.MethodPost, "/api/model-profiles/slow/activate", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"active":true`) {
		t.Fatalf("activate: %d %s", rec.Code, rec.Body.String())
	}
	cat, err := h.settings.Profiles()
	if err != nil {
		t.Fatal(err)
	}
	if cat.ActiveProfile != "slow" {
		t.Fatalf("active = %q", cat.ActiveProfile)
	}
	if !strings.Contains(readProfileFile(t, harness), "active_profile: slow") {
		t.Fatal("active_profile not written")
	}
}

func TestProfileEndpointRejectsIncompleteCreate(t *testing.T) {
	h, _ := profileHub(t)
	rec := callProfile(t, h, http.MethodPost, "/api/model-profiles", map[string]any{
		"id": "x", "base_url": "http://x:1", "model": "m",
	})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "api_key") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestProfileEndpointNotFound(t *testing.T) {
	h, _ := profileHub(t)
	rec := callProfile(t, h, http.MethodDelete, "/api/model-profiles/missing", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func readProfileFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// The project's tier defaults are validated against the instance's profile
// catalogue on save: a typo or an unfinished profile would otherwise make every
// dispatch fail later with one opaque error. An explicit "" clears the default.
func TestUpdateProjectProfileDefaults(t *testing.T) {
	h, _ := profileHub(t)
	mgr, err := project.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := mgr.Create("tiered", "")
	if err != nil {
		t.Fatal(err)
	}
	h.mgr = mgr

	put := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/api/projects/"+p.ID, strings.NewReader(body))
		h.updateProject(rec, req, p.ID)
		return rec
	}
	rec := put(`{"worker_profile":"fast","strong_profile":"fast"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("save defaults: %d %s", rec.Code, rec.Body.String())
	}
	if worker, strong := p.ProfileDefaults(); worker != "fast" || strong != "fast" {
		t.Fatalf("defaults = %q/%q", worker, strong)
	}
	var info projectInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.WorkerProfile != "fast" || info.StrongProfile != "fast" {
		t.Fatalf("info = %+v", info)
	}

	// Unknown id: the save is refused, not stored and left to fail at dispatch.
	rec = put(`{"worker_profile":"gone"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "没有模型配置") {
		t.Fatalf("unknown profile: %d %s", rec.Code, rec.Body.String())
	}
	if worker, _ := p.ProfileDefaults(); worker != "fast" {
		t.Fatalf("a refused save changed the defaults: %q", worker)
	}

	// An omitted field keeps its value; "" clears only that tier.
	if rec := put(`{"worker_profile":""}`); rec.Code != http.StatusOK {
		t.Fatalf("clear: %d %s", rec.Code, rec.Body.String())
	}
	if worker, strong := p.ProfileDefaults(); worker != "" || strong != "fast" {
		t.Fatalf("cleared defaults = %q/%q", worker, strong)
	}
	if projectProfileDefaults(p) == nil || projectProfileDefaults(p).Strong != "fast" {
		t.Fatalf("api snapshot = %+v", projectProfileDefaults(p))
	}
}
