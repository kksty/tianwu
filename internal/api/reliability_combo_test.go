package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/project"
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
	premise, err := f.p.Store.AddFact(model.Fact{
		IntentID: source.ID, Kind: "vuln", Title: "premise",
		Summary: "old premise", Origin: model.OriginDeclared,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err != nil {
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

	runID, err := f.srv.StartRun(f.ident.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool { return len(ex.prompts) > 0 })
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
// different moments. The freeze transaction re-reads the effective policy and
// the current claims, so a policy flip in between cannot admit an unreviewed
// premise; the run records which policy actually applied.
func TestPolicyFlipBetweenAdmissionAndFreezeIsRecheckedAtFreeze(t *testing.T) {
	s, p := newBindingServer(t)
	premise, err := p.Store.AddFact(model.Fact{Kind: "vuln", Title: "unreviewed premise"})
	if err != nil {
		t.Fatal(err)
	}
	in, err := p.Store.CreateIntent(model.Intent{Title: "policy race", FromFactIDs: []string{premise.ID}})
	if err != nil {
		t.Fatal(err)
	}
	// 1) Under the current (standard) policy the API pre-check succeeds.
	if got := s.premisePolicy(); got != project.PremisePolicyStandard {
		t.Fatalf("default policy = %q", got)
	}
	if err := s.checkPremiseRefs([]string{premise.ID}); err != nil {
		t.Fatalf("standard admission refused the premise: %v", err)
	}
	_, run, err := p.Store.StartIntentRun(in.ID, "local", "", model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	// 2) The operator flips to strict before the dispatch goroutine freezes.
	if _, err := p.SetPremisePolicy(project.PremisePolicyStrict); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Store.PrepareRunInput(run.ID, in, s.premisePolicy()); err == nil {
		t.Fatal("freeze accepted a premise admitted under the previous policy")
	}
	if saved, _ := p.Store.GetRun(run.ID); saved.PremisePolicy != "" {
		t.Fatalf("a refused freeze wrote a policy record: %q", saved.PremisePolicy)
	}
	// 3) Flipping back admits it, and the row records the policy that applied.
	if _, err := p.SetPremisePolicy(project.PremisePolicyStandard); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Store.PrepareRunInput(run.ID, in, s.premisePolicy()); err != nil {
		t.Fatal(err)
	}
	saved, _ := p.Store.GetRun(run.ID)
	if saved.PremisePolicy != "standard" || saved.PremiseClaimVersions[premise.ID] != premise.ClaimVersion {
		t.Fatalf("freeze provenance = %+v", saved)
	}
}
