package modelclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNormaliseBase(t *testing.T) {
	cases := map[string]string{
		"http://h:8080":                     "http://h:8080/v1",
		"http://h:8080/":                    "http://h:8080/v1",
		"http://h:8080/v1":                  "http://h:8080/v1",
		"http://h:8080/v1/":                 "http://h:8080/v1",
		"http://h:8080/v1/chat/completions": "http://h:8080/v1",
		"http://h:8080/api/v1":              "http://h:8080/api/v1",
	}
	for in, want := range cases {
		if got := normaliseBase(in); got != want {
			t.Errorf("normaliseBase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCompleteParsesResponseAndStripsThink(t *testing.T) {
	var gotPath, gotAuth, gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		var req struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotModel = req.Model
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "glm-5",
			"choices": []map[string]any{{
				"message":       map[string]any{"content": "<think>算一下</think>{\"noop\":{\"reason\":\"等\"}}"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
		})
	}))
	defer srv.Close()

	c, err := New(srv.URL, "sk-test", "glm-5")
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Complete(context.Background(), "sys", "user")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotAuth != "Bearer sk-test" || gotModel != "glm-5" {
		t.Fatalf("auth=%q model=%q", gotAuth, gotModel)
	}
	if strings.Contains(res.Content, "think") || !strings.Contains(res.Content, "noop") {
		t.Fatalf("content = %q", res.Content)
	}
	if res.Usage.TotalTokens != 15 || res.Model != "glm-5" {
		t.Fatalf("usage=%+v model=%q", res.Usage, res.Model)
	}
}

func TestChatSendsTranscriptInOrder(t *testing.T) {
	var got []Message
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
		}
		got = req.Messages
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message":       map[string]any{"content": `{"noop":{"reason":"x"}}`},
				"finish_reason": "stop",
			}},
		})
	}))
	defer srv.Close()

	c, err := New(srv.URL, "k", "m")
	if err != nil {
		t.Fatal(err)
	}
	in := []Message{
		{Role: "system", Content: "s"},
		{Role: "user", Content: "u"},
		{Role: "assistant", Content: `{"tool":"facts"}`},
		{Role: "user", Content: "结果"},
	}
	if _, err := c.Chat(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(in) {
		t.Fatalf("messages = %d, want %d", len(got), len(in))
	}
	for i := range in {
		if got[i] != in[i] {
			t.Fatalf("message[%d] = %+v, want %+v", i, got[i], in[i])
		}
	}
}

func TestCompleteReportsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "k", "m")
	if _, err := c.Complete(context.Background(), "s", "u"); err == nil {
		t.Fatal("want error on 401")
	}
}

func TestNewRequiresAllParts(t *testing.T) {
	if _, err := New("", "k", "m"); err == nil {
		t.Fatal("missing base must fail")
	}
	if _, err := New("http://h", "", "m"); err == nil {
		t.Fatal("missing key must fail")
	}
	if _, err := New("http://h", "k", ""); err == nil {
		t.Fatal("missing model must fail")
	}
}

// A length-capped completion is a prefix, not an answer. Reporting it as a
// generic parse failure sent the decider into a retry loop against the same cap;
// the error has to name the cap.
func TestChatReportsATruncatedCompletion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message":       map[string]any{"content": `{"intents":[{"title":"x"`},
				"finish_reason": "length",
			}},
			"usage": map[string]any{"total_tokens": 12},
		})
	}))
	defer srv.Close()

	c, err := New(srv.URL, "sk-test", "m")
	if err != nil {
		t.Fatal(err)
	}
	if c.MaxTokens != 0 {
		t.Fatalf("default max_tokens = %d, want 0 (omit; provider/harness default)", c.MaxTokens)
	}
	if _, err := c.Complete(context.Background(), "s", "u"); err == nil || !strings.Contains(err.Error(), "max_tokens") {
		t.Fatalf("truncated completion error = %v", err)
	}
}

// Zero max_tokens is "not set": the request must omit the field so the
// provider's model limit applies, exactly like a harness run without
// --max-tokens. A hardcoded cap here is what made the two disagree.
func TestChatOmitsMaxTokensWhenUnset(t *testing.T) {
	var raw map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&raw)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": `{"noop":{"reason":"x"}}`}, "finish_reason": "stop"}},
		})
	}))
	defer srv.Close()

	c, err := New(srv.URL, "k", "m")
	if err != nil {
		t.Fatal(err)
	}
	if c.MaxTokens != 0 {
		t.Fatalf("MaxTokens = %d", c.MaxTokens)
	}
	if _, err := c.Complete(context.Background(), "s", "u"); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["max_tokens"]; ok {
		t.Fatalf("request sent max_tokens=%v although it was unset", raw["max_tokens"])
	}
}
