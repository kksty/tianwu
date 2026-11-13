package api

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// Pausing the project must close the dispatch gate itself, not just stop the
// decider: a round already past the pause check could otherwise start a new run
// right after CancelAll.
func TestProjectPauseClosesTheDispatchGate(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, &blockingExec{}, dir, filepath.Join(dir, "cases"))
	in, _ := st.CreateIntent(model.Intent{Title: "暂停期间不能派"})
	t.Cleanup(func() {
		s.projectResume(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/project/resume", nil))
		s.CancelAll()
		s.WaitIdle(5 * time.Second)
		time.Sleep(150 * time.Millisecond)
	})

	s.projectPause(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/project/pause", nil))
	if _, err := s.StartRun(in.ID); err == nil || !strings.Contains(err.Error(), "暂停") {
		t.Fatalf("start while paused = %v, want a pause refusal", err)
	}

	s.projectResume(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/project/resume", nil))
	runID, err := s.StartRun(in.ID)
	if err != nil {
		t.Fatalf("resume did not reopen dispatch: %v", err)
	}
	// Let the dispatcher register its cancel func before cleanup: otherwise
	// CancelAll sees an empty map and the blocked run outlives the temp dir.
	waitFor(t, 5*time.Second, func() bool {
		return s.Lifecycle.RunActive(runID)
	})
}

// A purpose run writes inside the project but has no cancel entry; a delete
// that waited only for cancels would RemoveAll underneath it.
func TestWaitIdleSeesPurposeRuns(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, &blockingExec{}, dir, filepath.Join(dir, "cases"))
	run, err := st.AddRun(model.Run{Node: "local", Purpose: "report", Status: model.RunRunning})
	if err != nil {
		t.Fatal(err)
	}
	if s.WaitIdle(50 * time.Millisecond) {
		t.Fatal("a live report run must keep the project busy")
	}
	if _, err := st.UpdateRun(run.ID, func(r *model.Run) { r.Status = model.RunDone }); err != nil {
		t.Fatal(err)
	}
	if !s.WaitIdle(500 * time.Millisecond) {
		t.Fatal("WaitIdle did not notice the report finished")
	}
}
