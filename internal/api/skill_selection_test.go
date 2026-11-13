package api

import (
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/model"
)

func TestDispatchRefusesAnIncompleteSkillSelectionBeforeRunningExecutor(t *testing.T) {
	ex := &scriptedExec{}
	s, in := newScriptedServer(t, ex)
	_, err := s.Store.UpdateIntent(in.ID, func(in *model.Intent) { in.Skills = []string{"missing-selected-skill"} })
	if err != nil {
		t.Fatal(err)
	}
	runID, err := s.StartRun(in.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The shared run helper waits for durable settlement, not a sleep.
	waitRun(t, s.Project, runID)
	run, ok := s.Store.GetRun(runID)
	if !ok || run.Status != model.RunFailed || !strings.Contains(run.ExitReason, "准备技能失败") {
		t.Fatalf("run without its selected contract = %+v", run)
	}
}
