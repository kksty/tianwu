package modelclient

import "testing"

func TestAPIErrorNeedsHuman(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"unauthorized", 401, "invalid api key", true},
		{"payment", 402, "insufficient quota", true},
		{"forbidden", 403, "model not allowed", true},
		{"quota 429", 429, `{"error":{"message":"insufficient_quota"}}`, true},
		{"chinese quota 429", 429, "额度已用完", true},
		// The exact shape this project hit with MiniMax: a 429 whose reason is a
		// daily cap, not a per-minute rate limit.
		{"minimax daily limit", 429, `{"code":"USAGE_LIMIT_EXCEEDED","message":"error: code=429 reason=\"DAILY_LIMIT_EXCEEDED\" message=\"daily usage limit exceeded\""}`, true},
		{"openai insufficient_quota type", 429, `{"error":{"type":"insufficient_quota","message":"You exceeded your current quota"}}`, true},
		{"rate limit 429", 429, "rate limit exceeded, retry later", false},
		{"server error", 500, "bad gateway", false},
		{"bad request", 400, "invalid json", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := &APIError{StatusCode: tc.status, Body: tc.body}
			if got := err.NeedsHuman(); got != tc.want {
				t.Fatalf("NeedsHuman(%d, %q) = %v, want %v", tc.status, tc.body, got, tc.want)
			}
		})
	}
}
