package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/decider"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/project"
	"github.com/kksty/tianwu/internal/store"
)

func TestProjectPausePersistsAndRejectsCommanderWakeAndPartialResume(t *testing.T) {
	s, p := newBindingServer(t)
	s.Decider = decider.NewRunner(s.Store, nil, "")
	s.Decider.ModelName = "fixture"
	s.Decider.Commander = func(context.Context, string, bool) (decider.RoundResult, error) {
		t.Error("paused project called commander")
		return decider.RoundResult{}, nil
	}
	if rec := call(t, s, http.MethodPost, "/api/project/pause", nil); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	m, err := project.Open(filepath.Dir(p.Dir))
	if err != nil {
		t.Fatal(err)
	}
	if !m.Get(p.ID).IsPaused() {
		t.Fatal("pause was not persisted")
	}
	for _, route := range []string{"/api/decide", "/api/decider/resume"} {
		if rec := call(t, s, http.MethodPost, route, nil); rec.Code != http.StatusConflict {
			t.Fatalf("%s: %d %s", route, rec.Code, rec.Body.String())
		}
	}
	if !s.paused.Load() || !s.Decider.Paused() {
		t.Fatal("partial resume bypassed project pause")
	}
	if rec := call(t, s, http.MethodPost, "/api/project/resume", nil); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	m, err = project.Open(filepath.Dir(p.Dir))
	if err != nil {
		t.Fatal(err)
	}
	if m.Get(p.ID).IsPaused() || s.paused.Load() || s.Decider.Paused() {
		t.Fatal("explicit resume did not clear persisted and runtime pause")
	}
}

// A finished engagement pauses itself: the switch must reach the manifest so a
// restart does not relaunch it, the reason must say why, and the operator's
// explicit resume must still work afterwards.
func TestAutoPausePausesFinishedProjectAndOperatorCanResume(t *testing.T) {
	s, p := newBindingServer(t)
	s.Decider = decider.NewRunner(s.Store, nil, "")
	s.Decider.ModelName = "fixture"
	s.Decider.Commander = func(context.Context, string, bool) (decider.RoundResult, error) {
		t.Error("auto-paused project called commander")
		return decider.RoundResult{}, nil
	}
	s.AutoPause("目标已完成：测试自动暂停")
	if !s.paused.Load() || !s.Decider.Paused() {
		t.Fatal("auto-pause did not gate the project and the commander")
	}
	if reason := s.Decider.Status().PausedReason; !strings.Contains(reason, "目标已完成") {
		t.Fatalf("pause reason = %q, want it to name completion", reason)
	}
	m, err := project.Open(filepath.Dir(p.Dir))
	if err != nil {
		t.Fatal(err)
	}
	if !m.Get(p.ID).IsPaused() {
		t.Fatal("auto-pause was not persisted")
	}
	// A repeated settle signal is a no-op, not a second pause/cancel.
	s.AutoPause("again")
	if rec := call(t, s, http.MethodPost, "/api/project/resume", nil); rec.Code != 200 {
		t.Fatalf("resume after auto-pause: %d %s", rec.Code, rec.Body.String())
	}
	if s.paused.Load() || s.Decider.Paused() {
		t.Fatal("operator resume did not clear the automatic pause")
	}
}

func TestLatePurposeRegistrationIsCanceledAfterProjectPause(t *testing.T) {
	s, _ := newBindingServer(t)
	s.SetPaused(true)
	s.CancelAllWithCause(model.CancelCausePause, "pause")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	settle := s.TrackPurposeRun("late-commander", cancel)
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("late purpose registration escaped pause")
	}
	if cause := settle(); cause != model.CancelCausePause {
		t.Fatalf("cancel cause=%q", cause)
	}
}

// The whole-project switch is one endpoint on purpose: an operator stopping the
// engagement must not have to remember "pause the commander here, cancel the
// runs there". It also has to be reversible — the canceled directions are only
// pending, not held, so Resume can re-dispatch them.
func TestProjectPauseStopsDeciderAndResumes(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, &handoffExec{}, dir, filepath.Join(dir, "cases"))
	s.Decider = decider.NewRunner(st, nil, filepath.Join(dir, "decisions.jsonl"))

	rr := httptest.NewRecorder()
	s.projectPause(rr, httptest.NewRequest(http.MethodPost, "/api/project/pause", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("pause = %d: %s", rr.Code, rr.Body.String())
	}
	if !s.Decider.Paused() {
		t.Fatal("projectPause did not pause the commander")
	}
	var paused struct {
		Paused   bool `json:"paused"`
		Canceled int  `json:"canceled"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &paused); err != nil {
		t.Fatal(err)
	}
	if !paused.Paused || paused.Canceled != 0 {
		t.Fatalf("pause response = %+v, want paused with 0 live runs", paused)
	}

	rr = httptest.NewRecorder()
	s.projectResume(rr, httptest.NewRequest(http.MethodPost, "/api/project/resume", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("resume = %d: %s", rr.Code, rr.Body.String())
	}
	if s.Decider.Paused() {
		t.Fatal("projectResume left the commander paused")
	}
}
