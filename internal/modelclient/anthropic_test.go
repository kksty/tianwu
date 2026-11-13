package modelclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAnthropicChatSendsMessagesShape(t *testing.T) {
	var got struct {
		Model     string             `json:"model"`
		MaxTokens int                `json:"max_tokens"`
		System    string             `json:"system"`
		Messages  []anthropicMessage `json:"messages"`
	}
	var path, key, version string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, key, version = r.URL.Path, r.Header.Get("x-api-key"), r.Header.Get("anthropic-version")
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_, _ = w.Write([]byte(`{
			"id":"msg_1","model":"claude-test","stop_reason":"end_turn",
			"content":[{"type":"text","text":"<thinking>skip</thinking>{\"noop\":{\"reason\":\"x\"}}"}],
			"usage":{"input_tokens":120,"output_tokens":30}
		}`))
	}))
	defer srv.Close()

	c, err := NewWithProvider(srv.URL, "secret", "claude-test", "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	c.MaxTokens = 2048
	res, err := c.Chat(context.Background(), []Message{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "thinking"},
		{Role: "user", Content: "tool result"},
		{Role: "user", Content: "second user turn"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if path != "/v1/messages" {
		t.Fatalf("path = %q", path)
	}
	if key != "secret" || version != anthropicVersion {
		t.Fatalf("headers = %q / %q", key, version)
	}
	if got.Model != "claude-test" || got.MaxTokens != 2048 || got.System != "system prompt" {
		t.Fatalf("request = %+v", got)
	}
	if len(got.Messages) != 3 {
		t.Fatalf("messages = %+v", got.Messages)
	}
	// Consecutive user turns merge; Anthropic rejects non-alternating roles.
	if got.Messages[2].Role != "user" || !strings.Contains(got.Messages[2].Content, "tool result") ||
		!strings.Contains(got.Messages[2].Content, "second user turn") {
		t.Fatalf("merged user turn = %+v", got.Messages[2])
	}
	if res.Content != `{"noop":{"reason":"x"}}` {
		t.Fatalf("content = %q", res.Content)
	}
	if res.Usage.TotalTokens != 150 || res.Usage.PromptTokens != 120 || res.Usage.CompletionTokens != 30 {
		t.Fatalf("usage = %+v", res.Usage)
	}
	if res.Model != "claude-test" {
		t.Fatalf("model = %q", res.Model)
	}
}

func TestAnthropicChatReportsAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
	}))
	defer srv.Close()

	c, err := NewWithProvider(srv.URL+"/v1", "bad", "claude", "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Complete(context.Background(), "s", "u")
	if err == nil || !strings.Contains(err.Error(), "invalid x-api-key") {
		t.Fatalf("error = %v", err)
	}
}

func TestAnthropicTruncatedResponseIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"model":"m","stop_reason":"max_tokens","content":[{"type":"text","text":"  "}],"usage":{"input_tokens":1,"output_tokens":2}}`))
	}))
	defer srv.Close()
	c, err := NewWithProvider(srv.URL+"/v1", "k", "m", "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Complete(context.Background(), "s", "u"); err == nil || !strings.Contains(err.Error(), "max_tokens") {
		t.Fatalf("error = %v", err)
	}
}

func TestProviderResolution(t *testing.T) {
	for _, tc := range []struct {
		provider, base, want string
	}{
		{"anthropic", "https://api.anthropic.com/v1", ProviderAnthropic},
		{"Anthropic", "http://gw:8080", ProviderAnthropic},
		{"", "https://api.anthropic.com/v1", ProviderAnthropic},
		{"", "https://api.anthropic.com", ProviderAnthropic},
		{"", "http://gw:8080/v1", ProviderOpenAI},
		{"openai", "http://gw:8080", ProviderOpenAI},
		{"deepseek", "https://api.deepseek.com/v1", ProviderOpenAI},
	} {
		if got := providerOf(tc.provider, tc.base); got != tc.want {
			t.Fatalf("providerOf(%q,%q) = %q, want %q", tc.provider, tc.base, got, tc.want)
		}
	}
}

func TestAnthropicBaseNormalisation(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"https://api.anthropic.com", "https://api.anthropic.com/v1"},
		{"https://api.anthropic.com/v1/", "https://api.anthropic.com/v1"},
		{"https://gw/anthropic", "https://gw/anthropic"},
		{"https://gw/anthropic/v1/messages", "https://gw/anthropic/v1"},
	} {
		if got := normaliseAnthropicBase(tc.in); got != tc.want {
			t.Fatalf("normaliseAnthropicBase(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
