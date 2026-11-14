package store

import (
	"errors"
	"testing"

	"github.com/kksty/tianwu/internal/model"
)

func TestTaskContextIsAVersionedSelection(t *testing.T) {
	s := open(t)
	ref, _ := s.AddFact(model.Fact{Title: "参考"})
	pin, _ := s.AddFact(model.Fact{Title: "固定"})
	in, _ := s.CreateIntent(model.Intent{Title: "task", FromFactIDs: []string{ref.ID}})

	// The first edit names version 0, the version the intent was created with.
	updated, err := s.UpdateTaskContext(in.ID, "", TaskContext{
		Version: 0, References: []string{" " + ref.ID + " ", ref.ID}, Pins: []string{pin.ID}, Entities: []string{"host:10.0.0.5"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ContextVersion != 1 {
		t.Fatalf("context version = %d, want 1", updated.ContextVersion)
	}
	if len(updated.ContextFactIDs) != 1 || updated.ContextFactIDs[0] != ref.ID {
		t.Fatalf("references not normalised: %v", updated.ContextFactIDs)
	}
	if len(updated.PinnedFactIDs) != 1 || updated.PinnedFactIDs[0] != pin.ID || updated.InterestEntityRefs[0] != "host:10.0.0.5" {
		t.Fatalf("pins/entities lost: %+v", updated)
	}
	// References are not prerequisites: the hard premise list is untouched.
	if len(updated.FromFactIDs) != 1 || updated.FromFactIDs[0] != ref.ID {
		t.Fatalf("hard premises rewritten: %v", updated.FromFactIDs)
	}

	// A stale editor is refused, and a refused edit changes nothing.
	if _, err := s.UpdateTaskContext(in.ID, "", TaskContext{Version: 0, References: []string{pin.ID}}); !errors.Is(err, ErrContextConflict) {
		t.Fatalf("stale update = %v, want ErrContextConflict", err)
	}
	if got, _ := s.GetIntent(in.ID); got.ContextVersion != 1 || got.ContextFactIDs[0] != ref.ID {
		t.Fatalf("stale update changed the selection: %+v", got)
	}
	if _, err := s.UpdateTaskContext(in.ID, "", TaskContext{Version: 1, References: []string{"f_missing"}}); err == nil {
		t.Fatal("unknown reference accepted")
	}
}

// A participant may edit only the direction its run currently holds: a stale
// run, a finished intent or a foreign intent is a conflict, not an update.
func TestTaskContextUpdateRequiresTheCurrentRun(t *testing.T) {
	s := open(t)
	ref, _ := s.AddFact(model.Fact{Title: "参考"})
	in, _ := s.CreateIntent(model.Intent{Title: "task"})
	_, run, err := s.StartIntentRun(in.ID, "local", "", model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateTaskContext(in.ID, "r_stale", TaskContext{Version: 0, References: []string{ref.ID}}); !errors.Is(err, ErrContextConflict) {
		t.Fatalf("stale run = %v, want ErrContextConflict", err)
	}
	if _, err := s.UpdateTaskContext(in.ID, run.ID, TaskContext{Version: 0, References: []string{ref.ID}}); err != nil {
		t.Fatalf("current run could not update: %v", err)
	}
	// Once the run ends, the participant identity is no longer the holder.
	if _, err := s.UpdateRun(run.ID, func(r *model.Run) { r.Status = model.RunDone }); err != nil {
		t.Fatal(err)
	}
	_, _ = s.UpdateIntent(in.ID, func(i *model.Intent) { i.Status = model.StatusDone; i.RunID = "" })
	if _, err := s.UpdateTaskContext(in.ID, run.ID, TaskContext{Version: 1}); !errors.Is(err, ErrContextConflict) {
		t.Fatalf("finished owner = %v, want ErrContextConflict", err)
	}
}

// Dispatch freezes what the run actually saw: premise claim versions, explicit
// context record versions and the hard premises.
func TestDispatchFreezesClaimAndContextVersions(t *testing.T) {
	s := open(t)
	e, err := s.AddEvidence(model.Evidence{URI: "in_1/proof.txt"})
	if err != nil {
		t.Fatal(err)
	}
	premise := verifiedSupport(t, s, e.ID, "前提")
	ref, _ := s.AddFact(model.Fact{Title: "参考"})
	pin, _ := s.AddFact(model.Fact{Title: "固定"})
	in, _ := s.CreateIntent(model.Intent{Title: "task", FromFactIDs: []string{premise.ID}})
	if _, err := s.UpdateTaskContext(in.ID, "", TaskContext{Version: 0, References: []string{ref.ID}, Pins: []string{pin.ID}}); err != nil {
		t.Fatal(err)
	}
	// The intent row passed to dispatch carries the selection it observed.
	in, _ = s.GetIntent(in.ID)
	_, run, err := s.StartIntentRun(in.ID, "local", "", model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PrepareRunInput(run.ID, in, model.PremiseVerifiedOnly); err != nil {
		t.Fatal(err)
	}
	saved, _ := s.GetRun(run.ID)
	if saved.PremiseClaimVersions[premise.ID] != premise.ClaimVersion || saved.PremiseVersions[premise.ID] == 0 {
		t.Fatalf("premise versions = %+v / %+v", saved.PremiseVersions, saved.PremiseClaimVersions)
	}
	if saved.ContextFactVersions[ref.ID] == 0 || saved.ContextFactVersions[pin.ID] == 0 {
		t.Fatalf("context versions not frozen: %+v", saved.ContextFactVersions)
	}
	if len(saved.ContextFactVersions) != 2 {
		t.Fatalf("frozen context set = %+v, want exactly the selection", saved.ContextFactVersions)
	}
	// Changing the reference afterwards does not rewrite the frozen run row.
	_, _ = s.UpdateFact(ref.ID, func(f *model.Fact) { f.Summary = "changed" })
	again, _ := s.GetRun(run.ID)
	if again.ContextFactVersions[ref.ID] != saved.ContextFactVersions[ref.ID] {
		t.Fatal("frozen context version was rewritten")
	}
}

// Creation-time context is explicit writer input: an unknown fact id is a
// request error, and spawn idempotency compares the context selection instead
// of treating a changed selection as the same worker.
func TestCreationValidatesAndDeduplicatesInitialContext(t *testing.T) {
	s := open(t)
	ref, _ := s.AddFact(model.Fact{Title: "参考"})
	pin, _ := s.AddFact(model.Fact{Title: "固定"})
	if _, err := s.CreateIntent(model.Intent{Title: "bad", ContextFactIDs: []string{"f_missing"}}); !errors.Is(err, ErrFactRefMissing) {
		t.Fatalf("unknown create reference = %v", err)
	}
	in, err := s.CreateIntent(model.Intent{Title: "ok", ContextFactIDs: []string{ref.ID, ref.ID}, PinnedFactIDs: []string{pin.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if len(in.ContextFactIDs) != 1 || in.ContextFactIDs[0] != ref.ID || len(in.PinnedFactIDs) != 1 {
		t.Fatalf("initial context = %+v", in)
	}

	spawn := model.Intent{Title: "w", CreatedBy: "pt_x", IdempotencyKey: "same", ContextFactIDs: []string{ref.ID}}
	if _, created, err := s.CreateSpawnIntent(spawn); err != nil || !created {
		t.Fatalf("spawn = %v %v", created, err)
	}
	if _, created, err := s.CreateSpawnIntent(spawn); err != nil || created {
		t.Fatalf("same-key retry = %v %v", created, err)
	}
	changed := spawn
	changed.ContextFactIDs = []string{pin.ID}
	if _, _, err := s.CreateSpawnIntent(changed); !errors.Is(err, ErrSpawnIdempotencyConflict) {
		t.Fatalf("changed context under one key = %v", err)
	}
	if _, created, err := s.CreateSpawnIntent(model.Intent{Title: "bad", CreatedBy: "pt_x", IdempotencyKey: "k2", ContextFactIDs: []string{"f_missing"}}); err == nil || created {
		t.Fatalf("spawn with unknown reference = %v %v", created, err)
	}
}
