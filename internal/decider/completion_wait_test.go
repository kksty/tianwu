package decider

import (
	"github.com/kksty/tianwu/internal/model"
	"testing"
)

func TestExplicitWaitPreventsCompletionPause(t *testing.T) {
	st := newStore(t)
	in, err := st.CreateIntent(model.Intent{Title: "waiting for an explicit receipt"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.UpdateIntent(in.ID, func(in *model.Intent) { in.Status = model.StatusWaiting; in.Wait = &model.Wait{} })
	if err != nil {
		t.Fatal(err)
	}
	g, err := st.AddGoal(model.Goal{Statement: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	e, err := st.AddEvidence(model.Evidence{URI: "fixture.txt"})
	if err != nil {
		t.Fatal(err)
	}
	completeGoal(t, st, g.ID, e.ID)
	r := NewRunner(st, nil, "")
	if r.workFinished() {
		t.Fatal("project considered finished while direction is explicitly waiting")
	}
}
