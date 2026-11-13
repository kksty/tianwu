// Package modelclient talks to an LLM endpoint in either of the two protocols
// the harness supports: OpenAI-compatible chat completions (the default) and
// Anthropic Messages. The control plane and the scan nodes share one model
// configuration, so the decider must be able to speak whichever protocol the
// active harness profile uses.
//
// It reuses the same environment variables as cyber-harness so a single
// configuration feeds both layers:
//
//	CYBER_BASE_URL   http://host:8080      (a trailing /v1 is added if absent)
//	CYBER_PROVIDER   openai | anthropic
//	CYBER_API_KEY    sk-...
//	CYBER_MODEL      qwen3.8-27b
//
// The decider is a stateless round, not a session: it may make a few read-only
// ledger queries inside one round, then returns an action (D3, D54).
package modelclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	EnvBase     = "CYBER_BASE_URL"
	EnvKey      = "CYBER_API_KEY"
	EnvModel    = "CYBER_MODEL"
	EnvProvider = "CYBER_PROVIDER"
)

// Provider is the wire protocol, not the vendor: DeepSeek, Moonshot, Ollama …
// are all OpenAI-compatible and map to ProviderOpenAI.
const (
	ProviderOpenAI    = "openai"
	ProviderAnthropic = "anthropic"
)

// Usage is the token accounting the decision log records.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Result is one completion.
type Result struct {
	ReturnedModel string
	Content       string
	Usage         Usage
	Latency       time.Duration
	Model         string
}

// Client is one model endpoint.
type Client struct {
	// Provider is the protocol: ProviderOpenAI or ProviderAnthropic.
	Provider  string
	BaseURL   string
	APIKey    string
	Model     string
	HTTP      *http.Client
	MaxTokens int
	// SessionID belongs to this client's conversation, independent of its key.
	SessionID string
}

// normaliseBase accepts "http://h:8080" and "http://h:8080/v1" alike.
func normaliseBase(u string) string {
	u = strings.TrimSpace(u)
	u = strings.TrimRight(u, "/")
	if u == "" {
		return ""
	}
	if strings.HasSuffix(u, "/chat/completions") {
		u = strings.TrimSuffix(u, "/chat/completions")
	}
	if !strings.HasSuffix(u, "/v1") && !strings.Contains(u, "/v1/") {
		u += "/v1"
	}
	return u
}

// normaliseAnthropicBase mirrors the harness's Anthropic endpoint rule
// (base + "/messages") and only adds /v1 when the operator gave a bare host.
// A custom gateway path is left alone.
func normaliseAnthropicBase(u string) string {
	u = strings.TrimSpace(u)
	u = strings.TrimRight(u, "/")
	u = strings.TrimSuffix(u, "/messages")
	if u == "" {
		return ""
	}
	if parsed, err := url.Parse(u); err == nil && parsed.Host != "" && (parsed.Path == "" || parsed.Path == "/") {
		u = strings.TrimRight(u, "/") + "/v1"
	}
	return u
}

// providerOf resolves the protocol. Empty/unknown vendor names are
// OpenAI-compatible unless the URL itself is unmistakably Anthropic.
func providerOf(provider, base string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case ProviderAnthropic:
		return ProviderAnthropic
	case "", ProviderOpenAI:
		if strings.TrimSpace(provider) == "" {
			lower := strings.ToLower(base)
			if strings.Contains(lower, "anthropic.com") || strings.HasSuffix(strings.TrimRight(lower, "/"), "/messages") {
				return ProviderAnthropic
			}
		}
		return ProviderOpenAI
	default:
		// deepseek / moonshot / groq / openrouter / ollama / zhipu … all speak
		// the OpenAI wire format; provider is a protocol, not a brand.
		return ProviderOpenAI
	}
}

// Endpoint resolves the wire protocol and normalises the base URL exactly the
// way the client does. Auxiliary callers (the settings page asking for /models)
// use it so they cannot hit a different path than chat completions do: the
// harness base is commonly written as "http://host:8080" without "/v1".
func Endpoint(base, provider string) (string, string) {
	p := providerOf(provider, base)
	if p == ProviderAnthropic {
		return normaliseAnthropicBase(base), p
	}
	return normaliseBase(base), p
}

// New validates and builds an OpenAI-compatible client.
func New(base, key, model string) (*Client, error) {
	return NewWithProvider(base, key, model, "")
}

// NewWithProvider builds a client for the given protocol.
func NewWithProvider(base, key, model, provider string) (*Client, error) {
	p := providerOf(provider, base)
	if p == ProviderAnthropic {
		base = normaliseAnthropicBase(base)
	} else {
		base = normaliseBase(base)
	}
	key = strings.TrimSpace(key)
	model = strings.TrimSpace(model)
	switch {
	case base == "":
		return nil, fmt.Errorf("%s 为空：无法确定模型端点", EnvBase)
	case key == "":
		return nil, fmt.Errorf("%s 为空：无法鉴权", EnvKey)
	case model == "":
		return nil, fmt.Errorf("%s 为空：不知道该调哪个模型", EnvModel)
	}
	// Zero means "do not send max_tokens": the provider's model limit (or the
	// harness's own default) applies. The decider and a local harness must agree
	// on that boundary, so no invented cap lives here. Anthropic is the
	// exception its API requires a value; it keeps a conservative default until
	// the settings page sets one.
	maxTokens := 0
	if p == ProviderAnthropic {
		maxTokens = anthropicDefaultMaxToken
	}
	return &Client{
		Provider:  p,
		BaseURL:   base,
		APIKey:    key,
		Model:     model,
		HTTP:      &http.Client{Timeout: 300 * time.Second},
		MaxTokens: maxTokens,
		SessionID: "tw-" + rand.Text(),
	}, nil
}

// FromEnv builds a client from CYBER_* variables.
func FromEnv() (*Client, error) {
	return NewWithProvider(os.Getenv(EnvBase), os.Getenv(EnvKey), os.Getenv(EnvModel), os.Getenv(EnvProvider))
}

func (c *Client) Name() string { return c.Model }

// Message is one turn of a chat transcript.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model          string            `json:"model"`
	Messages       []Message         `json:"messages"`
	Temperature    float64           `json:"temperature,omitempty"`
	MaxTokens      int               `json:"max_tokens,omitempty"`
	ResponseFormat map[string]string `json:"response_format,omitempty"`
	Stream         bool              `json:"stream,omitempty"`
}

type chatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage Usage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

var thinkBlock = regexp.MustCompile(`(?is)<(think|thinking|analysis)>.*?</(think|thinking|analysis)>`)

// Chat runs one non-streaming JSON-mode chat over a transcript. JSON mode
// stays on because every turn — a tool call or a final action — is a JSON
// object. The provider decides whether that mode is a request field (OpenAI)
// or just the prompt's contract (Anthropic has no response_format).
func (c *Client) Chat(ctx context.Context, msgs []Message) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(msgs) == 0 {
		return Result{}, fmt.Errorf("空对话")
	}
	if c.Provider == ProviderAnthropic {
		return c.chatAnthropic(ctx, msgs)
	}
	return c.chatOpenAI(ctx, msgs, true)
}

// Check makes one plain-text inference without JSON mode or tool requirements.
func (c *Client) Check(ctx context.Context) (Result, error) {
	msgs := []Message{{Role: "user", Content: "Connectivity check. Reply only OK."}}
	if c.Provider == ProviderAnthropic {
		return c.chatAnthropic(ctx, msgs)
	}
	return c.chatOpenAI(ctx, msgs, false)
}

func (c *Client) chatOpenAI(ctx context.Context, msgs []Message, jsonMode bool) (Result, error) {
	max := c.MaxTokens
	reqBody := chatRequest{
		Model:          c.Model,
		Messages:       msgs,
		Temperature:    0.2,
		MaxTokens:      max,
		ResponseFormat: map[string]string{"type": "json_object"},
	}
	if !jsonMode {
		reqBody.ResponseFormat = nil
		reqBody.Temperature = 0
	}
	b, err := json.Marshal(reqBody)
	if err != nil {
		return Result{}, err
	}

	url := c.BaseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	ApplyEndpointHeaders(req, c.SessionID)

	start := time.Now()
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("模型请求失败（%s）：%w", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return Result{}, err
	}
	latency := time.Since(start)

	if resp.StatusCode != http.StatusOK {
		return Result{}, &APIError{StatusCode: resp.StatusCode, Body: clip(string(body), 400)}
	}
	var out chatResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return Result{}, fmt.Errorf("模型响应不是 JSON：%w", err)
	}
	if out.Error != nil && out.Error.Message != "" {
		return Result{}, fmt.Errorf("模型报错：%s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return Result{}, fmt.Errorf("模型没有返回任何 choice")
	}
	msg := out.Choices[0].Message
	content := strings.TrimSpace(thinkBlock.ReplaceAllString(msg.Content, ""))
	if content == "" {
		if strings.TrimSpace(msg.ReasoningContent) != "" {
			return Result{}, fmt.Errorf("模型只输出了推理内容，没有正文（finish=%s）", out.Choices[0].FinishReason)
		}
		return Result{}, fmt.Errorf("模型返回空内容（finish=%s）", out.Choices[0].FinishReason)
	}
	// A plan that hit the token cap is a prefix of a plan, not a plan: the
	// decider would fail to parse it and retry into the same wall. Say why.
	if out.Choices[0].FinishReason == "length" {
		return Result{}, fmt.Errorf("模型输出被 max_tokens 截断（%d，推理与正文共用）：调大 max_tokens 或缩小输入", max)
	}
	model := out.Model
	if model == "" {
		model = c.Model
	}
	return Result{Content: content, Usage: out.Usage, Latency: latency, Model: model, ReturnedModel: out.Model}, nil
}

// Complete is the one-shot form, kept for callers that have no transcript.
func (c *Client) Complete(ctx context.Context, system, user string) (Result, error) {
	return c.Chat(ctx, []Message{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	})
}

func clip(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}
