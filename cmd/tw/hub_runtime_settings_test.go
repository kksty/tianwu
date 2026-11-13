package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/settings"
)

// runtimeSettingsHub builds the smallest hub that serves /api/runtime-settings:
// a temp legacy store (so no real home file is read or written) and a gate at
// the historic default. The route is exercised through the hub's own root
// switch, which is what makes it an operator surface (no project prefix).
func runtimeSettingsHub(t *testing.T) (*hub, string) {
	t.Helper()
	dir := t.TempDir()
	legacy := filepath.Join(dir, "settings.json")
	st, err := settings.LoadAt(legacy, "")
	if err != nil {
		t.Fatal(err)
	}
	return &hub{
		settings:      st,
		auxiliaryGate: executor.NewGate(1),
		kits:          map[string]*kit{},
	}, legacy
}

func callRuntimeSettings(t *testing.T, h *hub, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, "/api/runtime-settings", nil)
	} else {
		req = httptest.NewRequest(method, "/api/runtime-settings", strings.NewReader(body))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

type runtimeSettingsResponse struct {
	AuxiliaryConcurrency    int `json:"auxiliary_concurrency"`
	MaxAuxiliaryConcurrency int `json:"max_auxiliary_concurrency"`
}

func decodeRuntimeSettings(t *testing.T, rec *httptest.ResponseRecorder) runtimeSettingsResponse {
	t.Helper()
	var out runtimeSettingsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not JSON (status %d): %v\n%s", rec.Code, err, rec.Body.String())
	}
	return out
}

func TestRuntimeSettingsEndpointControlsAuxiliaryGate(t *testing.T) {
	h, legacy := runtimeSettingsHub(t)

	// GET reports the historic default plus the hard maximum the console uses as
	// its slider bound.
	rec := callRuntimeSettings(t, h, http.MethodGet, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d %s", rec.Code, rec.Body.String())
	}
	if got := decodeRuntimeSettings(t, rec); got.AuxiliaryConcurrency != 1 ||
		got.MaxAuxiliaryConcurrency != settings.MaxAuxiliaryConcurrency {
		t.Fatalf("GET body = %+v", got)
	}

	// PUT 3 returns the contract shape, persists to the legacy settings.json and
	// resizes the live gate for new acquisitions.
	gate := h.auxiliaryGate
	rec = callRuntimeSettings(t, h, http.MethodPut, `{"auxiliary_concurrency":3}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT 3 = %d %s", rec.Code, rec.Body.String())
	}
	if got := decodeRuntimeSettings(t, rec); got.AuxiliaryConcurrency != 3 ||
		got.MaxAuxiliaryConcurrency != settings.MaxAuxiliaryConcurrency {
		t.Fatalf("PUT 3 body = %+v", got)
	}
	if h.auxiliaryGate != gate {
		t.Fatal("PUT replaced the gate pointer; verifier/report closures would keep the old one")
	}
	if got := h.auxiliaryGate.Capacity(); got != 3 {
		t.Fatalf("gate capacity = %d, want 3", got)
	}
	reloaded, err := settings.LoadAt(legacy, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.RuntimeSettings().AuxiliaryConcurrency; got != 3 {
		t.Fatalf("persisted auxiliary_concurrency = %d, want 3", got)
	}

	// Invalid requests are 400 and change neither the gate nor the file.
	for _, body := range []string{
		`{"auxiliary_concurrency":9}`,
		`{"auxiliary_concurrency":0}`,
		`{"auxiliary_concurrency":-2}`,
		`{"auxiliary_concurrency":3.5}`,
		`{"auxiliary_concurrency":"3"}`,
		`{"auxiliary_concurrency":null}`,
		`{}`,
		`{"bogus":1}`,
	} {
		rec := callRuntimeSettings(t, h, http.MethodPut, body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("PUT %s = %d, want 400: %s", body, rec.Code, rec.Body.String())
		}
		if got := h.auxiliaryGate.Capacity(); got != 3 {
			t.Fatalf("PUT %s resized the gate to %d", body, got)
		}
	}
	reloaded, err = settings.LoadAt(legacy, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.RuntimeSettings().AuxiliaryConcurrency; got != 3 {
		t.Fatalf("a rejected PUT reached disk: %d", got)
	}

	// The route is GET/PUT only.
	rec = callRuntimeSettings(t, h, http.MethodPost, "")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST = %d, want 405", rec.Code)
	}

	// GET reflects the stored value after the save.
	rec = callRuntimeSettings(t, h, http.MethodGet, "")
	if got := decodeRuntimeSettings(t, rec); got.AuxiliaryConcurrency != 3 {
		t.Fatalf("GET after save = %+v", got)
	}
}

// A hub without a settings store or without a gate answers 503 instead of
// panicking (it can be built during degraded startup).
func TestRuntimeSettingsUnavailableWithoutStore(t *testing.T) {
	h := &hub{kits: map[string]*kit{}}
	rec := callRuntimeSettings(t, h, http.MethodGet, "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET = %d, want 503", rec.Code)
	}
	rec = callRuntimeSettings(t, h, http.MethodPut, `{"auxiliary_concurrency":2}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("PUT = %d, want 503", rec.Code)
	}
}
