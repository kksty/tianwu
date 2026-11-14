package ledger

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestCompleteGoalRequiresEvidence(t *testing.T) {
	st := newStore(t)
	f, _ := st.AddFact(model.Fact{Kind: "note", Title: "无证据"})
	if _, err := CompleteGoal(st, "", []string{f.ID}, "范围已覆盖"); err == nil || !strings.Contains(err.Error(), "每条事实都必须有自己的证据") {
		t.Fatalf("err = %v", err)
	}
}

// A receipt's citations are its proof: citing an unproven observation next to a
// proven one would smuggle it into the page as "basis" (D97).
func TestCompleteGoalRejectsCitationWithoutEvidence(t *testing.T) {
	st := newStore(t)
	ev, _ := st.AddEvidence(model.Evidence{Name: "p.txt", URI: "p.txt"})
	proven, _ := st.AddFact(model.Fact{Kind: "vuln", Title: "有证据", Status: "verified", EvidenceIDs: []string{ev.ID}})
	guess, _ := st.AddFact(model.Fact{Kind: "note", Title: "只是观察"})
	g, _ := st.AddGoal(model.Goal{Statement: "覆盖认证面"})
	if _, err := CompleteGoal(st, g.ID, []string{proven.ID, guess.ID}, "范围已覆盖"); err == nil || !strings.Contains(err.Error(), guess.ID) {
		t.Fatalf("err = %v", err)
	}
}

// A stamp is a decision the page has to explain later; without the reason the
// console can only repeat "已达成", which is the operator's next question (D96).
func TestCompleteGoalRequiresReason(t *testing.T) {
	st := newStore(t)
	ev, _ := st.AddEvidence(model.Evidence{Name: "p.txt", URI: "p.txt"})
	f, _ := st.AddFact(model.Fact{Kind: "note", Title: "观察", EvidenceIDs: []string{ev.ID}})
	g, _ := st.AddGoal(model.Goal{Statement: "留下证据"})
	if _, err := CompleteGoal(st, g.ID, []string{f.ID}, "   "); err == nil || !strings.Contains(err.Error(), "必须写 note") {
		t.Fatalf("err = %v", err)
	}
}

// D66's "cited vuln/cred needs a verified fact" gate is revoked (D97): the
// commander judges the goal against its own statement + quantified target, and
// verification stays the report's delivery gate. The engine must not turn that
// judgement into a count.
func TestCompleteGoalRejectsUnverifiedCitation(t *testing.T) {
	st := newStore(t)
	ev, _ := st.AddEvidence(model.Evidence{Name: "p.txt", URI: "p.txt"})
	f, _ := st.AddFact(model.Fact{Kind: "vuln", Title: "未复核但有实测", Status: "unverified", EvidenceIDs: []string{ev.ID}})
	g, _ := st.AddGoal(model.Goal{Statement: "拿到 shell", QuantifiedTarget: "口径：认证面有一条实测结论"})
	if _, err := CompleteGoal(st, g.ID, []string{f.ID}, "未复核结论不能直接盖章"); err == nil {
		t.Fatal("unverified citation accepted as completion proof")
	}
}

func TestCompleteGoalPinsVerifiedVulnWithReceipt(t *testing.T) {
	st := newStore(t)
	ev, _ := st.AddEvidence(model.Evidence{Name: "p.txt", URI: "p.txt"})
	f, _ := st.AddFact(model.Fact{Kind: "vuln", Title: "已复核", Status: "unverified", EvidenceIDs: []string{ev.ID}})
	if _, err := st.ReviewFact(model.Review{FactID: f.ID, Verdict: "pass", VerifierRun: "run-test"}); err != nil {
		t.Fatal(err)
	}
	g, _ := st.AddGoal(model.Goal{Statement: "拿到 shell"})
	pinned, err := CompleteGoal(st, g.ID, []string{f.ID}, "复核通过：验证码可跳过，实测可反复试口令")
	if err != nil {
		t.Fatal(err)
	}
	if !pinned.Met || pinned.EvidenceID != ev.ID {
		t.Fatalf("goal = %+v", pinned)
	}
	if pinned.Note != "复核通过：验证码可跳过，实测可反复试口令" || len(pinned.From) != 1 || pinned.From[0] != f.ID {
		t.Fatalf("receipt = %+v", pinned)
	}
	if pinned.CompletedAt == nil {
		t.Fatal("completed_at missing")
	}
}

func TestCompleteGoalRejectsUnknownGoal(t *testing.T) {
	st := newStore(t)
	ev, _ := st.AddEvidence(model.Evidence{Name: "p.txt", URI: "p.txt"})
	f, _ := st.AddFact(model.Fact{Kind: "note", Title: "观察", EvidenceIDs: []string{ev.ID}})
	if _, err := CompleteGoal(st, "g_gone", []string{f.ID}, "范围已覆盖"); err == nil || !strings.Contains(err.Error(), "不存在的目标") {
		t.Fatalf("err = %v", err)
	}
}

func TestNominatePlaybookRequiresObservation(t *testing.T) {
	st := newStore(t)
	if _, _, err := NominatePlaybook(st, model.Playbook{Title: "x", When: "y"}); err == nil || !strings.Contains(err.Error(), "已有事实") {
		t.Fatalf("err = %v", err)
	}
}

func TestNominatePlaybookWritesCandidate(t *testing.T) {
	st := newStore(t)
	f, _ := st.AddFact(model.Fact{Kind: "note", Title: "观察"})
	pb, changed, err := NominatePlaybook(st, model.Playbook{Title: "x", When: "y", FromFactIDs: []string{f.ID}})
	if err != nil || !changed {
		t.Fatalf("pb = %+v changed=%v err=%v", pb, changed, err)
	}
	if pb.Status != model.PlaybookCandidate || pb.Origin != model.PlaybookFromCommander {
		t.Fatalf("pb = %+v", pb)
	}
}

func TestNominatePlaybookDoesNotOverwriteConfirmed(t *testing.T) {
	st := newStore(t)
	f, _ := st.AddFact(model.Fact{Kind: "note", Title: "观察"})
	if _, _, err := st.UpsertPlaybook(model.Playbook{
		Title: "x", When: "y", Origin: model.PlaybookFromExecutor,
		Status: model.PlaybookConfirmed, FromFactIDs: []string{f.ID},
	}); err != nil {
		t.Fatal(err)
	}
	_, changed, err := NominatePlaybook(st, model.Playbook{Title: "x", When: "y", FromFactIDs: []string{f.ID}})
	if err != nil || changed {
		t.Fatalf("changed=%v err=%v, want a refused overwrite", changed, err)
	}
	books := st.Playbooks()
	if len(books) != 1 || books[0].Status != model.PlaybookConfirmed {
		t.Fatalf("books = %+v", books)
	}
}
