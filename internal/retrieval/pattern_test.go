package retrieval

import (
	"fmt"
	"testing"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

func TestExperienceRequiresDirectedProvenanceAndExplicitEntityBinding(t *testing.T) {
	p := &model.ExperiencePattern{Nodes: []model.PatternNode{{Name: "a", Type: "fact", Tags: []string{"before"}, EntityVar: "target"}, {Name: "step", Type: "intent"}, {Name: "b", Type: "fact", Tags: []string{"after"}, EntityVar: "target"}}, Edges: []model.PatternEdge{{Source: "a", Target: "step", Kind: "premise"}, {Source: "step", Target: "b", Kind: "produces"}}}
	db := store.DB{Facts: []model.Fact{{ID: "a", Tags: []string{"before"}, EntityRef: "target-1"}, {ID: "b", Tags: []string{"after"}, IntentID: "step", EntityRef: "target-1"}}, Intents: []model.Intent{{ID: "step", FromFactIDs: []string{"a"}}}}
	check := func(want string) {
		t.Helper()
		got := MatchPattern(db, p, []string{"a", "b"})
		if got.State != want {
			t.Fatalf("want %s got %+v", want, got)
		}
	}
	check("matched")
	db.Facts[1].EntityRef = "target-2"
	check("missing")
	db.Facts[1].EntityRef = ""
	check("unknown")
	db.Facts[1].EntityRef = "target-1"
	db.Intents[0].FromFactIDs = nil
	check("missing")
	db.Intents[0].FromFactIDs = []string{"a"}
	db.Facts[1].Status = "rejected"
	check("missing")
}

func TestPatternSearchExhaustionStaysUnknown(t *testing.T) {
	p := &model.ExperiencePattern{
		Nodes: []model.PatternNode{{Name: "fact", Type: "fact"}, {Name: "step", Type: "intent"}},
		Edges: []model.PatternEdge{{Source: "fact", Target: "step", Kind: "premise"}},
	}
	db := store.DB{}
	ids := []string{}
	for i := 0; i < 101; i++ {
		id := fmt.Sprint(i)
		db.Facts = append(db.Facts, model.Fact{ID: id})
		db.Intents = append(db.Intents, model.Intent{ID: id})
		ids = append(ids, id)
	}
	got := MatchPattern(db, p, ids)
	if got.State != "unknown" || got.Reason != "pattern search budget exhausted" || len(got.Bindings) != 0 {
		t.Fatalf("exhaustion must not assert applicability: %+v", got)
	}
}
