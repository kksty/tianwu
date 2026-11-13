package modelclient

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type opencodeTransport func(*http.Request) (*http.Response, error)

func (f opencodeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOpenCodeConversationHeaderSurvivesTurnsAndKeyChange(t *testing.T) {
	c, err := New("https://opencode.ai/inference/go/openai/v1/chat/completions/", "first-key", "fixture-model")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	c.HTTP = &http.Client{Transport: opencodeTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/inference/go/openai/v1/chat/completions" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.UserAgent() != "tianwu/0.x" {
			t.Fatalf("user agent = %q", r.UserAgent())
		}
		ids = append(ids, r.Header.Get("x-opencode-session"))
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"{}"},"finish_reason":"stop"}]}`)), Header: make(http.Header)}, nil
	})}
	for n := 0; n < 2; n++ {
		if _, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "fixture"}}); err != nil {
			t.Fatal(err)
		}
		c.APIKey = "rotated-key"
	}
	if ids[0] == "" || ids[0] != ids[1] {
		t.Fatalf("conversation changed across requests: %v", ids)
	}
	other, _ := New(c.BaseURL, "other-key", c.Model)
	if other.SessionID == c.SessionID {
		t.Fatal("new conversation reused an identity")
	}
}

func TestOpenCodeNormalizationKeepsBillingRoute(t *testing.T) {
	for _, prefix := range []string{"/inference/go/openai/v1", "/inference/openai/v1"} {
		base := "https://opencode.ai" + prefix
		if got := NormalizeOpenCodeBase(base + "/chat/completions/"); got != base {
			t.Fatalf("base = %q", got)
		}
	}
	for _, base := range []string{"https://opencode.ai.example/v1", "https://example.test/opencode.ai/v1", "https://opencode.ai@evil.test/v1", "http://opencode.ai/v1"} {
		r, _ := http.NewRequest(http.MethodPost, base, nil)
		ApplyEndpointHeaders(r, "fixture-session")
		if r.Header.Get("x-opencode-session") != "" {
			t.Fatalf("session header sent to %s", base)
		}
	}
}
