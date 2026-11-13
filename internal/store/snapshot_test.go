package store

import (
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/model"
)

func TestIntentSnapshotsOwnAllMutableFields(t *testing.T) {
	s := open(t)
	request := model.Intent{
		Title: "detached", FromFactIDs: []string{"f_original"}, Skills: []string{"original"},
		Notes:         []model.IntentNote{{Text: "original"}},
		Interjections: []model.Interjection{{Text: "original"}},
	}
	in, err := s.CreateIntent(request)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := s.GetIntent(in.ID)
	mutate := func(in model.Intent) {
		in.FromFactIDs[0] = "changed"
		in.Skills[0] = "changed"
		in.Notes[0].Text = "changed"
		in.Interjections[0].Text = "changed"
	}
	for name, snapshot := range map[string]model.Intent{
		"input": request, "created": in, "get": want,
		"list": s.ListIntents()[0], "snapshot": s.Snapshot().Intents[0],
	} {
		t.Run(name, func(t *testing.T) {
			mutate(snapshot)
			got, _ := s.GetIntent(in.ID)
			if got.FromFactIDs[0] != "f_original" || got.Skills[0] != "original" ||
				got.Notes[0].Text != "original" || got.Interjections[0].Text != "original" {
				t.Fatal("snapshot mutation changed the stored intent")
			}
		})
	}
	// The earlier snapshot must also stay stable when a writer edits nested fields.
	before, _ := s.GetIntent(in.ID)
	updated, err := s.UpdateIntent(in.ID, func(i *model.Intent) { i.Skills[0] = "updated" })
	if err != nil {
		t.Fatal(err)
	}
	if before.Skills[0] != "original" || updated.Skills[0] != "updated" {
		t.Fatal("stored update changed an earlier snapshot")
	}
}

func TestChildCreationOwnsMutableInput(t *testing.T) {
	s := open(t)
	parent, err := s.CreateIntent(model.Intent{Title: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	request := model.Intent{Title: "child", Skills: []string{"original"}, FromFactIDs: []string{"f_original"}}
	child, _, err := s.CreateChildIntent(parent.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	request.Skills[0], request.FromFactIDs[0] = "changed", "changed"
	got, _ := s.GetIntent(child.ID)
	if got.Skills[0] != "original" || got.FromFactIDs[0] != "f_original" {
		t.Fatal("child shares mutable input with caller")
	}
}

func TestSpawnCreationOwnsMutableInputAndOutput(t *testing.T) {
	s := open(t)
	request := model.Intent{Title: "worker", Skills: []string{"original"}, FromFactIDs: []string{"f_original"}}
	worker, _, err := s.CreateSpawnIntent(request)
	if err != nil {
		t.Fatal(err)
	}
	request.Skills[0], request.FromFactIDs[0] = "changed input", "changed input"
	worker.Skills[0], worker.FromFactIDs[0] = "changed output", "changed output"
	got, _ := s.GetIntent(worker.ID)
	if got.Skills[0] != "original" || got.FromFactIDs[0] != "f_original" {
		t.Fatal("spawn shares mutable input or output with caller")
	}
}

func TestWaitSnapshotsFromChildAndSettlementAPIsAreDetached(t *testing.T) {
	s := open(t)
	parent, err := s.CreateIntent(model.Intent{Title: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	child, _, err := s.CreateChildIntent(parent.ID, model.Intent{Title: "child"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateIntent(child.ID, func(in *model.Intent) { in.Status = model.StatusRunning; in.RunID = "r_child" }); err != nil {
		t.Fatal(err)
	}
	request, err := s.RegisterOperatorWait(child.ID, "r_child", model.OperatorWait{Request: "login", AccessIDs: []string{"ac_original"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteOperatorWait(child.ID, request.ID, "done"); err != nil {
		t.Fatal(err)
	}
	out, err := s.SettleIntentForRun(child.ID, "r_child", model.RunDone)
	if err != nil {
		t.Fatal(err)
	}
	woken, _, err := s.SatisfyWait(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := s.UpdateIntent(child.ID, func(in *model.Intent) {})
	if err != nil {
		t.Fatal(err)
	}
	for name, wait := range map[string]*model.Wait{
		"children":    s.Children(parent.ID)[0].Wait,
		"descendants": s.Descendants(parent.ID)[0].Wait,
		"settlement":  out.Wait,
		"satisfied":   woken.Wait,
		"updated":     updated.Wait,
		"get":         func() *model.Wait { in, _ := s.GetIntent(child.ID); return in.Wait }(),
	} {
		t.Run(name, func(t *testing.T) {
			wait.Operator.AccessIDs[0] = "ac_mutated"
			*wait.Operator.CompletedAt = time.Time{}
			got, _ := s.GetIntent(child.ID)
			if got.Wait.Operator.AccessIDs[0] != "ac_original" || got.Wait.Operator.CompletedAt.IsZero() {
				t.Fatal("caller changed stored wait through a snapshot")
			}
		})
	}
}

func TestOperatorWaitOwnsRequestAccessIDs(t *testing.T) {
	s, in := operatorStore(t)
	ids := []string{"ac_original"}
	request, err := s.RegisterOperatorWait(in.ID, in.RunID, model.OperatorWait{Request: "login", AccessIDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	ids[0] = "ac_input_mutated"
	request.AccessIDs[0] = "ac_output_mutated"
	got, _ := s.GetIntent(in.ID)
	if got.Wait.Operator.AccessIDs[0] != "ac_original" {
		t.Fatal("request access IDs alias stored wait")
	}
}

func TestChildWaitOwnsChildrenAndDeadline(t *testing.T) {
	s := open(t)
	parent, err := s.CreateIntent(model.Intent{Title: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	child, _, err := s.CreateChildIntent(parent.ID, model.Intent{Title: "child"})
	if err != nil {
		t.Fatal(err)
	}
	children := []string{child.ID}
	deadline := time.Now().Add(time.Hour)
	if _, err := s.RegisterWait(parent.ID, "r_parent", model.Wait{Children: children, Deadline: &deadline}); err != nil {
		t.Fatal(err)
	}
	children[0] = "in_missing"
	deadline = time.Time{}
	got, _ := s.GetIntent(parent.ID)
	if got.Wait.Children[0] != child.ID || got.Wait.Deadline.IsZero() {
		t.Fatal("registration retained caller-owned children or deadline")
	}
	*got.Wait.Deadline = time.Time{}
	if state, ok := s.EvaluateWait(parent.ID); !ok || state.Ready {
		t.Fatal("mutating snapshot deadline satisfied the stored wait")
	}
}
