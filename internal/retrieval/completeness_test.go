package retrieval

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/model"
)

func TestAllExperienceConditionsSurviveAndReportTheMissingLastOne(t *testing.T) {
	s := ledger(t)
	tags := []string{}
	for i := 0; i < 12; i++ {
		tags = append(tags, fmt.Sprintf("condition:%d", i))
	}
	_, _ = s.AddFact(model.Fact{Title: "partial conditions", Tags: tags[:11]})
	p, _, err := s.UpsertPlaybook(model.Playbook{Title: "experience", Origin: model.PlaybookFromExecutor, RequiredTags: tags})
	if err != nil {
		t.Fatal(err)
	}
	rows := ExperienceCandidates(s.Snapshot(), ExperienceQuery{})
	if len(p.RequiredTags) != 12 || len(rows) != 1 || rows[0].Match != "missing" || len(rows[0].MissingTags) != 1 || rows[0].MissingTags[0] != tags[11] {
		t.Fatalf("lost condition: %+v", rows)
	}
}

func TestGraphPagesBoundFanoutAndKeepEveryRelationship(t *testing.T) {
	s := ledger(t)
	hub, _ := s.AddFact(model.Fact{Title: "hub", Doc: strings.Repeat("long", 20000)})
	for i := 0; i < 90; i++ {
		_, _ = s.CreateIntent(model.Intent{Title: fmt.Sprint(i), FromFactIDs: []string{hub.ID}})
	}
	q := GraphQuery{Roots: []string{"fact:" + hub.ID}, Direction: "descendants", Depth: 1, Limit: 1, MaxBytes: 4096}
	edges := map[string]bool{}
	nodes := map[string]bool{}
	hadDetail := false
	for pages := 0; pages < 300; pages++ {
		p, err := Graph(s, q)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(p)
		if len(raw) > 4096 || len(p.Edges) > 1 || len(p.Boundary) > 1 {
			t.Fatalf("page exceeded budget: %d", len(raw))
		}
		if len(p.Details) > 0 {
			hadDetail = true
		}
		for _, f := range p.Facts {
			nodes[f.ID] = true
		}
		for _, in := range p.Intents {
			nodes[in.ID] = true
		}
		for _, e := range p.Edges {
			edges[e.Source+e.Target+e.Kind] = true
		}
		if !p.HasMore {
			if len(edges) != 90 || len(nodes) != 91 || !hadDetail {
				t.Fatal("incomplete graph")
			}
			return
		}
		if p.NextCursor == q.Cursor {
			t.Fatal("cursor did not advance")
		}
		q.Cursor = p.NextCursor
	}
	t.Fatal("graph did not finish")
}

func TestExperienceArchiveRetainsMoreThanThreeHundredEntriesAndHistory(t *testing.T) {
	s := ledger(t)
	var first model.Playbook
	for i := 0; i < 305; i++ {
		p, _, err := s.UpsertPlaybook(model.Playbook{Title: fmt.Sprintf("experience %03d", i), Origin: model.PlaybookFromExecutor})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = p
		}
	}
	if len(s.Playbooks()) != 305 {
		t.Fatal("experience was evicted")
	}
	archived, err := s.ArchivePlaybook(first.ID, first.UpdatedAt, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ArchivePlaybook(first.ID, first.UpdatedAt, false); err == nil {
		t.Fatal("stale curation accepted")
	}
	for _, p := range ExperienceCandidates(s.Snapshot(), ExperienceQuery{}) {
		if p.Playbook.ID == first.ID {
			t.Fatal("archived experience recalled")
		}
	}
	p, err := Playbooks(s, PlaybookQuery{Archived: "only", Limit: 1})
	if err != nil || p.Total != 1 || p.Playbooks[0].ID != first.ID {
		t.Fatal("archive not queryable")
	}
	h, err := History(s, "playbook", first.ID, "", 10)
	if err != nil || len(h.History) != 2 {
		t.Fatal("history missing")
	}
	if _, err = s.ArchivePlaybook(first.ID, archived.UpdatedAt, false); err != nil {
		t.Fatal(err)
	}
}

func TestHistoricalRootedGraphDoesNotAddUnrelatedEdges(t *testing.T) {
	s := ledger(t)
	root, _ := s.AddFact(model.Fact{Title: "root"})
	step, _ := s.CreateIntent(model.Intent{Title: "related", FromFactIDs: []string{root.ID}})
	other, _ := s.AddFact(model.Fact{Title: "unrelated"})
	_, _ = s.CreateIntent(model.Intent{Title: "other", FromFactIDs: []string{other.ID}})
	q := GraphQuery{Roots: []string{"fact:" + root.ID}, Direction: "descendants", Depth: 1, Limit: 1}
	page, err := Graph(s, q)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s.UpdateFact(other.ID, func(f *model.Fact) { f.Summary = "new observation" })
	for count := 0; ; count++ {
		if count > 10 || page.EdgesTotal != 1 {
			t.Fatalf("query escaped its subgraph: %+v", page)
		}
		for _, edge := range page.Edges {
			if edge.Source != "fact:"+root.ID || edge.Target != "intent:"+step.ID {
				t.Fatalf("unrelated edge: %+v", edge)
			}
		}
		if !page.HasMore {
			break
		}
		q.Cursor = page.NextCursor
		page, err = Graph(s, q)
		if err != nil {
			t.Fatal(err)
		}
	}
}
