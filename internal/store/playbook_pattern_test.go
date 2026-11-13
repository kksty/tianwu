package store

import (
	"testing"

	"github.com/kksty/tianwu/internal/model"
)

func TestExperiencePatternRequiresConnectedTypedRelationships(t *testing.T) {
	for _, p := range []*model.ExperiencePattern{
		{},
		{Nodes: []model.PatternNode{{Name: "a", Type: "fact"}, {Name: "b", Type: "fact"}}},
		{Nodes: []model.PatternNode{{Name: "a", Type: "fact"}, {Name: "b", Type: "fact"}}, Edges: []model.PatternEdge{{Source: "a", Target: "b", Kind: "premise"}}},
		{Nodes: []model.PatternNode{{Name: "a", Type: "intent", EntityVar: "target"}}},
	} {
		if err := ValidatePattern(p); err == nil {
			t.Fatalf("invalid pattern accepted: %+v", p)
		}
	}
}

func TestExperienceSnapshotsAndWriterResultsOwnTheirProvenance(t *testing.T) {
	s := open(t)
	f, _ := s.AddFact(model.Fact{Title: "source"})
	input := model.Playbook{Title: "experience", Origin: model.PlaybookFromExecutor, FromFactIDs: []string{f.ID}, Pattern: &model.ExperiencePattern{Nodes: []model.PatternNode{{Name: "source", Type: "fact", Tags: []string{"observed"}}}}}
	input.FromFactVersions = map[string]uint64{f.ID: 1}
	p, _, err := s.UpsertPlaybook(input)
	if err != nil {
		t.Fatal(err)
	}
	poison := func(p model.Playbook) {
		p.Pattern.Nodes[0].Tags[0] = "changed"
		p.FromFactVersions[f.ID] = 999
	}
	poison(input)
	poison(p)
	poison(s.Snapshot().Playbooks[0])
	ignored, changed, err := s.UpsertPlaybook(model.Playbook{Title: p.Title, Origin: model.PlaybookFromCommander})
	if err != nil || changed {
		t.Fatal("commander rewrote confirmed experience", err)
	}
	poison(ignored)
	retired, err := s.RetirePlaybook(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	poison(retired)
	saved, _ := s.PlaybookByID(p.ID)
	if saved.Pattern.Nodes[0].Tags[0] != "observed" || saved.FromFactVersions[f.ID] != 1 {
		t.Fatal("caller changed stored provenance", saved)
	}
}

func TestExecutorRedeclarationDoesNotRestoreArchivedExperience(t *testing.T) {
	s := open(t)
	p, _, _ := s.UpsertPlaybook(model.Playbook{Title: "experience", Origin: model.PlaybookFromExecutor, Doc: "first"})
	_, err := s.ArchivePlaybook(p.ID, p.UpdatedAt, true)
	if err != nil {
		t.Fatal(err)
	}
	updated, changed, err := s.UpsertPlaybook(model.Playbook{Title: p.Title, Origin: model.PlaybookFromExecutor, Doc: "second", SourceRunID: "latest"})
	if err != nil || !changed || !updated.Archived || updated.Doc != "second" || updated.SourceRunID != "latest" {
		t.Fatalf("redeclaration changed curation or lost content: %+v, %v", updated, err)
	}
}
