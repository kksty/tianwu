package store

import (
	"testing"

	"github.com/kksty/tianwu/internal/model"
)

func TestDispatchInputAndRecordedVersionsUseSameObservation(t *testing.T) {
	s := open(t)
	e, _ := s.AddEvidence(model.Evidence{URI: "proof.txt"})
	f, _ := s.AddFact(model.Fact{Kind: "vuln", Title: "premise", Summary: "first", EvidenceIDs: []string{e.ID}})
	if _, err := s.ReviewFact(model.Review{FactID: f.ID, Verdict: "pass", VerifierRun: "run-1"}); err != nil {
		t.Fatal(err)
	}
	in, _ := s.CreateIntent(model.Intent{Title: "task", FromFactIDs: []string{f.ID}})
	_, run, _ := s.StartIntentRun(in.ID, "local", "", model.Run{})
	db, err := s.PrepareRunInput(run.ID, in, model.PremiseVerifiedOnly)
	if err != nil {
		t.Fatal(err)
	}
	_, version, _ := s.FactVersion(f.ID)
	saved, _ := s.GetRun(run.ID)
	if saved.PremiseVersions[f.ID] != version || db.Facts[0].Summary != "first" {
		t.Fatal("input and provenance differ")
	}
	_, _ = s.UpdateFact(f.ID, func(f *model.Fact) { f.Summary = "second" })
	if db.Facts[0].Summary != "first" {
		t.Fatal("frozen input changed")
	}
	_, _ = s.UpdateFact(f.ID, func(f *model.Fact) { f.Status = "rejected" })
	if _, err = s.PrepareRunInput(run.ID, in, model.PremiseVerifiedOnly); err == nil {
		t.Fatal("rejected premise dispatched")
	}
}

func TestDispatchPreparationRefusesAnIntentOwnedByAnotherRun(t *testing.T) {
	s := open(t)
	in, _ := s.CreateIntent(model.Intent{Title: "task"})
	_, run, _ := s.StartIntentRun(in.ID, "local", "", model.Run{})
	_, _ = s.UpdateIntent(in.ID, func(in *model.Intent) { in.RunID = "replacement" })
	if _, err := s.PrepareRunInput(run.ID, in, model.PremiseVerifiedOnly); err == nil {
		t.Fatal("superseded run prepared an input")
	}
}

func TestVerifiedOnlyPreparationRechecksTheActualClaim(t *testing.T) {
	s := open(t)
	e, err := s.AddEvidence(model.Evidence{URI: "proof.txt"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.AddFact(model.Fact{Kind: "vuln", Title: "premise", Summary: "first", EvidenceIDs: []string{e.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviewFact(model.Review{FactID: f.ID, Verdict: "pass", VerifierRun: "run-1"}); err != nil {
		t.Fatal(err)
	}
	in, _ := s.CreateIntent(model.Intent{Title: "task", FromFactIDs: []string{f.ID}})
	_, run, _ := s.StartIntentRun(in.ID, "local", "", model.Run{})
	if _, err := s.UpdateFact(f.ID, func(f *model.Fact) { f.Summary = "unreviewed replacement" }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PrepareRunInput(run.ID, in, model.PremiseVerifiedOnly); err == nil {
		t.Fatal("verified_only dispatch accepted an unreviewed replacement")
	}
	if _, err := s.PrepareRunInput(run.ID, in, model.PremiseVerifiedOnly); err == nil {
		t.Fatal("the only mode accepted an unreviewed replacement")
	}
}

func TestGoalReceiptPinsClaimsAndRejectsRefutedSupport(t *testing.T) {
	s := open(t)
	e, _ := s.AddEvidence(model.Evidence{URI: "proof.txt"})
	f := verifiedSupport(t, s, e.ID, "support")
	g, _ := s.AddGoal(model.Goal{Statement: "goal"})
	pinned, err := s.PinGoal(g.ID, e.ID, "observed evidence satisfies the goal", []string{f.ID})
	if err != nil {
		t.Fatal(err)
	}
	if pinned.FromClaimVersions[f.ID] != f.ClaimVersion {
		t.Fatal("claim receipt missing")
	}
	pinned.FromClaimVersions[f.ID] = 999
	if s.Goals()[0].FromClaimVersions[f.ID] != f.ClaimVersion {
		t.Fatal("returned receipt aliases store")
	}
	if _, err = s.ReopenGoal(g.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.UpdateFact(f.ID, func(f *model.Fact) { f.Status = "rejected" }); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PinGoal(g.ID, e.ID, "refuted support", []string{f.ID}); err == nil {
		t.Fatal("refuted support accepted")
	}
}
