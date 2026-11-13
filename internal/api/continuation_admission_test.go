package api

import (
	"github.com/kksty/tianwu/internal/model"
	"sync"
	"testing"
	"time"
)

func TestExplicitContinuationPreparationCannotBeStolenByChildPump(t *testing.T) {
	for _, mode := range []string{"continue", "retry_resume", "retry_fresh"} {
		t.Run(mode, func(t *testing.T) {
			status, runStatus := model.StatusDone, model.RunDone
			if mode != "continue" {
				status, runStatus = model.StatusFailed, model.RunFailed
			}
			s, ex, in, _ := finishedServer(t, status, runStatus, true)
			parent, err := s.Store.CreateIntent(model.Intent{Title: "parent"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Store.UpdateIntent(in.ID, func(i *model.Intent) { i.ParentID = parent.ID }); err != nil {
				t.Fatal(err)
			}
			var gate sync.Mutex
			gate.Lock()
			var release sync.Once
			unlock := func() { release.Do(gate.Unlock) }
			defer unlock()
			s.InstanceDispatch = &gate
			done := make(chan error, 1)
			go func() {
				if mode == "continue" {
					_, _, err := s.continueIntentRun(in.ID, "")
					done <- err
				} else {
					_, _, err := s.retryIntentRun(in.ID, "", mode == "retry_resume")
					done <- err
				}
			}()
			waitFor(t, time.Second, func() bool { i, _ := s.Store.GetIntent(in.ID); return i.Status == model.StatusPending })
			prepared, _ := s.Store.GetIntent(in.ID)
			if !prepared.Hold {
				t.Fatal("explicit preparation exposed a dispatchable child")
			}
			pumped := make(chan struct{})
			go func() { s.pump(); close(pumped) }()
			select {
			case <-pumped:
			case <-time.After(time.Second):
				t.Fatal("child pump tried to steal explicit continuation")
			}
			unlock()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			waitFor(t, time.Second, func() bool { return len(ex.requests()) == 1 })
			if ex.requests()[0].Resume != (mode != "retry_fresh") {
				t.Fatal("explicit continuation changed session policy")
			}
		})
	}
}

func TestConcurrentRetriesCannotResetAnAlreadyClaimedDirection(t *testing.T) {
	s, ex, in, _ := finishedServer(t, model.StatusFailed, model.RunFailed, true)
	errs := make(chan error, 8)
	for range 8 {
		go func() { _, _, err := s.retryIntentRun(in.ID, "parallel", true); errs <- err }()
	}
	wins := 0
	for range 8 {
		if <-errs == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("successful retries=%d", wins)
	}
	waitFor(t, time.Second, func() bool { return len(ex.requests()) == 1 })
	current, _ := s.Store.GetIntent(in.ID)
	if current.Hold || (current.Status != model.StatusRunning && current.Status != model.StatusClaimed) {
		t.Fatal("losing retry corrupted live claim")
	}
}
