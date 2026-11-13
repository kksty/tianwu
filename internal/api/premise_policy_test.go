package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/project"
)

func TestPremisePolicyEndpointIsOperatorOnlyAndValidated(t *testing.T) {
	s, p := newBindingServer(t)
	rec := call(t, s, http.MethodGet, "/api/premise-policy", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"premise_policy":"standard"`) {
		t.Fatalf("default policy = %d %s", rec.Code, rec.Body.String())
	}
	rec = call(t, s, http.MethodPut, "/api/premise-policy", map[string]any{"premise_policy": "verified_only"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"effective":"verified_only"`) {
		t.Fatalf("set verified_only = %d %s", rec.Code, rec.Body.String())
	}
	rec = call(t, s, http.MethodPut, "/api/premise-policy", map[string]any{"premise_policy": "sometimes"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "premise_policy_invalid") {
		t.Fatalf("invalid policy = %d %s", rec.Code, rec.Body.String())
	}
	if got := s.premisePolicy(); got != project.PremisePolicyVerifiedOnly {
		t.Fatalf("effective policy after invalid set = %q", got)
	}
	// The removed middle mode is not writable any more. Rejecting it must leave
	// both the effective and the stored policy unchanged: a 400 is not a silent
	// downgrade to standard, and only the next explicit save rewrites the value.
	rec = call(t, s, http.MethodPut, "/api/premise-policy", map[string]any{"premise_policy": "strict"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "premise_policy_invalid") {
		t.Fatalf("set strict = %d %s", rec.Code, rec.Body.String())
	}
	if got := s.premisePolicy(); got != project.PremisePolicyVerifiedOnly {
		t.Fatalf("rejected strict write changed the effective policy: %q", got)
	}
	if p.PremisePolicy != project.PremisePolicyVerifiedOnly {
		t.Fatalf("rejected strict write changed the stored policy: %q", p.PremisePolicy)
	}
}

func TestTopLevelCreationValidatesPremiseExistence(t *testing.T) {
	s, p := newBindingServer(t)
	rec := call(t, s, http.MethodPost, "/api/intents", map[string]any{
		"title": "bad premise", "from_fact_ids": []string{"f_missing"},
	})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "fact_ref_missing") {
		t.Fatalf("missing premise = %d %s", rec.Code, rec.Body.String())
	}
	if len(p.Store.Snapshot().Intents) != 0 {
		t.Fatal("a rejected creation still wrote an intent")
	}
}
