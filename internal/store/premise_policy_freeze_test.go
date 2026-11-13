package store

import (
	"testing"

	"github.com/kksty/tianwu/internal/model"
)

// F4: the strict check and the actual input freeze must be one decision. The
// run records which policy its frozen input was admitted under, and the newest
// policy cannot retroactively rewrite that record.
func TestPremisePolicyIsFrozenWithTheInput(t *testing.T) {
	s := open(t)
	f, err := s.AddFact(model.Fact{Kind: "vuln", Title: "premise"})
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
	if _, err := s.PrepareRunInput(run.ID, in, model.PremiseStrict); err == nil {
		t.Fatal("strict freeze accepted an unverified vuln premise")
	}
	// Standard mode admits it and records the policy that actually applied.
	frozen, err := s.PrepareRunInput(run.ID, in, model.PremiseStandard)
	if err != nil {
		t.Fatal(err)
	}
	saved, ok := s.GetRun(run.ID)
	if !ok || saved.PremisePolicy != "standard" {
		t.Fatalf("run did not record its premise policy: %+v", saved)
	}
	if saved.PremiseClaimVersions[f.ID] != frozen.Facts[0].ClaimVersion {
		t.Fatalf("frozen claim version mismatch: run=%v input=%v", saved.PremiseClaimVersions, frozen.Facts[0].ClaimVersion)
	}
	// The recorded policy describes the input, not whatever the project says now.
	if again, _ := s.GetRun(run.ID); again.PremisePolicy != "standard" {
		t.Fatalf("audit record changed with the caller: %q", again.PremisePolicy)
	}

	// Once the claim is reviewed, a new run can be admitted under strict and
	// records that too — the two rows stay distinguishable.
	if _, err := s.ReviewFact(model.Review{FactID: f.ID, Verdict: "pass"}); err != nil {
		t.Fatal(err)
	}
	in2, err := s.CreateIntent(model.Intent{Title: "strict child", FromFactIDs: []string{f.ID}})
	if err != nil {
		t.Fatal(err)
	}
	_, run2, err := s.StartIntentRun(in2.ID, "local", "", model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PrepareRunInput(run2.ID, in2, model.PremiseStrict); err != nil {
		t.Fatal(err)
	}
	saved2, _ := s.GetRun(run2.ID)
	if saved2.PremisePolicy != "strict" {
		t.Fatalf("strict run policy = %q", saved2.PremisePolicy)
	}
	if saved.PremisePolicy == saved2.PremisePolicy {
		t.Fatal("the two admissions collapsed into one policy record")
	}
}
