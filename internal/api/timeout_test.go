package api

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// A timeout is a watchdog decision. The cause is part of the run record the
// fleet and console read, so it must be set structurally at the timeout, not
// guessed later from the reason text.
func TestTimeoutIsRecordedAsAWatchdogCause(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	ex := &blockingExec{}
	s := New(st, ex, dir, filepath.Join(dir, "cases"))
	s.Timeout = 40 * time.Millisecond
	t.Cleanup(func() {
		s.CancelAll()
		s.WaitIdle(5 * time.Second)
		time.Sleep(150 * time.Millisecond)
	})

	in, err := st.CreateIntent(model.Intent{Title: "会超时的方向", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	runID, err := s.StartRun(in.ID)
	if err != nil {
		t.Fatal(err)
	}

	waitFor(t, 5*time.Second, func() bool {
		r, ok := st.GetRun(runID)
		return ok && r.Status == model.RunCanceled
	})
	r, ok := st.GetRun(runID)
	if !ok {
		t.Fatal("run disappeared")
	}
	if r.CancelCause != model.CancelCauseWatchdog {
		t.Fatalf("cancel cause = %q, want %q", r.CancelCause, model.CancelCauseWatchdog)
	}
	if !strings.Contains(r.ExitReason, "超时") {
		t.Fatalf("exit reason = %q", r.ExitReason)
	}
	// A timeout is our policy, not the executor's failure: the direction goes
	// back to pending and is held, so it cannot re-dispatch itself forever.
	got, ok := st.GetIntent(in.ID)
	if !ok || got.Status != model.StatusPending || !got.Hold {
		t.Fatalf("intent after timeout = %+v", got)
	}
}
