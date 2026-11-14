package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/evidence"
	"github.com/kksty/tianwu/internal/model"
)

// F5: a handoff snapshot records identities, not frozen prose. The brief is
// rebuilt after the dispatch input is frozen, so a premise corrected between
// "handoff written" and "new session started" is shown at its current version;
// a vanished premise is named as unusable instead of being silently repeated.
func TestHandoffBriefIsRebuiltFromTheFrozenPremise(t *testing.T) {
	f := newParentFixture(t)
	ex := &briefExec{}
	f.srv.Exec = ex
	t.Cleanup(func() { f.srv.CancelAll(); f.srv.WaitIdle(3 * time.Second) })

	// The premise was observed in another direction; the child cites it as a
	// necessary predecessor, which is the normal shape (citing an own fact would
	// be a graph cycle).
	source, err := f.p.Store.CreateIntent(model.Intent{Title: "source direction"})
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(f.p.CaseDir, "in_other", "proof.txt")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("handoff proof"), 0o644); err != nil {
		t.Fatal(err)
	}
	preserved, err := evidence.Preserve(f.p.CaseDir, src)
	if err != nil {
		t.Fatal(err)
	}
	ev, err := f.p.Store.AddEvidence(model.Evidence{
		IntentID: source.ID, URI: preserved.URI, Digest: preserved.Digest,
		Size: preserved.Size, Name: "proof.txt",
	})
	if err != nil {
		t.Fatal(err)
	}
	premise, err := f.p.Store.AddFact(model.Fact{
		IntentID: source.ID, Kind: "vuln", Title: "premise",
		Summary: "old premise", Origin: model.OriginDeclared, EvidenceIDs: []string{ev.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Store.ReviewFact(model.Review{FactID: premise.ID, Verdict: "pass", VerifierRun: "run-old"}); err != nil {
		t.Fatal(err)
	}
	// The handoff snapshot was written while the premise was OLD; only IDs are
	// carried. Then the premise is corrected before the replacement dispatches.
	now := time.Now()
	if _, err := f.p.Store.UpdateRun(f.run.ID, func(r *model.Run) {
		r.Status, r.EndedAt = model.RunDone, &now
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Store.UpdateIntent(f.ident.IntentID, func(i *model.Intent) {
		i.Status, i.RunID = model.StatusPending, ""
		i.FromFactIDs = []string{premise.ID}
		i.HandoffVersion = 1
		i.Handoff = &model.Handoff{
			FromRun: f.run.ID,
			Note:    "switch model",
			Facts:   []string{premise.ID, "f_vanished"},
			Evidence: []string{
				"in_other/proof.txt",
			},
		}
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Store.UpdateFact(premise.ID, func(fact *model.Fact) {
		fact.Summary = "new premise after handoff"
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Store.ReviewFact(model.Review{FactID: premise.ID, Verdict: "pass", VerifierRun: "run-new"}); err != nil {
		t.Fatal(err)
	}

	runID, err := f.srv.StartRun(f.ident.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool { return ex.count() > 0 })
	prompt := ex.promptAt(0)
	for _, want := range []string{
		"new premise after handoff",
		"断言 v2",
		"## 交接（新会话，换模型接班）",
		"f_vanished",
		"当前快照不存在",
		"../in_other/proof.txt",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("rebuilt handoff brief missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "old premise") {
		t.Fatalf("brief reused the handoff-time premise text:\n%s", prompt)
	}

	// A handoff starts a fresh session, so its actual brief must be archived and
	// reproducible: the delivered prompt and the file have to be identical.
	run, ok := f.p.Store.GetRun(runID)
	if !ok || run.BriefPath == "" {
		t.Fatalf("handoff run did not archive its brief: %+v", run)
	}
	archived, err := os.ReadFile(filepath.Join(f.p.CaseDir, filepath.FromSlash(run.BriefPath)))
	if err != nil {
		t.Fatalf("archived handoff brief: %v", err)
	}
	if string(archived) != prompt {
		t.Fatalf("archived handoff brief is not the dispatched prompt:\nfile=%q\nprompt=%q", archived, prompt)
	}
}

// F4: the friendly API-side premise check and the async input freeze are two
// different moments. The freeze transaction re-checks the current claim/proof,
// so a candidate registered while pending cannot start until it is verified.
func TestFreezeRechecksThePremiseProof(t *testing.T) {
	s, p := newBindingServer(t)
	e, err := p.Store.AddEvidence(model.Evidence{URI: "in_owner/proof.txt"})
	if err != nil {
		t.Fatal(err)
	}
	premise, err := p.Store.AddFact(model.Fact{Kind: "vuln", Title: "unreviewed premise", EvidenceIDs: []string{e.ID}})
	if err != nil {
		t.Fatal(err)
	}
	in, err := p.Store.CreateIntent(model.Intent{Title: "proof race", FromFactIDs: []string{premise.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.premisePolicy(); got != model.PremiseVerifiedOnly {
		t.Fatalf("policy = %q", got)
	}
	// Creating the direction is allowed while the reviewable premise is pending;
	// dispatch preparation is the gate.
	if err := s.checkPremiseRefs([]string{premise.ID}); err != nil {
		t.Fatalf("candidate admission refused: %v", err)
	}
	_, run, err := p.Store.StartIntentRun(in.ID, "local", "", model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Store.PrepareRunInput(run.ID, in, s.premisePolicy()); err == nil {
		t.Fatal("freeze accepted an unverified premise")
	}
	if saved, _ := p.Store.GetRun(run.ID); saved.PremisePolicy != "" {
		t.Fatalf("a refused freeze wrote a policy record: %q", saved.PremisePolicy)
	}
	if _, err := p.Store.ReviewFact(model.Review{FactID: premise.ID, Verdict: "pass", VerifierRun: "run-proof"}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Store.PrepareRunInput(run.ID, in, s.premisePolicy()); err != nil {
		t.Fatal(err)
	}
	if saved, _ := p.Store.GetRun(run.ID); saved.PremisePolicy != model.PremiseVerifiedOnly {
		t.Fatalf("frozen policy = %q", saved.PremisePolicy)
	}
}
