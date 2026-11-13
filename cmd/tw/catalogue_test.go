package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/settings"
)

// The settings page's "检测选取模型" action reads the draft endpoint's /models
// list before saving. These tests use an httptest gateway: no real credential
// or network is involved.

func catalogueHub(t *testing.T, baseURL string) *hub {
	t.Helper()
	dir := t.TempDir()
	harness := filepath.Join(dir, "cyber.yaml")
	body := fmt.Sprintf(`llm:
    active_profile: fast
    providers:
        - id: fast
          provider: openai
          base_url: %s
          api_key: fast-secret
          model: fast-model
`, baseURL)
	if err := os.WriteFile(harness, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := settings.LoadAt(filepath.Join(dir, "settings.json"), harness)
	if err != nil {
		t.Fatal(err)
	}
	return &hub{settings: st}
}

func callCatalogue(t *testing.T, h *hub, method string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, "/api/model-catalogue", &buf)
	rec := httptest.NewRecorder()
	h.serveModelCatalogue(rec, req)
	return rec
}

func TestModelCatalogueEndpointListsDraftModels(t *testing.T) {
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"fast-model"},{"id":"slow-model"}]}`))
	}))
	defer srv.Close()

	h := catalogueHub(t, srv.URL)
	rec := callCatalogue(t, h, http.MethodPost, map[string]any{
		"base_url": srv.URL,
		"model":    "slow-model",
		"api_key":  "draft-secret",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("catalogue: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Endpoint  string   `json:"endpoint"`
		Models    []string `json:"models"`
		Count     int      `json:"count"`
		Model     string   `json:"model"`
		Available bool     `json:"available"`
		Local     bool     `json:"local"`
		Error     string   `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Error != "" || out.Endpoint != srv.URL+"/v1/models" || out.Count != 2 {
		t.Fatalf("catalogue = %+v", out)
	}
	if len(out.Models) != 2 || out.Models[1] != "slow-model" || out.Model != "slow-model" || !out.Available {
		t.Fatalf("catalogue = %+v", out)
	}
	if !out.Local {
		t.Fatalf("loopback endpoint should be reported local: %+v", out)
	}
	if gotKey != "Bearer draft-secret" {
		t.Fatalf("gateway saw key %q", gotKey)
	}
	if strings.Contains(rec.Body.String(), "draft-secret") {
		t.Fatal("response leaked the API key")
	}
}

func TestModelCatalogueEndpointReusesSavedProfile(t *testing.T) {
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"data":[{"id":"fast-model"}]}`))
	}))
	defer srv.Close()

	h := catalogueHub(t, srv.URL)
	// Editing the saved profile leaves the key blank and only sends its id.
	rec := callCatalogue(t, h, http.MethodPost, map[string]any{"id": "fast", "model": "fast-model"})
	if rec.Code != http.StatusOK {
		t.Fatalf("catalogue: %d %s", rec.Code, rec.Body.String())
	}
	if gotKey != "Bearer fast-secret" {
		t.Fatalf("saved key was not reused: %q", gotKey)
	}
	if strings.Contains(rec.Body.String(), "fast-secret") {
		t.Fatal("response leaked the saved key")
	}
	var out struct {
		Models    []string `json:"models"`
		Available bool     `json:"available"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Models) != 1 || !out.Available {
		t.Fatalf("catalogue = %+v", out)
	}
}

func TestModelCatalogueEndpointRejectsBadRequests(t *testing.T) {
	h := catalogueHub(t, "http://127.0.0.1:1")
	if rec := callCatalogue(t, h, http.MethodGet, nil); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d %s", rec.Code, rec.Body.String())
	}
	if rec := callCatalogue(t, h, http.MethodPost, map[string]any{"model": "m"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty binding: %d %s", rec.Code, rec.Body.String())
	}
}
