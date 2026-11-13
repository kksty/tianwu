package store

import (
	"testing"

	"github.com/kksty/tianwu/internal/model"
)

func TestExplicitCancellationIsHeldInTheSettlementTransaction(t *testing.T) {
	for _, tc := range []struct {
		cause string
		hold  bool
	}{
		{model.CancelCauseOperator, true}, {model.CancelCauseStopLoss, true},
		{model.CancelCauseWorkerKill, true}, {model.CancelCauseModelStall, true},
		{model.CancelCauseRetry, false}, {model.CancelCauseSteer, false},
		{model.CancelCauseHandoff, false}, {model.CancelCauseRestart, false},
		{model.CancelCauseWatchdog, false}, {model.CancelCausePause, false},
	} {
		t.Run(tc.cause, func(t *testing.T) {
			s := open(t)
			defer s.Close()
			in, err := s.CreateIntent(model.Intent{Title: "explicit stop"})
			if err != nil {
				t.Fatal(err)
			}
			_, run, err := s.StartIntentRun(in.ID, "local", "", model.Run{})
			if err != nil {
				t.Fatal(err)
			}
			out, err := s.SettleIntentForRun(in.ID, run.ID, model.RunCanceled, tc.cause)
			if err != nil {
				t.Fatal(err)
			}
			got, _ := s.GetIntent(in.ID)
			status := model.StatusPending
			if tc.hold {
				status = model.StatusFailed
			}
			if out.Status != status || got.Status != status || got.Hold != tc.hold || got.Attempts != 0 {
				t.Fatalf("cancellation settled incorrectly: status=%s hold=%v attempts=%d outcome=%s", got.Status, got.Hold, got.Attempts, out.Status)
			}
		})
	}
}
