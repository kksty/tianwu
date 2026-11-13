package api

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// The watchdog exists for dispatch(): it puts back an intent whose goroutine
// vanished. Verifier and report runs are started inside their own generators,
// not by dispatch, so they never appear in the intent-run cancel registry and
// were reaped as lost work on the same two-minute clock. Reaping them marked a
// perfectly healthy review/report run "canceled" while its process was still
// writing — the acceptance run found this.
func TestWatchdogLeavesVerifierAndReportRunsAlone(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, nil, dir, filepath.Join(dir, "cases"))

	report, err := st.AddRun(model.Run{Node: "local", Purpose: "report"})
	if err != nil {
		t.Fatal(err)
	}
	verify, err := st.AddRun(model.Run{IntentID: "in_x", Node: "local", Purpose: "verifier"})
	if err != nil {
		t.Fatal(err)
	}
	// A dispatched intent run is the watchdog's business, and it is stale.
	stale, err := st.AddRun(model.Run{IntentID: "in_y", Node: "local"})
	if err != nil {
		t.Fatal(err)
	}

	old := time.Now().Add(-5 * time.Minute)
	for _, id := range []string{report.ID, verify.ID, stale.ID} {
		if _, err := st.UpdateRun(id, func(r *model.Run) { r.StartedAt = old }); err != nil {
			t.Fatal(err)
		}
	}

	s.reapOrphans()

	for _, id := range []string{report.ID, verify.ID} {
		r, ok := st.GetRun(id)
		if !ok {
			t.Fatalf("run %s disappeared", id)
		}
		if r.Status != model.RunRunning {
			t.Fatalf("run %s (%s) was reaped: status=%s reason=%s", id, r.Purpose, r.Status, r.ExitReason)
		}
	}
	r, _ := st.GetRun(stale.ID)
	if r.Status != model.RunCanceled {
		t.Fatalf("dispatched run should have been reaped, got status=%s", r.Status)
	}
	if r.ExitReason != "调度丢失，守卫回收" {
		t.Fatalf("unexpected exit reason %q", r.ExitReason)
	}
	if r.CancelCause != model.CancelCauseWatchdog {
		t.Fatalf("cancel cause = %q, want %q", r.CancelCause, model.CancelCauseWatchdog)
	}
}
