package api

import (
	"context"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/model"
)

func TestPausedProfileRefusesExplicitAndDefaultWorkerRuns(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		s, p := newBindingServer(t)
		s.LocalNode = "local"
		s.Profiles = func() ([]ProfileInfo, string, error) {
			return []ProfileInfo{{ID: "paused", Paused: true, Runnable: true}}, "paused", nil
		}
		in := model.Intent{Title: "fixture"}
		if explicit {
			in.Profile = "paused"
		}
		created, err := p.Store.CreateIntent(in)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.StartRun(created.ID); err == nil || !strings.Contains(err.Error(), "已暂停") {
			t.Fatalf("dispatch error=%v", err)
		}
		if len(p.Store.Runs()) != 0 {
			t.Fatal("paused profile created a run")
		}
	}
}

func TestPausedDefaultRefusesCommanderBeforeCreatingRun(t *testing.T) {
	s, ex, st := newCommanderServer(t)
	s.LocalNode = "local"
	paused := true
	s.Profiles = func() ([]ProfileInfo, string, error) {
		return []ProfileInfo{{ID: "default", Paused: paused, Runnable: !paused}}, "default", nil
	}
	if _, _, err := s.StartCommander(context.Background(), "fixture", false); err == nil || !strings.Contains(err.Error(), "已暂停") {
		t.Fatalf("commander error=%v", err)
	}
	if ex.runs != 0 || len(st.Runs()) != 0 {
		t.Fatal("paused default called executor")
	}
	paused = false
	if _, _, err := s.StartCommander(context.Background(), "fixture", false); err != nil {
		t.Fatal(err)
	}
	if ex.runs != 1 {
		t.Fatal("resumed default did not admit commander")
	}
}
