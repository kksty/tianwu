package modelclient

import (
	"encoding/json"
	"fmt"
	"strings"
)

// APIError is a non-200 answer from the model endpoint. The status code is kept
// typed so callers can tell "this will never work without a human" (bad key,
// payment required, quota exhausted) from a transient transport/rate-limit
// failure. It is not a model answer and must not be parsed as one.
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("模型返回 %d：%s", e.StatusCode, e.Body)
}

// NeedsHuman reports whether retrying with the same key is pointless. 401/402/
// 403 are authentication, payment or permission failures; a 429 is treated the
// same only when the provider says the *quota* is gone. A plain 429 is a rate
// limit and should be retried after a pause, not parked.
//
// The distinction is read from the provider's structured error fields first:
// vendors disagree on wording (MiniMax sends code USAGE_LIMIT_EXCEEDED with
// "daily usage limit exceeded"), and a flat keyword scan missed exactly the
// error this project hit in production.
func (e *APIError) NeedsHuman() bool {
	switch e.StatusCode {
	case 401, 402, 403:
		return true
	case 429:
		return quotaExhausted(e.Body)
	}
	return false
}

// quotaExhausted reads the vendor error envelope: OpenAI-style
// {"error":{"type":"insufficient_quota"}}, Anthropic-style
// {"error":{"type":"..."}} and MiniMax-style {"code":"USAGE_LIMIT_EXCEEDED"}.
// The textual fallback exists for plain-text gateways; its markers stay narrow
// so "rate limit exceeded" is not mistaken for "quota exhausted".
func quotaExhausted(body string) bool {
	var env struct {
		Code  string `json:"code"`
		Type  string `json:"type"`
		Error struct {
			Code string `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(body), &env) == nil {
		for _, v := range []string{env.Code, env.Type, env.Error.Code, env.Error.Type} {
			switch strings.ToLower(strings.TrimSpace(v)) {
			case "insufficient_quota", "usage_limit_exceeded", "daily_limit_exceeded",
				"quota_exceeded", "billing_hard_limit_reached", "billing_not_active":
				return true
			}
		}
	}
	low := strings.ToLower(body)
	for _, marker := range []string{
		"insufficient_quota", "usage_limit", "daily_limit", "quota_exceeded",
		"billing", "credit", "余额", "额度",
	} {
		if strings.Contains(low, marker) {
			return true
		}
	}
	return false
}
