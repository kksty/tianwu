package retrieval

import (
	"fmt"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
	"path/filepath"
	"testing"
)

func ledger(t *testing.T) *store.Store {
	t.Helper()
	s, e := store.Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func TestFactPagesKeepSnapshotAndAllFilters(t *testing.T) {
	s := ledger(t)
	a, _ := s.CreateIntent(model.Intent{Title: "a"})
	b, _ := s.CreateIntent(model.Intent{Title: "b"})
	ids := map[string]bool{}
	for i := 0; i < 7; i++ {
		f, e := s.AddFact(model.Fact{IntentID: a.ID, Kind: "note", Title: fmt.Sprintf("old %02d", i), Summary: "shared remote", Doc: "historical credential", Tags: []string{"context:x"}})
		if e != nil {
			t.Fatal(e)
		}
		ids[f.ID] = true
	}
	_, _ = s.AddFact(model.Fact{IntentID: b.ID, Kind: "note", Title: "shared remote", Doc: "credential"})
	q := FactQuery{Q: "shared credential", Kind: "note", Intent: a.ID, Tag: "context:x", Limit: 2, Ascending: true}
	page, e := Facts(s, q)
	if e != nil || len(page.Facts) != 2 || page.Total != 7 {
		t.Fatalf("%+v %v", page, e)
	}
	seen := map[string]bool{}
	for _, f := range page.Facts {
		seen[f.ID] = true
	}
	for _, f := range s.ListFacts(a.ID, 0) {
		_, e = s.UpdateFact(f.ID, func(f *model.Fact) { f.Summary = "changed"; f.Doc = "changed" })
		if e != nil {
			t.Fatal(e)
		}
	}
	_, _ = s.AddFact(model.Fact{IntentID: a.ID, Kind: "note", Title: "shared credential"})
	for page.HasMore {
		q.Cursor = page.NextCursor
		page, e = Facts(s, q)
		if e != nil {
			t.Fatal(e)
		}
		for _, f := range page.Facts {
			if seen[f.ID] || f.Doc != "historical credential" {
				t.Fatalf("moving snapshot: %+v", f)
			}
			seen[f.ID] = true
		}
	}
	if len(seen) != 7 {
		t.Fatalf("lost history: %d", len(seen))
	}
	q.Intent = b.ID
	q.Cursor = encode(cursor{Version: page.Version, Query: "wrong"})
	if _, e = Facts(s, q); e == nil {
		t.Fatal("changed query accepted")
	}
}
func TestUnacknowledgedContextReplaysAcrossRestart(t *testing.T) {
	s := ledger(t)
	for i := 0; i < 24; i++ {
		_, e := s.AddFact(model.Fact{Title: fmt.Sprintf("fact %d", i)})
		if e != nil {
			t.Fatal(e)
		}
	}
	first, e := WorkingContext(s, "commander")
	if e != nil {
		t.Fatal(e)
	}
	if !first.Changes.HasMore || !first.Survey.HasMore {
		t.Fatal("fixture did not paginate")
	}
	s, e = store.Open(s.Path())
	if e != nil {
		t.Fatal(e)
	}
	_, _ = s.AddFact(model.Fact{Title: "new after delivery"})
	again, e := WorkingContext(s, "commander")
	if e != nil || again.DeliveryID != first.DeliveryID || again.Changes.NextCursor != first.Changes.NextCursor {
		t.Fatalf("delivery moved: %v", e)
	}
	if e = s.AcknowledgeDelivery("commander", first.DeliveryID); e != nil {
		t.Fatal(e)
	}
	next, e := WorkingContext(s, "commander")
	if e != nil {
		t.Fatal(e)
	}
	if next.DeliveryID == first.DeliveryID || next.Survey.Facts[0].ID == first.Survey.Facts[0].ID {
		t.Fatal("survey starved behind newest changes")
	}
	if e = s.AcknowledgeDelivery("commander", first.DeliveryID); e == nil {
		t.Fatal("stale acknowledgement advanced cursor")
	}
}
func TestGraphRetainsAllPremisesAndReportsPageBoundaries(t *testing.T) {
	s := ledger(t)
	refs := []string{}
	for i := 0; i < 17; i++ {
		f, _ := s.AddFact(model.Fact{Title: fmt.Sprintf("premise %d", i)})
		refs = append(refs, f.ID)
	}
	in, _ := s.CreateIntent(model.Intent{Title: "joint premise", FromFactIDs: refs})
	q := GraphQuery{Roots: []string{"intent:" + in.ID}, Direction: "ancestors", Depth: 0, Limit: 3}
	seen := map[string]bool{}
	boundaries := 0
	for {
		p, e := Graph(s, q)
		if e != nil {
			t.Fatal(e)
		}
		for _, f := range p.Facts {
			seen[f.ID] = true
		}
		for _, i := range p.Intents {
			if len(i.FromFactIDs) != 17 {
				t.Fatal("premises clipped")
			}
		}
		boundaries += len(p.Boundary)
		if !p.HasMore {
			break
		}
		q.Cursor = p.NextCursor
	}
	if len(seen) != 17 || boundaries == 0 {
		t.Fatalf("facts=%d boundary=%d", len(seen), boundaries)
	}
}
func TestChangesFinishFixedBatchBeforeNewerWrites(t *testing.T) {
	s := ledger(t)
	for i := 0; i < 5; i++ {
		_, _ = s.AddFact(model.Fact{Title: fmt.Sprint(i)})
	}
	p, e := Changes(s, "", 2)
	if e != nil {
		t.Fatal(e)
	}
	watermark := p.HighWatermark
	_, _ = s.AddFact(model.Fact{Title: "later"})
	for p.HasMore {
		p, e = Changes(s, p.NextCursor, 2)
		if e != nil {
			t.Fatal(e)
		}
		if p.HighWatermark != watermark {
			t.Fatal("new writes moved the batch")
		}
	}
	p, e = Changes(s, p.NextCursor, 2)
	if e != nil || len(p.Changes) != 1 {
		t.Fatalf("later changes lost: %+v %v", p, e)
	}
}
