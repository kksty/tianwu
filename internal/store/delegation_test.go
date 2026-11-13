package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/model"
)

func childFor(parent model.Intent, title, key string) model.Intent {
	return model.Intent{
		Title: title, Description: "d", CreatedBy: "pt_" + parent.ID,
		DelegatedByRun: parent.RunID, IdempotencyKey: key,
	}
}

func TestCreateChildIntentLinksAndInherits(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	parent, err := s.CreateIntent(model.Intent{Title: "父", Doc: "操作员约束"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateIntent(parent.ID, func(i *model.Intent) { i.Status = model.StatusRunning }); err != nil {
		t.Fatal(err)
	}
	parent, _ = s.GetIntent(parent.ID)
	child, created, err := s.CreateChildIntent(parent.ID, childFor(parent, "子", "k1"))
	if err != nil || !created {
		t.Fatalf("create: %v created=%v", err, created)
	}
	if child.ParentID != parent.ID || child.Status != model.StatusPending ||
		child.Doc != "操作员约束" || child.CreatedBy != "pt_"+parent.ID {
		t.Fatalf("child = %+v", child)
	}
	if got := s.Children(parent.ID); len(got) != 1 || got[0].ID != child.ID {
		t.Fatalf("children = %+v", got)
	}
	// The same key returns the original row, not a second assistant.
	again, created, err := s.CreateChildIntent(parent.ID, childFor(parent, "子", "k1"))
	if err != nil || created || again.ID != child.ID {
		t.Fatalf("retry: %+v created=%v err=%v", again, created, err)
	}
	// Same key, different task is a caller bug.
	if _, _, err := s.CreateChildIntent(parent.ID, childFor(parent, "另一个任务", "k1")); !errors.Is(err, ErrChildIdempotencyConflict) {
		t.Fatalf("conflict err = %v", err)
	}
	// A finished parent cannot delegate.
	if _, err := s.UpdateIntent(parent.ID, func(i *model.Intent) { i.Status = model.StatusDone }); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateChildIntent(parent.ID, childFor(parent, "晚了", "k2")); err == nil {
		t.Fatal("delegation from a done parent was accepted")
	}
}

func TestWaitEvaluationAndImmediateReady(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	parent, _ := s.CreateIntent(model.Intent{Title: "父"})
	childA, _, _ := s.CreateChildIntent(parent.ID, childFor(parent, "A", ""))
	childB, _, _ := s.CreateChildIntent(parent.ID, childFor(parent, "B", ""))
	parent, _ = s.GetIntent(parent.ID)
	if _, err := s.UpdateIntent(parent.ID, func(i *model.Intent) { i.Status = model.StatusRunning }); err != nil {
		t.Fatal(err)
	}
	// Pending children: register stores the wait and reports not-ready.
	state, err := s.RegisterWait(parent.ID, "r_parent", model.Wait{
		Children: []string{childA.ID, childB.ID}, Mode: model.WaitAll,
	})
	if err != nil || state.Ready {
		t.Fatalf("register: %+v %v", state, err)
	}
	cur, _ := s.GetIntent(parent.ID)
	if cur.Wait == nil || cur.Wait.RegisteredByRun != "r_parent" || len(cur.Wait.Children) != 2 {
		t.Fatalf("wait = %+v", cur.Wait)
	}
	// One child done is not enough for all.
	if _, err := s.UpdateIntent(childA.ID, func(i *model.Intent) { i.Status = model.StatusDone }); err != nil {
		t.Fatal(err)
	}
	if state, _ := s.EvaluateWait(parent.ID); state.Ready {
		t.Fatal("all-wait ready with one child pending")
	}
	// The finish path: parent's run ends while one child is still open -> waiting.
	if _, err := s.UpdateIntent(parent.ID, func(i *model.Intent) { i.RunID = "r_parent" }); err != nil {
		t.Fatal(err)
	}
	out, err := s.SettleIntentForRun(parent.ID, "r_parent", model.RunDone)
	if err != nil || out.Status != model.StatusWaiting || out.Wake {
		t.Fatalf("settle: %+v %v", out, err)
	}
	// Second child done -> waiting condition ready -> parent pending to wake.
	if _, err := s.UpdateIntent(childB.ID, func(i *model.Intent) { i.Status = model.StatusDone }); err != nil {
		t.Fatal(err)
	}
	state, ok := s.EvaluateWait(parent.ID)
	if !ok || !state.Ready || len(state.Results) != 2 {
		t.Fatalf("evaluate: %+v ok=%v", state, ok)
	}
}

func TestWaitAnyAndAlreadySettled(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	parent, _ := s.CreateIntent(model.Intent{Title: "父"})
	a, _, _ := s.CreateChildIntent(parent.ID, childFor(parent, "A", ""))
	b, _, _ := s.CreateChildIntent(parent.ID, childFor(parent, "B", ""))
	if _, err := s.UpdateIntent(a.ID, func(i *model.Intent) { i.Status = model.StatusDone }); err != nil {
		t.Fatal(err)
	}
	parent, _ = s.GetIntent(parent.ID)
	if _, err := s.UpdateIntent(parent.ID, func(i *model.Intent) { i.Status = model.StatusRunning }); err != nil {
		t.Fatal(err)
	}
	state, err := s.RegisterWait(parent.ID, "r", model.Wait{Children: []string{a.ID, b.ID}, Mode: model.WaitAny})
	if err != nil || !state.Ready || len(state.Results) != 1 || state.Results[0].ChildID != a.ID {
		t.Fatalf("any wait: %+v %v", state, err)
	}
	// Ready on registration must not leave a wait that parks the parent later.
	cur, _ := s.GetIntent(parent.ID)
	if cur.Wait != nil {
		t.Fatalf("ready wait stored: %+v", cur.Wait)
	}
	// A child not parented by this intent is rejected.
	other, _ := s.CreateIntent(model.Intent{Title: "别的"})
	if _, err := s.RegisterWait(parent.ID, "r", model.Wait{Children: []string{other.ID}, Mode: model.WaitAll}); !errors.Is(err, ErrWaitInvalid) {
		t.Fatalf("foreign child err = %v", err)
	}
}

func TestWaitTimeoutIsReady(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	parent, _ := s.CreateIntent(model.Intent{Title: "父"})
	child, _, _ := s.CreateChildIntent(parent.ID, childFor(parent, "A", ""))
	parent, _ = s.GetIntent(parent.ID)
	if _, err := s.UpdateIntent(parent.ID, func(i *model.Intent) { i.Status = model.StatusRunning }); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Minute)
	state, err := s.RegisterWait(parent.ID, "r", model.Wait{
		Children: []string{child.ID}, Mode: model.WaitAll, Deadline: &past,
	})
	if err != nil || !state.Ready || state.Reason != model.WaitReasonTimeout || len(state.Pending) != 1 {
		t.Fatalf("timeout: %+v %v", state, err)
	}
}

func TestSettleImplicitWaitAndStaleRun(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	parent, _ := s.CreateIntent(model.Intent{Title: "父"})
	child, _, _ := s.CreateChildIntent(parent.ID, childFor(parent, "A", ""))
	if _, err := s.UpdateIntent(parent.ID, func(i *model.Intent) {
		i.Status = model.StatusRunning
		i.RunID = "r1"
	}); err != nil {
		t.Fatal(err)
	}
	// A parent that finishes without a wait still has an open child: implicit all.
	out, err := s.SettleIntentForRun(parent.ID, "r1", model.RunDone)
	if err != nil || out.Status != model.StatusWaiting || out.Wait == nil || !out.Wait.Implicit {
		t.Fatalf("implicit: %+v %v", out, err)
	}
	// A stale finish must not touch the intent.
	cur, _ := s.GetIntent(parent.ID)
	if _, err := s.UpdateIntent(parent.ID, func(i *model.Intent) { i.RunID = "r2" }); err != nil {
		t.Fatal(err)
	}
	out, err = s.SettleIntentForRun(parent.ID, "r1", model.RunDone)
	if err != nil || !out.Stale {
		t.Fatalf("stale: %+v %v", out, err)
	}
	if now, _ := s.GetIntent(parent.ID); now.Status != cur.Status {
		t.Fatalf("stale finish changed status: %s -> %s", cur.Status, now.Status)
	}
	_ = child
}

func TestAbortedChildSettlesFailed(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	parent, _ := s.CreateIntent(model.Intent{Title: "父"})
	child, _, _ := s.CreateChildIntent(parent.ID, childFor(parent, "A", ""))
	if _, err := s.UpdateIntent(child.ID, func(i *model.Intent) {
		i.Status = model.StatusRunning
		i.RunID = "r_child"
		i.Aborted = true
	}); err != nil {
		t.Fatal(err)
	}
	out, err := s.SettleIntentForRun(child.ID, "r_child", model.RunCanceled)
	if err != nil || out.Status != model.StatusFailed {
		t.Fatalf("aborted: %+v %v", out, err)
	}
}

func TestDescendantsBreadthFirst(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	root, _ := s.CreateIntent(model.Intent{Title: "root"})
	c1, _, _ := s.CreateChildIntent(root.ID, childFor(root, "c1", ""))
	c2, _, _ := s.CreateChildIntent(root.ID, childFor(root, "c2", ""))
	g1, _, _ := s.CreateChildIntent(c1.ID, childFor(c1, "g1", ""))
	got := s.Descendants(root.ID)
	if len(got) != 3 {
		t.Fatalf("descendants = %d", len(got))
	}
	ids := map[string]bool{got[0].ID: true, got[1].ID: true, got[2].ID: true}
	for _, id := range []string{c1.ID, c2.ID, g1.ID} {
		if !ids[id] {
			t.Fatalf("missing %s in %v", id, got)
		}
	}
}
