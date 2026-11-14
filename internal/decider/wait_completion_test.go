package decider

import (
	"context"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/model"
)

// F7 regression in combination: every shape of explicit wait keeps the project
// open even when all goals are stamped. The heartbeat must not spend rounds on
// an unchanged waiting board, and pause/resume must not turn the wait into
// "finished". Only explicitly resolving the wait may let completion fire.
func TestWaitingDirectionsBlockCompletionWithoutHeartbeatSpin(t *testing.T) {
	waits := map[string]*model.Wait{
		"operator_receipt": {Operator: &model.OperatorWait{Request: "扫码登录"}},
		"external_action":  {Actions: []string{"ac_1"}},
		"subtask":          {Children: []string{"in_child"}},
		"bare":             {},
	}
	for name, wait := range waits {
		t.Run(name, func(t *testing.T) {
			st := newStore(t)
			g, err := st.AddGoal(model.Goal{Statement: "goal"})
			if err != nil {
				t.Fatal(err)
			}
			e, err := st.AddEvidence(model.Evidence{URI: "fixture.txt"})
			if err != nil {
				t.Fatal(err)
			}
			completeGoal(t, st, g.ID, e.ID)
			in, err := st.CreateIntent(model.Intent{Title: "waiting direction"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.UpdateIntent(in.ID, func(i *model.Intent) {
				i.Status, i.Wait = model.StatusWaiting, wait
			}); err != nil {
				t.Fatal(err)
			}
			r := NewRunner(st, nil, "")
			r.ModelName = "fixture"
			r.Heartbeat = time.Hour
			r.Debounce = time.Millisecond
			calls, settled := 0, 0
			r.Commander = func(context.Context, string, bool) (RoundResult, error) {
				calls++
				// An unacknowledged delivery is redelivered by design; the real
				// commander acks it. The test is about the waiting board, not
				// about the delivery queue.
				if d, ok := st.PendingDelivery("commander"); ok {
					if err := st.AcknowledgeDelivery("commander", d.ID); err != nil {
						t.Errorf("ack delivery: %v", err)
					}
				}
				return RoundResult{}, nil
			}
			r.Settled = func() { settled++ }

			for n := 0; n < 30; n++ {
				r.Run(context.Background(), "heartbeat", false)
			}
			if settled != 0 {
				t.Fatalf("a waiting direction was reported finished (%d signals)", settled)
			}
			// One round may be spent noticing the changed board; repeated
			// heartbeats on an unchanged waiting board must not spin the model.
			if calls > 1 {
				t.Fatalf("heartbeat spent %d commander rounds on an unchanged waiting board", calls)
			}
			if r.workFinished() {
				t.Fatal("workFinished ignored the explicit wait")
			}

			// Pausing the project and resuming must not reclassify the wait.
			r.Pause()
			r.settleIfDone()
			if settled != 0 {
				t.Fatal("paused runner signalled completion while the direction still waits")
			}
			r.Resume()
			r.settleIfDone()
			if settled != 0 || r.workFinished() {
				t.Fatal("resume treated the open wait as finished")
			}

			// Releasing the wait is the explicit resolution that may finish work.
			if _, err := st.UpdateIntent(in.ID, func(i *model.Intent) {
				i.Status, i.Wait = model.StatusDone, nil
			}); err != nil {
				t.Fatal(err)
			}
			r.settleIfDone()
			if settled != 1 {
				t.Fatalf("resolved work signalled completion %d times, want 1", settled)
			}
			r.Pause()
			r.settleIfDone()
			if settled != 1 {
				t.Fatal("a paused runner repeated the settle signal")
			}
		})
	}
}

// A waiting direction that is resumed into a running turn is still open: the
// completion decision reads one snapshot and cannot see "waiting" as done.
func TestWaitResumedIntoRunningKeepsProjectOpen(t *testing.T) {
	st := newStore(t)
	g, _ := st.AddGoal(model.Goal{Statement: "goal"})
	e, _ := st.AddEvidence(model.Evidence{URI: "fixture.txt"})
	completeGoal(t, st, g.ID, e.ID)
	in, _ := st.CreateIntent(model.Intent{Title: "direction"})
	if _, err := st.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Status = model.StatusWaiting
		i.Wait = &model.Wait{Operator: &model.OperatorWait{Request: "login"}}
	}); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(st, nil, "")
	r.Settled = func() { t.Error("waiting work was reported finished") }
	r.settleIfDone()
	if _, err := st.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Status, i.Wait = model.StatusRunning, nil
	}); err != nil {
		t.Fatal(err)
	}
	r.settleIfDone()
	if r.workFinished() {
		t.Fatal("running direction treated as finished")
	}
}
