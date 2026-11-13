package api

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/evidence"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/project"
	"github.com/kksty/tianwu/internal/verifier"
)

func proofPolicyFixture(t *testing.T) (*Server, *project.Project, model.Fact, *reviewProbeExec) {
	t.Helper()
	s, p := newBindingServer(t)
	if _, err := p.SetPremisePolicy(project.PremisePolicyVerifiedOnly); err != nil {
		t.Fatal(err)
	}
	s.Exec = reliabilityFinishOnlyExec{}
	ex := &reviewProbeExec{}
	s.Verifier = verifier.New(p.Store, ex, s.CaseRoot)
	s.Verifier.Policy = s.premisePolicy
	s.Verifier.Track = s.TrackReviewRun
	owner, err := p.Store.CreateIntent(model.Intent{Title: "observation owner"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Store.UpdateIntent(owner.ID, func(i *model.Intent) { i.Status = model.StatusDone }); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(s.CaseRoot, owner.ID, "proof.txt")
	if err = os.MkdirAll(filepath.Dir(src), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(src, []byte("fixture observation"), 0600); err != nil {
		t.Fatal(err)
	}
	proof, err := evidence.Preserve(s.CaseRoot, src)
	if err != nil {
		t.Fatal(err)
	}
	ev, err := p.Store.AddEvidence(model.Evidence{IntentID: owner.ID, URI: proof.URI, Digest: proof.Digest, Size: proof.Size, Name: "proof.txt", SourceURI: owner.ID + "/proof.txt"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := p.Store.AddFact(model.Fact{IntentID: owner.ID, Kind: "host", Title: "fixture host observation", Summary: "a specific observed condition", EvidenceIDs: []string{ev.ID}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.BeginShutdown(); s.WaitIdle(2 * time.Second) })
	return s, p, f, ex
}

func TestVerifiedOnlyKeepsCandidatesAndAutomaticallyProvesNeededObservation(t *testing.T) {
	s, p, f, ex := proofPolicyFixture(t)
	if err := s.checkPremiseRefs([]string{f.ID}); err != nil {
		t.Fatal("a candidate must be recordable as a blocked dependency:", err)
	}
	in, err := p.Store.CreateIntent(model.Intent{Title: "consumer", FromFactIDs: []string{f.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if ready := s.readiness(p.Store.Snapshot(), in, s.premisePolicy()); ready.Ready {
		t.Fatal("preserved bytes were mistaken for proof")
	}
	s.SchedulePendingReviews()
	waitFor(t, 5*time.Second, func() bool { got, _ := p.Store.FactByID(f.ID); return got.Status == "verified" })
	ready := s.readiness(p.Store.Snapshot(), in, s.premisePolicy())
	if !ready.Ready || ready.Premises[0].ReviewID == "" || ready.Premises[0].Integrity != "intact" {
		t.Fatalf("missing proof-bound readiness: %+v", ready)
	}
	if ex.runs() != 1 {
		t.Fatalf("observation was reviewed %d times", ex.runs())
	}
	runID, err := s.StartRun(in.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool { r, _ := p.Store.GetRun(runID); return r.Status == model.RunDone })
	r, _ := p.Store.GetRun(runID)
	if r.PremisePolicy != model.PremiseVerifiedOnly || r.PremiseClaimVersions[f.ID] != f.ClaimVersion {
		t.Fatal("policy/claim input was not frozen")
	}
}

func TestVerifiedOnlyRejectsMissingReceiptTamperedBytesAndUnprovedCompletion(t *testing.T) {
	s, p, f, _ := proofPolicyFixture(t)
	in, _ := p.Store.CreateIntent(model.Intent{Title: "consumer", FromFactIDs: []string{f.ID}})
	// A display status is insufficient: admission needs the review receipt.
	if _, err := p.Store.UpdateFact(f.ID, func(f *model.Fact) { f.Status = "verified" }); err != nil {
		t.Fatal(err)
	}
	if got := s.readiness(p.Store.Snapshot(), in, s.premisePolicy()); got.Ready || !strings.Contains(got.Premises[0].Reason, "收据") {
		t.Fatalf("status-only claim admitted: %+v", got)
	}
	if _, err := p.Store.UpdateFact(f.ID, func(f *model.Fact) { f.Status = "unverified" }); err != nil {
		t.Fatal(err)
	}
	goal, _ := p.Store.AddGoal(model.Goal{Statement: "fixture goal"})
	if _, err := p.Store.PinGoal(goal.ID, f.EvidenceIDs[0], "reason", []string{f.ID}, model.PremiseVerifiedOnly); err == nil {
		t.Fatal("unproved goal accepted")
	}
	if _, err := s.Verifier.Run(context.Background(), f.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Store.PinGoal(goal.ID, f.EvidenceIDs[0], "independent proof", []string{f.ID}, model.PremiseVerifiedOnly); err != nil {
		t.Fatal(err)
	}
	ev, _ := p.Store.EvidenceByID(f.EvidenceIDs[0])
	if err := os.WriteFile(filepath.Join(s.CaseRoot, filepath.FromSlash(ev.URI)), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := s.readiness(p.Store.Snapshot(), in, s.premisePolicy()); got.Ready || got.Premises[0].Integrity != "invalid" {
		t.Fatalf("tampered input admitted: %+v", got)
	}
}

func TestVerifiedOnlyNeverMakesAnOrdinaryNoteARequiredPremise(t *testing.T) {
	s, p, _, _ := proofPolicyFixture(t)
	f, _ := p.Store.AddFact(model.Fact{Kind: "note", Title: "historical suggestion"})
	if err := s.checkPremiseRefs([]string{f.ID}); err == nil {
		t.Fatal("note became an actionable prerequisite")
	}
	// Notes remain present and readable; the policy never deletes them.
	if _, ok := p.Store.FactByID(f.ID); !ok {
		t.Fatal("candidate was deleted")
	}
}
