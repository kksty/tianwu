package decider

import (
	"testing"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// completeGoal stamps a goal with one reviewable, currently proven fact. The
// only premise mode forbids a bare evidence id with no verified assertion.
func completeGoal(t *testing.T, st *store.Store, goalID, evidenceID string) model.Fact {
	t.Helper()
	f, err := st.AddFact(model.Fact{Kind: "vuln", Title: "fixture support", EvidenceIDs: []string{evidenceID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReviewFact(model.Review{FactID: f.ID, Verdict: "pass", VerifierRun: "run-fixture"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PinGoal(goalID, evidenceID, "fixture complete", []string{f.ID}); err != nil {
		t.Fatal(err)
	}
	return f
}
