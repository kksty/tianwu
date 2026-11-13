package api

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/project"
	"github.com/kksty/tianwu/internal/store"
)

func TestSchedulerCapacityUsesProjectMaxRuns(t *testing.T) {
	for _, tc := range []struct {
		name              string
		instance, project int
		want              bool
	}{
		{"raised", 1, 2, true},
		{"uncapped", 1, 0, true},
		{"lowered", 3, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newBindingServer(t)
			s.MaxRuns = tc.instance
			s.Project = &project.Project{Manifest: project.Manifest{MaxRuns: &tc.project}}
			if _, err := s.Store.AddRun(model.Run{IntentID: "in_busy"}); err != nil {
				t.Fatal(err)
			}
			if got := s.hasCapacity(); got != tc.want {
				t.Fatalf("capacity = %v, want %v", got, tc.want)
			}
		})
	}
}

// The console's per-project cap wins over --max-runs: an override of 1 must
// refuse a second direction even though the instance default is 3.
func TestProjectMaxRunsOverridesInstanceDefault(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	ex := &blockingExec{}
	s := New(st, ex, dir, filepath.Join(dir, "cases"))
	s.MaxRuns = 3
	one := 1
	s.Project = &project.Project{Manifest: project.Manifest{
		MaxRuns:         &one,
		Nodes:           []string{"local"},
		DefaultNode:     "local",
		NodesConfigured: true,
	}}
	defer func() {
		s.CancelAll()
		s.WaitIdle(5 * time.Second)
		time.Sleep(100 * time.Millisecond)
	}()

	first, _ := st.CreateIntent(model.Intent{Title: "第一个"})
	second, _ := st.CreateIntent(model.Intent{Title: "第二个"})
	if _, err := s.StartRun(first.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool { return len(ex.requests()) == 1 })
	if _, err := s.StartRun(second.ID); err == nil || !strings.Contains(err.Error(), "并发") {
		t.Fatalf("second dispatch = %v, want the project override to refuse it", err)
	}
}

// 0 is a deliberate "uncapped" override, not "unset": it must lift the
// instance default rather than fall back to it.
func TestZeroProjectMaxRunsIsUncapped(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	ex := &blockingExec{}
	s := New(st, ex, dir, filepath.Join(dir, "cases"))
	s.MaxRuns = 1
	zero := 0
	s.Project = &project.Project{Manifest: project.Manifest{
		MaxRuns:         &zero,
		Nodes:           []string{"local"},
		DefaultNode:     "local",
		NodesConfigured: true,
	}}
	defer func() {
		s.CancelAll()
		s.WaitIdle(5 * time.Second)
		time.Sleep(100 * time.Millisecond)
	}()

	first, _ := st.CreateIntent(model.Intent{Title: "第一个"})
	second, _ := st.CreateIntent(model.Intent{Title: "第二个"})
	if _, err := s.StartRun(first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartRun(second.ID); err != nil {
		t.Fatalf("a 0 override must be uncapped: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool { return len(ex.requests()) == 2 })
}
