package api

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// Recover settles the ledger: a run recorded as running belongs to a process
// that is gone, so it becomes canceled with the restart cause and its root
// intent goes back to pending without spending an attempt.
//
// It does not touch the case's AOP session pointer. That is the host layer's
// job (host.SweepStaleSessions at startup / endpoint change): Recover has no
// data-directory-wide view and rescanning every project here would repeat the
// same work per attach (watchdog-liveness §4.2).
func TestRecoverSettlesRunningRunsAndLeavesSessionMarkers(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, nil, dir, filepath.Join(dir, "cases"))

	in, err := st.CreateIntent(model.Intent{Title: "重启前在跑的方向"})
	if err != nil {
		t.Fatal(err)
	}
	// Build the same linked current-run identity as actual dispatch. An old
	// unrelated run must not reset a replacement intent during recovery.
	_, run, err := st.StartIntentRun(in.ID, "local", "", model.Run{})
	if err != nil {
		t.Fatal(err)
	}

	caseDir := filepath.Join(dir, "cases", in.ID, ".tw")
	if err := os.MkdirAll(caseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(caseDir, "aop-session.json")
	if err := os.WriteFile(markerPath, []byte(`{"url":"http://127.0.0.1:18767","agent":"local","session":"s1"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	s.Recover()

	got, ok := st.GetRun(run.ID)
	if !ok {
		t.Fatal("run disappeared")
	}
	if got.Status != model.RunCanceled || got.CancelCause != model.CancelCauseRestart {
		t.Fatalf("run = %s/%s, want canceled/%s", got.Status, got.CancelCause, model.CancelCauseRestart)
	}
	settled, ok := st.GetIntent(in.ID)
	if !ok {
		t.Fatal("intent disappeared")
	}
	if settled.Status != model.StatusPending {
		t.Fatalf("intent status = %s, want pending", settled.Status)
	}
	if settled.ClaimedBy != "" {
		t.Fatalf("intent claim survived the restart: %q", settled.ClaimedBy)
	}
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("Recover must leave session markers to the host sweep: %v", err)
	}
}
