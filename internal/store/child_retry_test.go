package store

import (
	"errors"
	"testing"

	"github.com/kksty/tianwu/internal/model"
)

func TestWorkerRetryCannotChangeSkills(t *testing.T) {
	for _, mode := range []string{"child", "spawn"} {
		t.Run(mode, func(t *testing.T) {
			s := open(t)
			parent, err := s.CreateIntent(model.Intent{Title: "parent"})
			if err != nil {
				t.Fatal(err)
			}
			create := func(request model.Intent) (model.Intent, bool, error) {
				if mode == "child" {
					return s.CreateChildIntent(parent.ID, request)
				}
				request.ParentID = parent.ID
				return s.CreateSpawnIntent(request)
			}
			request := model.Intent{Title: "worker", IdempotencyKey: "same", Skills: []string{"original"}}
			worker, _, err := create(request)
			if err != nil {
				t.Fatal(err)
			}
			if retry, created, err := create(request); err != nil || created || retry.ID != worker.ID {
				t.Fatalf("identical retry: %v, %v", created, err)
			}
			request.Skills = []string{"changed"}
			wantErr := ErrChildIdempotencyConflict
			if mode == "spawn" {
				wantErr = ErrSpawnIdempotencyConflict
			}
			if _, _, err := create(request); !errors.Is(err, wantErr) {
				t.Fatalf("changed skills must conflict: %v", err)
			}
			if len(s.Children(parent.ID)) != 1 {
				t.Fatal("retry created an extra worker")
			}
		})
	}
}

func TestCreatingChildUpdatesParentTimestampWhenIntentSliceGrows(t *testing.T) {
	s := open(t)
	parent, err := s.CreateIntent(model.Intent{Title: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	// Force the next append to allocate: a pointer into the old slice must not
	// receive the parent's update after the backing array has been replaced.
	s.mu.Lock()
	s.db.Intents = append([]model.Intent(nil), s.db.Intents...)
	s.db.Intents = s.db.Intents[:len(s.db.Intents):len(s.db.Intents)]
	s.mu.Unlock()
	child, _, err := s.CreateChildIntent(parent.ID, model.Intent{Title: "child"})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetIntent(parent.ID)
	if !got.UpdatedAt.Equal(child.CreatedAt) {
		t.Fatalf("parent timestamp stayed at %v, want child creation time %v", got.UpdatedAt, child.CreatedAt)
	}
}
