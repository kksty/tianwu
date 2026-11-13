package api

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// finish writes the run and the intent in two steps. A crash (or a failed
// second write) between them leaves run=done + intent=running, a shape none of
// the other reapers look at: run says "already running", cancel says "not
// running", park/retry/reopen all refuse. The guard repairs it.
func TestDanglingRunningIntentIsReconciled(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, nil, dir, filepath.Join(dir, "cases"))

	done, _ := st.CreateIntent(model.Intent{Title: "跑完了但没收尾"})
	run, _ := st.AddRun(model.Run{IntentID: done.ID, Node: "local"})
	ended := time.Now().Add(-2 * time.Minute)
	if _, err := st.UpdateRun(run.ID, func(r *model.Run) {
		r.Status = model.RunDone
		r.EndedAt = &ended
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIntent(done.ID, func(i *model.Intent) {
		i.Status = model.StatusRunning
		i.RunID = run.ID
		i.Attempts = 1
	}); err != nil {
		t.Fatal(err)
	}

	s.reapDanglingIntents()

	got, _ := st.GetIntent(done.ID)
	if got.Status != model.StatusDone || got.Attempts != 1 {
		t.Fatalf("reconciled intent = %+v", got)
	}
}

// A canceled run's intent goes back to pending without spending an attempt, the
// same transition finish would have written.
func TestDanglingCanceledRunReleasesTheAttempt(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, nil, dir, filepath.Join(dir, "cases"))

	in, _ := st.CreateIntent(model.Intent{Title: "取消没收尾"})
	run, _ := st.AddRun(model.Run{IntentID: in.ID, Node: "local"})
	ended := time.Now().Add(-2 * time.Minute)
	if _, err := st.UpdateRun(run.ID, func(r *model.Run) {
		r.Status = model.RunCanceled
		r.EndedAt = &ended
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Status = model.StatusRunning
		i.RunID = run.ID
		i.Attempts = 1
	}); err != nil {
		t.Fatal(err)
	}

	s.reapDanglingIntents()

	got, _ := st.GetIntent(in.ID)
	if got.Status != model.StatusPending || got.Attempts != 0 {
		t.Fatalf("reconciled intent = %+v", got)
	}
}

// The grace window keeps the reaper away from a run that finish is closing right
// now (UpdateRun happened, UpdateIntent has not).
func TestRecentTerminalRunIsLeftToFinish(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, nil, dir, filepath.Join(dir, "cases"))

	in, _ := st.CreateIntent(model.Intent{Title: "刚结束"})
	run, _ := st.AddRun(model.Run{IntentID: in.ID, Node: "local"})
	ended := time.Now()
	if _, err := st.UpdateRun(run.ID, func(r *model.Run) {
		r.Status = model.RunDone
		r.EndedAt = &ended
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Status = model.StatusRunning
		i.RunID = run.ID
	}); err != nil {
		t.Fatal(err)
	}

	s.reapDanglingIntents()

	got, _ := st.GetIntent(in.ID)
	if got.Status != model.StatusRunning {
		t.Fatalf("a just-closed run was reconciled too early: %+v", got)
	}
}
