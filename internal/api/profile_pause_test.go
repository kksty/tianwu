package api

import (
	"context"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/host"
	"github.com/kksty/tianwu/internal/model"
)

func TestPausedDefaultSelectsAvailableProfile(t *testing.T) {
	for _, tc := range []struct {
		name     string
		profiles []ProfileInfo
		want     string
	}{
		{"catalogue order", []ProfileInfo{{ID: "default", Paused: true}, {ID: "other", Runnable: true}}, "other"},
		{"configured fallback first", []ProfileInfo{{ID: "default", Paused: true, Fallback: "backup"}, {ID: "other", Runnable: true}, {ID: "backup", Runnable: true}}, "backup"},
		{"skip unavailable fallback", []ProfileInfo{{ID: "default", Paused: true, Fallback: "backup"}, {ID: "backup", Runnable: true, Probed: true}, {ID: "other", Runnable: true}}, "other"},
		{"skip paused and incomplete", []ProfileInfo{{ID: "default", Paused: true}, {ID: "paused", Paused: true, Runnable: true}, {ID: "incomplete"}, {ID: "other", Runnable: true}}, "other"},
		{"missing fallback", []ProfileInfo{{ID: "default", Paused: true, Fallback: "missing"}, {ID: "other", Runnable: true}}, "other"},
		{"self fallback", []ProfileInfo{{ID: "default", Paused: true, Fallback: "default"}, {ID: "other", Runnable: true}}, "other"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{Profiles: func() ([]ProfileInfo, string, error) { return tc.profiles, "default", nil }}
			binding, switched, err := s.inheritedProfileBinding("")
			if err != nil || !switched || binding.ID != tc.want {
				t.Fatalf("binding=%+v switched=%t err=%v", binding, switched, err)
			}
			if _, err := s.profileBinding("default"); err == nil {
				t.Fatal("explicit paused selection fell through to another model")
			}
		})
	}
}

func TestPausedDefaultWorkerSwitchesWithoutChangingSavedBinding(t *testing.T) {
	for _, projectDefault := range []bool{false, true} {
		t.Run(map[bool]string{false: "instance", true: "project"}[projectDefault], func(t *testing.T) {
			profiles := testProfiles()
			profiles[0].Paused = true
			s, p := profileServer(t, profiles...)
			if projectDefault {
				s.ProjectProfiles = &ProfileDefaults{Worker: "fast"}
			}
			// A replacement uses its named host even if the paused boot host is down.
			s.LocalHostStatus = func() host.HostStatus { return host.HostStatus{} }
			ex := &recordExec{}
			s.Exec = ex
			in, err := p.Store.CreateIntent(model.Intent{Title: "fixture"})
			if err != nil {
				t.Fatal(err)
			}
			runID, err := s.StartRun(in.ID)
			if err != nil {
				t.Fatal(err)
			}
			waitRun(t, p, runID)
			run, _ := p.Store.GetRun(runID)
			wantSource := "default_fallback"
			if projectDefault {
				wantSource = "project_worker_fallback"
			}
			if run.Profile != "slow" || run.ResolvedProfile != "slow" || run.ProfileRevision != "rev-slow" || run.ProfileSource != wantSource || run.Provider != "anthropic" || run.ContextWindow != 64000 {
				t.Fatalf("replacement binding=%+v", run)
			}
			if req := ex.Request(); req.Profile != "slow" || req.Node != "local" {
				t.Fatalf("request=%+v", req)
			}
			stored, _ := p.Store.GetIntent(in.ID)
			if stored.Profile != "" || s.activeProfile() != "fast" {
				t.Fatal("replacement rewrote the intent or instance default")
			}
			profiles[0].Paused = false
			s.LocalHostStatus = nil
			next, _, err := s.planRun(model.Intent{}, "", "", "")
			if err != nil || next.ResolvedProfile != "fast" {
				t.Fatalf("resumed default=%+v err=%v", next, err)
			}
			if old, _ := p.Store.GetRun(runID); old.ResolvedProfile != "slow" {
				t.Fatal("resume rewrote historical binding")
			}
		})
	}
}

func TestPausedDefaultCommanderUsesReplacementHost(t *testing.T) {
	for _, projectDefault := range []bool{false, true} {
		t.Run(map[bool]string{false: "instance", true: "project"}[projectDefault], func(t *testing.T) {
			s, ex, st := newCommanderServer(t)
			s.LocalNode = "local"
			s.LocalHostStatus = func() host.HostStatus { return host.HostStatus{} }
			profiles := testProfiles()
			profiles[0].Paused = true
			s.Profiles = func() ([]ProfileInfo, string, error) { return profiles, "fast", nil }
			if projectDefault {
				s.ProjectProfiles = &ProfileDefaults{Strong: "fast"}
			}
			if err := s.CheckCommanderHost(); err != nil {
				t.Fatal(err)
			}
			if name, window := s.CommanderModelInfo(); name != "slow-model" || window != 64000 {
				t.Fatalf("commander model=%s window=%d", name, window)
			}
			ex.resumable = true
			if _, _, err := s.StartCommander(context.Background(), "fixture", true); err != nil {
				t.Fatal(err)
			}
			if ex.req.Profile != "slow" || ex.req.Resume || !strings.HasPrefix(ex.req.Prompt, "/skill:commander ") {
				t.Fatalf("replacement continued the wrong host: %+v", ex.req)
			}
			runs := st.Runs()
			wantSource := "default_fallback"
			if projectDefault {
				wantSource = "commander_strong_fallback"
			}
			if len(runs) != 1 || runs[0].ResolvedProfile != "slow" || runs[0].ProfileSource != wantSource || runs[0].ProfileRevision != "rev-slow" {
				t.Fatalf("runs=%+v", runs)
			}
			profiles[0].Paused = false
			s.LocalHostStatus = nil
			if _, _, err := s.StartCommander(context.Background(), "restored", false); err != nil {
				t.Fatal(err)
			}
			wantRoute := ""
			if projectDefault {
				wantRoute = "fast"
			}
			if ex.req.Profile != wantRoute {
				t.Fatalf("resume kept replacement host: %+v", ex.req)
			}
		})
	}
}

func TestAuxiliaryCallsResolvePausedDefaultAtDispatch(t *testing.T) {
	for _, purpose := range []string{"verifier", "report"} {
		t.Run(purpose, func(t *testing.T) {
			profiles := testProfiles()
			s, _ := profileServer(t, profiles...)
			ex := &recordExec{}
			wrapped := s.DefaultModelExecutor(ex)
			profiles[0].Paused = true // paused after the executor was created
			if _, err := wrapped.Run(context.Background(), executor.Request{Purpose: purpose, Node: "local"}, func(executor.Event) {}); err != nil {
				t.Fatal(err)
			}
			if ex.Request().Profile != "slow" {
				t.Fatalf("auxiliary request=%+v", ex.Request())
			}
			profiles[0].Paused = false
			if _, err := wrapped.Run(context.Background(), executor.Request{Purpose: purpose, Node: "local"}, func(executor.Event) {}); err != nil {
				t.Fatal(err)
			}
			if ex.Request().Profile != "" {
				t.Fatal("auxiliary call did not return to the default host")
			}
			profiles[0].Paused, profiles[1].Paused = true, true
			unavailable := &recordExec{}
			_, err := s.DefaultModelExecutor(unavailable).Run(context.Background(), executor.Request{Purpose: purpose, Node: "local"}, func(executor.Event) {})
			if err == nil || !strings.Contains(err.Error(), "没有其他可用模型") || unavailable.run {
				t.Fatalf("no alternative still dispatched: err=%v ran=%t", err, unavailable.run)
			}
		})
	}
}

func TestDefaultModelExecutorPreservesNodeRouting(t *testing.T) {
	s, _ := profileServer(t, testProfiles()...)
	wrapped := s.DefaultModelExecutor(nodeExec{})
	if !executor.HasNode(wrapped, "lab") || executor.HasNode(wrapped, "missing") || executor.ResolveNode(wrapped, "lab") != "local" {
		t.Fatal("default-model routing lost the underlying node registry")
	}
}

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
