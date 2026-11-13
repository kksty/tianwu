package api

import (
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/model"
)

// --max-workers is an instance cap: the hub supplies GlobalRunning, and a new
// dispatch is refused once that count reaches the cap. Purpose runs are not
// workers and never appear in that count (RunningIntentCount filters them), so
// every newly started intent run must pass this gate.
func TestInstanceMaxWorkersRejectsNewDispatch(t *testing.T) {
	s, p := projectTierServer(t, nil)
	s.MaxWorkers = 2
	running := 2
	s.GlobalRunning = func() int { return running }
	in, err := p.Store.CreateIntent(model.Intent{Title: "容量测试", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartRun(in.ID); err == nil || !strings.Contains(err.Error(), "--max-workers") {
		t.Fatalf("cap reached started a run: %v", err)
	}
	// Below the cap a dispatch is accepted.
	running = 1
	runID, err := s.StartRun(in.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, p, runID)
	// A 0 cap means uncapped: the hook is present but never consulted.
	s.MaxWorkers = 0
	s.GlobalRunning = func() int { return 99 }
	in2, err := p.Store.CreateIntent(model.Intent{Title: "容量测试2", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	runID2, err := s.StartRun(in2.ID)
	if err != nil {
		t.Fatalf("0 cap refused a run: %v", err)
	}
	waitRun(t, p, runID2)
}
