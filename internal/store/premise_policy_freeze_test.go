package store

import (
	"testing"

	"github.com/kksty/tianwu/internal/model"
)

// F4: the verified_only check and the actual input freeze are one decision. The
// run records the policy its frozen input was admitted under, and a later
// project update cannot retroactively rewrite that record.
func TestPremisePolicyIsFrozenWithTheInput(t *testing.T) {
	s := open(t)
	e, err := s.AddEvidence(model.Evidence{URI: "proof.txt"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.AddFact(model.Fact{Kind: "vuln", Title: "premise", EvidenceIDs: []string{e.ID}})
	if err != nil {
		t.Fatal(err)
	}
	in, err := s.CreateIntent(model.Intent{Title: "child", FromFactIDs: []string{f.ID}})
	if err != nil {
		t.Fatal(err)
	}
	_, run, err := s.StartIntentRun(in.ID, "local", "", model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	// The unverified premise is refused by the freeze transaction itself, not
	// only by an earlier API-side check.
	if _, err := s.PrepareRunInput(run.ID, in, model.PremiseVerifiedOnly); err == nil {
		t.Fatal("freeze accepted an unverified vuln premise")
	}
	if saved, _ := s.GetRun(run.ID); saved.PremisePolicy != "" {
		t.Fatalf("a refused freeze wrote a policy record: %q", saved.PremisePolicy)
	}

	// Once the current claim carries a proof receipt, the run is admitted and
	// records the only policy.
	if _, err := s.ReviewFact(model.Review{FactID: f.ID, Verdict: "pass", VerifierRun: "run-1"}); err != nil {
		t.Fatal(err)
	}
	in2, err := s.CreateIntent(model.Intent{Title: "verified child", FromFactIDs: []string{f.ID}})
	if err != nil {
		t.Fatal(err)
	}
	_, run2, err := s.StartIntentRun(in2.ID, "local", "", model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := s.PrepareRunInput(run2.ID, in2, model.PremiseVerifiedOnly)
	if err != nil {
		t.Fatal(err)
	}
	saved, _ := s.GetRun(run2.ID)
	if saved.PremisePolicy != model.PremiseVerifiedOnly {
		t.Fatalf("run policy = %q", saved.PremisePolicy)
	}
	if saved.PremiseClaimVersions[f.ID] != frozen.Facts[0].ClaimVersion {
		t.Fatalf("frozen claim version mismatch: run=%v input=%v", saved.PremiseClaimVersions, frozen.Facts[0].ClaimVersion)
	}
	if again, _ := s.GetRun(run2.ID); again.PremisePolicy != model.PremiseVerifiedOnly {
		t.Fatalf("audit record changed with the caller: %q", again.PremisePolicy)
	}
}
