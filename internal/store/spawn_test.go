package store

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/kksty/tianwu/internal/model"
)

func newSpawnStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Repeating the same idempotency key finds the same worker; changing the
// payload under that key is a conflict, not a second worker.
func TestCreateSpawnIntentIsIdempotent(t *testing.T) {
	st := newSpawnStore(t)
	req := model.Intent{
		Title: "独立核对", Description: "只读", IdempotencyKey: "spawn-1",
		CreatedBy: "pt_in_a", DelegatedByRun: "r_a",
	}
	first, isNew, err := st.CreateSpawnIntent(req)
	if err != nil || !isNew {
		t.Fatalf("first: new=%v err=%v", isNew, err)
	}
	second, isNew, err := st.CreateSpawnIntent(req)
	if err != nil || isNew || second.ID != first.ID {
		t.Fatalf("retry: new=%v id=%s want %s err=%v", isNew, second.ID, first.ID, err)
	}
	if n := len(st.ListIntents()); n != 1 {
		t.Fatalf("intents = %d, want 1", n)
	}
	conflict := req
	conflict.Title = "另一个任务"
	if _, _, err := st.CreateSpawnIntent(conflict); !errors.Is(err, ErrSpawnIdempotencyConflict) {
		t.Fatalf("conflict err = %v", err)
	}
}

// A spawn under a parent direction inherits the operator constraints, skills
// and node default; it must not inherit the parent's model profile.
func TestCreateSpawnIntentInheritsParentContext(t *testing.T) {
	st := newSpawnStore(t)
	parent, err := st.CreateIntent(model.Intent{
		Title: "父方向", Doc: "操作者约束", Skills: []string{"collab"}, Node: "lab",
	})
	if err != nil {
		t.Fatal(err)
	}
	child, isNew, err := st.CreateSpawnIntent(model.Intent{
		Title: "子工作者", ParentID: parent.ID, CreatedBy: "pt_in_a",
		IdempotencyKey: "spawn-child",
	})
	if err != nil || !isNew {
		t.Fatalf("spawn: new=%v err=%v", isNew, err)
	}
	if child.Doc != "操作者约束" || len(child.Skills) != 1 || child.Skills[0] != "collab" || child.Node != "lab" {
		t.Fatalf("child did not inherit parent context: %+v", child)
	}
	if child.Profile != "" {
		t.Fatalf("child inherited a profile: %q", child.Profile)
	}
	if child.ParentID != parent.ID || child.Status != model.StatusPending {
		t.Fatalf("child = %+v", child)
	}
}

// A parentless spawn is legal: not every worker is a child of an existing
// direction (the commander can start independent work).
func TestCreateSpawnIntentWithoutParent(t *testing.T) {
	st := newSpawnStore(t)
	worker, isNew, err := st.CreateSpawnIntent(model.Intent{
		Title: "独立工作者", CreatedBy: "pt_in_a", IdempotencyKey: "spawn-root",
	})
	if err != nil || !isNew || worker.ParentID != "" {
		t.Fatalf("worker = %+v new=%v err=%v", worker, isNew, err)
	}
}

// Spawning under a finished parent is refused: the work would belong to a
// direction nobody is waiting for.
func TestCreateSpawnIntentRefusesFinishedParent(t *testing.T) {
	st := newSpawnStore(t)
	parent, err := st.CreateIntent(model.Intent{Title: "结束后"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIntent(parent.ID, func(i *model.Intent) { i.Status = model.StatusDone }); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateSpawnIntent(model.Intent{
		Title: "晚了", ParentID: parent.ID, CreatedBy: "pt_in_a",
	}); err == nil {
		t.Fatal("spawn under a finished parent was accepted")
	}
}
