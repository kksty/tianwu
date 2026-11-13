package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kksty/tianwu/internal/settings"
)

func TestListModelsOpenAI(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %s", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "gpt-4o", "max_output_tokens": 16384},
				{"id": "gpt-4o-mini"},
				{"id": "gpt-4o"},
			},
		})
	}))
	defer srv.Close()

	models, limits, err := listModels(context.Background(), settings.Model{
		BaseURL:  srv.URL + "/v1",
		APIKey:   "k",
		Provider: "openai",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer k" {
		t.Fatalf("auth = %q", gotAuth)
	}
	if len(models) != 2 || models[0] != "gpt-4o" || models[1] != "gpt-4o-mini" {
		t.Fatalf("models = %v", models)
	}
	// The output ceiling is part of the catalogue: settings uses it to clamp
	// max_tokens so the decider and harness never ask for more than the model
	// accepts.
	if limits["gpt-4o"] != 16384 {
		t.Fatalf("limits = %v", limits)
	}
	if _, ok := limits["gpt-4o-mini"]; ok {
		t.Fatalf("a model without max_output_tokens must not get a fake limit: %v", limits)
	}
}

// The configured base is usually a bare host (the harness contract adds /v1).
// The model list has to follow chat completions there instead of asking the
// gateway for /models, which is why "检测" failed on such a profile.
func TestListModelsAddsV1LikeChat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %s, want /v1/models", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m1"}}})
	}))
	defer srv.Close()

	models, _, err := listModels(context.Background(), settings.Model{
		BaseURL: srv.URL, APIKey: "k", Provider: "openai",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0] != "m1" {
		t.Fatalf("models = %v", models)
	}
}

func TestListModelsAnthropic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "ak" || r.Header.Get("anthropic-version") == "" {
			t.Errorf("headers = %v", r.Header)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"id": "claude-sonnet-4-5"}},
		})
	}))
	defer srv.Close()

	models, _, err := listModels(context.Background(), settings.Model{
		BaseURL:  srv.URL,
		APIKey:   "ak",
		Provider: "anthropic",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0] != "claude-sonnet-4-5" {
		t.Fatalf("models = %v", models)
	}
}

func TestListModelsReportsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"bad key"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	if _, _, err := listModels(context.Background(), settings.Model{
		BaseURL: srv.URL + "/v1",
		APIKey:  "k",
	}); err == nil {
		t.Fatal("expected an error for 401")
	}
}

// The detected catalogue's max_output_tokens caps an explicit setting; zero
// (not set) stays zero so the harness/provider default applies.
func TestClampMaxTokensUsesDetectedLimit(t *testing.T) {
	h := &hub{modelLimits: map[string]int{"kimi-k3-1": 32000}}

	got := h.clampMaxTokens(settings.Model{Model: "kimi-k3-1", MaxTokens: 40000})
	if got.MaxTokens != 32000 {
		t.Fatalf("clamped max_tokens = %d, want 32000", got.MaxTokens)
	}
	if got := h.clampMaxTokens(settings.Model{Model: "kimi-k3-1"}); got.MaxTokens != 0 {
		t.Fatalf("unset max_tokens must stay unset, got %d", got.MaxTokens)
	}
	if got := h.clampMaxTokens(settings.Model{Model: "unknown", MaxTokens: 40000}); got.MaxTokens != 40000 {
		t.Fatalf("unknown model must not be clamped, got %d", got.MaxTokens)
	}
}
