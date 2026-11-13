package decider

import (
	"github.com/kksty/tianwu/internal/model"
	"testing"
)

func TestReliabilityStaleCompletionReceiptMustRequireRejudgment(t *testing.T) {
	st := newStore(t)
	ev, err := st.AddEvidence(model.Evidence{URI: "fixture.txt"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := st.AddFact(model.Fact{Title: "support", EvidenceIDs: []string{ev.ID}, Summary: "original assertion"})
	if err != nil {
		t.Fatal(err)
	}
	g, err := st.AddGoal(model.Goal{Statement: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.PinGoal(g.ID, ev.ID, "original assertion satisfied the goal", []string{f.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err = st.UpdateFact(f.ID, func(f *model.Fact) { f.Summary = "changed assertion" }); err != nil {
		t.Fatal(err)
	}
	if !st.Goals()[0].ReceiptStale {
		t.Fatal("fixture has no stale receipt")
	}
	r := NewRunner(st, nil, "")
	if r.workFinished() {
		t.Fatal("stale completion receipt is displayed but ignored by completion/attention logic; project may auto-pause before rejudgment")
	}
}
