package modelclient

import (
	"net/http"
	"net/url"
	"strings"
)

// IsOpenCodeEndpoint identifies the service, not its billing mode. Go and
// metered paths must stay distinct; selecting one never rewrites the other.
func IsOpenCodeEndpoint(base string) bool {
	u, err := url.Parse(strings.TrimSpace(base))
	return err == nil && u.Scheme == "https" && strings.EqualFold(u.Hostname(), "opencode.ai")
}

// NormalizeOpenCodeBase accepts a pasted chat URL while preserving its full
// subscription or metered prefix. Other gateways keep their existing contract.
func NormalizeOpenCodeBase(base string) string {
	if !IsOpenCodeEndpoint(base) {
		return base
	}
	return strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(base), "/"), "/chat/completions")
}

// ApplyEndpointHeaders carries a caller-owned conversation identity. It must
// remain stable across turns/retries and must never be derived from a key.
func ApplyEndpointHeaders(req *http.Request, sessionID string) {
	if !IsOpenCodeEndpoint(req.URL.String()) {
		return
	}
	req.Header.Set("User-Agent", "tianwu/0.x")
	if sessionID != "" {
		req.Header.Set("x-opencode-session", sessionID)
	}
}
