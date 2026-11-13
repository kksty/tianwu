package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/settings"
)

func TestProfilePauseEndpointKeepsDefaultAndRejectsImplicitResume(t *testing.T) {
	h, _ := profileHub(t)
	for _, bad := range []any{map[string]any{}, map[string]any{"paused": nil}, map[string]any{"paused": "true"}} {
		if rec := callProfile(t, h, http.MethodPost, "/api/model-profiles/fast/pause", bad); rec.Code != 400 {
			t.Fatalf("invalid pause accepted: %s", rec.Body.String())
		}
	}
	if rec := callProfile(t, h, http.MethodPost, "/api/model-profiles/fast/pause", map[string]any{"paused": true}); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"paused":true`) || strings.Contains(rec.Body.String(), "fast-secret") {
		t.Fatalf("pause: %s", rec.Body.String())
	}
	cat, err := h.settings.Profiles()
	if err != nil || cat.ActiveProfile != "fast" || !cat.Profiles[0].Paused || cat.Profiles[0].Runnable {
		t.Fatal("pause lost configuration or changed the instance default")
	}
	if rec := callProfile(t, h, http.MethodPost, "/api/model-profiles/fast/pause", map[string]any{"paused": false}); rec.Code != 200 {
		t.Fatalf("resume: %s", rec.Body.String())
	}
	if rec := callProfile(t, h, http.MethodPost, "/api/model-profiles/missing/pause", map[string]any{"paused": true}); rec.Code != 404 {
		t.Fatal("missing profile was not rejected")
	}
}

func TestProfileConnectivityEndpointUsesSavedKeyAndReturnsActualModel(t *testing.T) {
	calls := 0
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer fixture-secret" {
			t.Error("wrong connectivity request")
		}
		fmt.Fprint(w, `{"model":"returned-model","choices":[{"message":{"content":"OK"},"finish_reason":"stop"}]}`)
	}))
	defer gateway.Close()
	h, _ := profileHub(t)
	if _, err := h.settings.SaveProfile(settings.Profile{ID: "check", BaseURL: gateway.URL, APIKey: "fixture-secret", Model: "configured-model"}); err != nil {
		t.Fatal(err)
	}
	rec := callProfile(t, h, http.MethodPost, "/api/model-profiles/check/probe", nil)
	var result map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || result["available"] != true || result["returned_model"] != "returned-model" || result["probe_kind"] != "connectivity" || calls != 1 {
		t.Fatalf("check: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "fixture-secret") {
		t.Fatal("connectivity response exposed credentials")
	}
}
