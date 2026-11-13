package api

import (
	"github.com/kksty/tianwu/internal/model"
	"sync/atomic"
	"testing"
	"time"
)

func TestShutdownSealsChildDispatchAndLatePurposeAdmission(t *testing.T) {
	s, ex, in, _ := finishedServer(t, model.StatusDone, model.RunDone, true)
	parent, err := s.Store.CreateIntent(model.Intent{Title: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.UpdateIntent(in.ID, func(i *model.Intent) { i.ParentID = parent.ID }); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.continueIntentRun(in.ID, ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, time.Second, func() bool { return len(ex.requests()) == 1 })
	if n := s.BeginShutdown(); n != 1 {
		t.Fatalf("stopped runs=%d", n)
	}
	if !s.WaitIdle(2 * time.Second) {
		t.Fatal("child redispatched during shutdown")
	}
	s.pump()
	if len(ex.requests()) != 1 {
		t.Fatal("shutdown allowed a fresh child")
	}
	if _, err := s.StartRun(in.ID); err == nil {
		t.Fatal("late explicit start was admitted")
	}
	var canceled atomic.Bool
	cleanup := s.TrackPurposeRun("late", func() { canceled.Store(true) })
	if !canceled.Load() || cleanup() != model.CancelCauseRestart {
		t.Fatal("late model purpose escaped shutdown")
	}
	if s.paused.Load() {
		t.Fatal("process shutdown changed project pause state")
	}
}
