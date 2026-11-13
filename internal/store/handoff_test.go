package store

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/kksty/tianwu/internal/model"
)

func TestSaveHandoffVersionCASAndProfile(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	in, err := s.CreateIntent(model.Intent{Title: "父", Profile: "old"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateIntent(in.ID, func(i *model.Intent) { i.Status = model.StatusPending }); err != nil {
		t.Fatal(err)
	}
	held, err := s.BeginHandoff(in.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.SaveHandoff(in.ID, 0, model.Handoff{ToProfile: "new", Note: "交接"}, held.HandoffOperation)
	if err != nil {
		t.Fatal(err)
	}
	if h.HandoffVersion != 1 || h.Handoff == nil || h.Handoff.Version != 1 || h.Handoff.ID == "" {
		t.Fatalf("saved = %+v", h)
	}
	if h.Profile != "new" || h.Status != model.StatusPending {
		t.Fatalf("intent after handoff = %+v", h)
	}
	// An old turn cannot overwrite the new handoff.
	if _, err := s.SaveHandoff(in.ID, 0, model.Handoff{ToProfile: "third"}); !errors.Is(err, ErrHandoffConflict) {
		t.Fatalf("stale handoff err = %v", err)
	}
	if err := s.ReleaseHandoffHold(in.ID, "下一次交接", held.HandoffOperation); err != nil {
		t.Fatal(err)
	}
	held, err = s.BeginHandoff(in.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	next, err := s.SaveHandoff(in.ID, 1, model.Handoff{ToProfile: "third"}, held.HandoffOperation)
	if err != nil || next.HandoffVersion != 2 {
		t.Fatalf("second handoff = %+v %v", next, err)
	}
}

// BeginHandoff is the abort point for a stale/duplicate request: it must fail
// without changing a single field, because the caller cancels the live run only
// after this succeeds.
func TestBeginHandoffCASLeavesStateUntouchedOnConflict(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	in, err := s.CreateIntent(model.Intent{Title: "父"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Status = model.StatusRunning
		i.RunID = "r_live"
		i.HandoffVersion = 1
		i.Hold = false
	}); err != nil {
		t.Fatal(err)
	}
	before, _ := s.GetIntent(in.ID)

	if _, err := s.BeginHandoff(in.ID, 0); !errors.Is(err, ErrHandoffConflict) {
		t.Fatalf("stale begin = %v, want ErrHandoffConflict", err)
	}
	after, _ := s.GetIntent(in.ID)
	if after.HandoffVersion != before.HandoffVersion || after.Hold || after.Status != model.StatusRunning ||
		after.RunID != before.RunID {
		t.Fatalf("stale begin changed the intent: before=%+v after=%+v", before, after)
	}

	// The matching version wins and takes the hold without touching the run.
	cur, err := s.BeginHandoff(in.ID, 1)
	if err != nil || !cur.Hold || cur.RunID != "r_live" {
		t.Fatalf("begin = %+v %v", cur, err)
	}
}

// Hold survives SaveHandoff and is released by the successful claim of the
// replacement run (or explicitly when the handoff aborts).
func TestHandoffHoldSpansSnapshotUntilClaim(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	in, _ := s.CreateIntent(model.Intent{Title: "父"})
	if _, err := s.UpdateIntent(in.ID, func(i *model.Intent) { i.Status = model.StatusPending }); err != nil {
		t.Fatal(err)
	}
	held, err := s.BeginHandoff(in.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := s.SaveHandoff(in.ID, 0, model.Handoff{ToProfile: "new"}, held.HandoffOperation)
	if err != nil {
		t.Fatal(err)
	}
	if !saved.Hold {
		t.Fatal("SaveHandoff dropped the handoff hold")
	}
	if _, ok, err := s.ClaimIntent(in.ID, "local"); !errors.Is(err, ErrHandoffConflict) || ok {
		t.Fatalf("unowned claim = %v %v", ok, err)
	}
	if _, ok, err := s.ClaimIntent(in.ID, "local", held.HandoffOperation); err != nil || !ok {
		t.Fatalf("claim = ok:%v err:%v", ok, err)
	}
	claimed, _ := s.GetIntent(in.ID)
	if claimed.Hold {
		t.Fatal("claim did not clear the handoff hold")
	}
}

// Aborting before the snapshot releases the hold so the direction can be
// scheduled like any other pending unit.
func TestReleaseHandoffHold(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	in, _ := s.CreateIntent(model.Intent{Title: "父"})
	held, err := s.BeginHandoff(in.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseHandoffHold(in.ID, "测试中止", held.HandoffOperation); err != nil {
		t.Fatal(err)
	}
	cur, _ := s.GetIntent(in.ID)
	if cur.Hold {
		t.Fatal("hold survived ReleaseHandoffHold")
	}
}
