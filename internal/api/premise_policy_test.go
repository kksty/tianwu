package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/project"
)

func TestPremisePolicyEndpointIsOperatorOnlyAndValidated(t *testing.T) {
	s, _ := newBindingServer(t)
	rec := call(t, s, http.MethodGet, "/api/premise-policy", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"premise_policy":"standard"`) {
		t.Fatalf("default policy = %d %s", rec.Code, rec.Body.String())
	}
	rec = call(t, s, http.MethodPut, "/api/premise-policy", map[string]any{"premise_policy": "strict"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"effective":"strict"`) {
		t.Fatalf("set strict = %d %s", rec.Code, rec.Body.String())
	}
	rec = call(t, s, http.MethodPut, "/api/premise-policy", map[string]any{"premise_policy": "sometimes"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "premise_policy_invalid") {
		t.Fatalf("invalid policy = %d %s", rec.Code, rec.Body.String())
	}
	if got := s.premisePolicy(); got != project.PremisePolicyStrict {
		t.Fatalf("effective policy after invalid set = %q", got)
	}
}

// Standard mode keeps the existing rule (the premise must exist and not be
// rejected); strict mode additionally requires a vuln/cred premise to be
// verified at its current claim version. Creation fails early so a direction is
// not queued with a premise the ledger will refuse later.
func TestStrictPremisePolicyGatesCreationAndDispatch(t *testing.T) {
	s, p := newBindingServer(t)
	fact, err := p.Store.AddFact(model.Fact{IntentID: "in_other", Kind: "vuln", Title: "越权", Summary: "unreviewed"})
	if err != nil {
		t.Fatal(err)
	}
	if rec := call(t, s, http.MethodPost, "/api/intents", map[string]any{
		"title": "standard direction", "from_fact_ids": []string{fact.ID},
	}); rec.Code != http.StatusCreated {
		t.Fatalf("standard creation = %d %s", rec.Code, rec.Body.String())
	}

	if _, err := p.SetPremisePolicy(project.PremisePolicyStrict); err != nil {
		t.Fatal(err)
	}
	rec := call(t, s, http.MethodPost, "/api/intents", map[string]any{
		"title": "strict direction", "from_fact_ids": []string{fact.ID},
	})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "premise_unverified") {
		t.Fatalf("strict creation = %d %s", rec.Code, rec.Body.String())
	}
	if _, err := p.Store.ReviewFact(model.Review{
		FactID: fact.ID, Verdict: "pass",
		ClaimVersion: fact.ClaimVersion, ClaimHash: fact.ClaimHash,
	}); err != nil {
		t.Fatal(err)
	}
	rec = call(t, s, http.MethodPost, "/api/intents", map[string]any{
		"title": "strict reviewed direction", "from_fact_ids": []string{fact.ID},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("strict creation after review = %d %s", rec.Code, rec.Body.String())
	}
	// A claim revision resets the verdict; dispatch then refuses the direction
	// until the new assertion is reviewed.
	if _, err := p.Store.UpdateFact(fact.ID, func(f *model.Fact) { f.Summary = "revised" }); err != nil {
		t.Fatal(err)
	}
	var created model.Intent
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	run := call(t, s, http.MethodPost, "/api/intents/"+created.ID+"/run", nil)
	if run.Code != http.StatusConflict || !strings.Contains(run.Body.String(), "严格前提模式") {
		t.Fatalf("strict dispatch = %d %s", run.Code, run.Body.String())
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
