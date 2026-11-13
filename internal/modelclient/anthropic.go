package modelclient

// Anthropic Messages API support. The decider never uses provider-native tool
// calls (it reads the ledger through its own JSON protocol), so this is a plain
// text exchange: system prompt, alternating messages, text blocks back.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	anthropicVersion         = "2023-06-01"
	anthropicDefaultMaxToken = 4096
)

type anthropicRequest struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	System      string             `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
	Temperature float64            `json:"temperature,omitempty"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicResponse struct {
	Model   string `json:"model"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// chatAnthropic sends one non-streaming Messages request. Anthropic has no
// response_format switch, so the system prompt's "only JSON" contract is what
// keeps the decider's parser happy (the same contract the OpenAI client states
// through response_format).
func (c *Client) chatAnthropic(ctx context.Context, msgs []Message) (Result, error) {
	var system []string
	turns := make([]anthropicMessage, 0, len(msgs))
	for _, m := range msgs {
		content := strings.TrimSpace(m.Content)
		role := strings.ToLower(strings.TrimSpace(m.Role))
		switch role {
		case "system", "developer":
			if content != "" {
				system = append(system, m.Content)
			}
		case "assistant", "user":
			if content == "" {
				continue
			}
			// Anthropic requires alternating roles; the decider's tool loop
			// can produce two user turns in a row (tool results), so merge.
			if n := len(turns); n > 0 && turns[n-1].Role == role {
				turns[n-1].Content += "\n\n" + m.Content
				continue
			}
			turns = append(turns, anthropicMessage{Role: role, Content: m.Content})
		}
	}
	if len(turns) == 0 {
		return Result{}, fmt.Errorf("空对话：没有 user/assistant 消息")
	}
	// The Messages API starts with a user turn. The decider always has one, so
	// this only protects an odd transcript from a confusing 400.
	if turns[0].Role != "user" {
		turns = append([]anthropicMessage{{Role: "user", Content: "(continue)"}}, turns...)
	}

	max := c.MaxTokens
	if max <= 0 {
		max = anthropicDefaultMaxToken
	}
	body, err := json.Marshal(anthropicRequest{
		Model:       c.Model,
		MaxTokens:   max,
		System:      strings.Join(system, "\n\n"),
		Messages:    turns,
		Temperature: 0.2,
	})
	if err != nil {
		return Result{}, err
	}

	endpoint := c.BaseURL + "/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.APIKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	ApplyEndpointHeaders(req, c.SessionID)

	start := time.Now()
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("模型请求失败（%s）：%w", endpoint, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return Result{}, err
	}
	latency := time.Since(start)

	if resp.StatusCode != http.StatusOK {
		return Result{}, &APIError{StatusCode: resp.StatusCode, Body: clip(string(raw), 400)}
	}

	var out anthropicResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return Result{}, fmt.Errorf("模型响应不是 JSON：%w", err)
	}
	if out.Error != nil && out.Error.Message != "" {
		return Result{}, fmt.Errorf("模型报错：%s", out.Error.Message)
	}

	var text strings.Builder
	for _, block := range out.Content {
		if block.Type == "text" || block.Type == "" {
			text.WriteString(block.Text)
		}
	}
	content := strings.TrimSpace(thinkBlock.ReplaceAllString(text.String(), ""))
	if content == "" {
		if out.StopReason == "max_tokens" {
			return Result{}, fmt.Errorf("模型输出被 max_tokens 截断（%d），没有正文", max)
		}
		return Result{}, fmt.Errorf("模型返回空内容（stop_reason=%s）", out.StopReason)
	}

	model := out.Model
	if model == "" {
		model = c.Model
	}
	return Result{
		ReturnedModel: out.Model,
		Content:       content,
		Usage: Usage{
			PromptTokens:     out.Usage.InputTokens,
			CompletionTokens: out.Usage.OutputTokens,
			TotalTokens:      out.Usage.InputTokens + out.Usage.OutputTokens,
		},
		Latency: latency,
		Model:   model,
	}, nil
}
