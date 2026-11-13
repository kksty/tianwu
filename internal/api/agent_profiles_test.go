package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/collab"
)

// profileFixture is an agent server with a profile catalogue and one issued
// identity. It is separate from agentFixture because these handlers only need
// identity + Profiles, not intents or a live run.
type profileFixture struct {
	srv   *Server
	proj  string
	ident collab.Identity
}

func newProfileFixture(t *testing.T) *profileFixture {
	t.Helper()
	s, p := newBindingServer(t)
	s.LocalNode = "local"
	s.Profiles = func() ([]ProfileInfo, string, error) {
		return []ProfileInfo{
			{ID: "cheap", Name: "便宜档", Provider: "openai", Model: "cheap-model",
				ContextWindow: 32000, Revision: "rev-cheap", Runnable: true, Active: true},
			{ID: "strong", Name: "强档", Provider: "anthropic", Model: "strong-model",
				ContextWindow: 200000, Revision: "rev-strong", Runnable: true},
			{ID: "broken", Provider: "openai", Revision: "rev-broken"},
		}, "cheap", nil
	}
	s.Collab = collab.NewRegistry(time.Hour)
	ident, err := s.Collab.Issue(collab.Identity{
		ProjectID: p.ID, ParticipantID: collab.ParticipantID("in_profile_test"),
		IntentID: "in_profile_test", RunID: "r_profile_test", Generation: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &profileFixture{srv: s, proj: p.ID, ident: ident}
}

func (f *profileFixture) call(t *testing.T, withIdentity bool, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if withIdentity {
		req = req.WithContext(WithIdentity(req.Context(), f.ident))
	}
	rec := httptest.NewRecorder()
	f.srv.AgentHandler().ServeHTTP(rec, req)
	return rec
}

// The agent can see which profiles exist and which one is active, without the
// runner having to guess an id or a credential.
func TestAgentCanListRunnableProfilesWithoutCredentials(t *testing.T) {
	f := newProfileFixture(t)
	rec := f.call(t, true, "/api/model-profiles")
	if rec.Code != http.StatusOK {
		t.Fatalf("profiles: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Profiles  []ProfileInfo `json:"profiles"`
		Active    string        `json:"active"`
		LocalNode string        `json:"local_node"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Profiles) != 3 || resp.Profiles[0].ID != "cheap" ||
		!resp.Profiles[0].Runnable || resp.Profiles[2].Runnable {
		t.Fatalf("profiles = %+v", resp.Profiles)
	}
	if resp.Active != "cheap" || resp.LocalNode != "local" {
		t.Fatalf("active/local_node = %q/%q", resp.Active, resp.LocalNode)
	}
	body := rec.Body.String()
	for _, banned := range []string{"api_key", "api-key", "credential", "sk-"} {
		if strings.Contains(strings.ToLower(body), banned) {
			t.Fatalf("profile list exposed %q: %s", banned, body)
		}
	}
}

// The console route is gated by the console token. The participant mux must not
// answer a request that carries no identity just because the handler is reused.
func TestAgentProfileListRequiresIdentity(t *testing.T) {
	f := newProfileFixture(t)
	rec := f.call(t, false, "/api/model-profiles")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no identity: %d %s", rec.Code, rec.Body.String())
	}
}
