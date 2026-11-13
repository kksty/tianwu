package retrieval

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

type focusFixture struct {
	s           *store.Store
	task        model.Intent
	required    model.Fact
	pinned      model.Fact
	reference   model.Fact
	provenance  model.Fact
	crossBranch model.Fact
	unrelated   model.Fact
}

// focusFixtures builds one task whose hot/warm/cold rows all come from recorded
// references, never from titles or prose.
func focusFixtures(t *testing.T) *focusFixture {
	t.Helper()
	s := ledger(t)
	t.Cleanup(func() { _ = s.Close() })
	// The provenance fact is produced by an earlier direction; a fact cannot be
	// its own producer's premise (the structured graph check rejects that).
	root, err := s.CreateIntent(model.Intent{Title: "root"})
	if err != nil {
		t.Fatal(err)
	}
	ancestor, err := s.CreateIntent(model.Intent{Title: "ancestor"})
	if err != nil {
		t.Fatal(err)
	}
	required, err := s.AddFact(model.Fact{IntentID: ancestor.ID, Kind: "cred", Title: "required", EntityRef: "host:web"})
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := s.AddFact(model.Fact{IntentID: ancestor.ID, Kind: "service", Title: "pinned"})
	if err != nil {
		t.Fatal(err)
	}
	reference, err := s.AddFact(model.Fact{IntentID: ancestor.ID, Kind: "host", Title: "reference"})
	if err != nil {
		t.Fatal(err)
	}
	provenance, err := s.AddFact(model.Fact{IntentID: root.ID, Kind: "note", Title: "provenance"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateIntent(model.Intent{Title: "other branch"})
	if err != nil {
		t.Fatal(err)
	}
	crossBranch, err := s.AddFact(model.Fact{IntentID: other.ID, Kind: "host", Title: "same entity elsewhere", EntityRef: "host:web"})
	if err != nil {
		t.Fatal(err)
	}
	unrelated, err := s.AddFact(model.Fact{IntentID: other.ID, Kind: "note", Title: "unrelated"})
	if err != nil {
		t.Fatal(err)
	}
	// The ancestor's own FromFactIDs make `provenance` part of the task's
	// recorded source chain, which is how TaskSources reaches it.
	if _, err := s.UpdateIntent(ancestor.ID, func(in *model.Intent) {
		in.FromFactIDs = []string{provenance.ID}
	}); err != nil {
		t.Fatal(err)
	}
	task, err := s.CreateIntent(model.Intent{
		Title:          "focus",
		ParentID:       ancestor.ID,
		FromFactIDs:    []string{required.ID},
		PinnedFactIDs:  []string{pinned.ID},
		ContextFactIDs: []string{reference.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &focusFixture{
		s: s, task: task, required: required, pinned: pinned, reference: reference,
		provenance: provenance, crossBranch: crossBranch, unrelated: unrelated,
	}
}

func focusVersionsOf(t *testing.T, s *store.Store) map[string]uint64 {
	t.Helper()
	_, version := s.ReadView()
	versions, err := s.FactVersionsAt(version)
	if err != nil {
		t.Fatal(err)
	}
	return versions
}

func focusRowsByID(rows []FocusFact) map[string]FocusFact {
	out := make(map[string]FocusFact, len(rows))
	for _, row := range rows {
		out[row.ID] = row
	}
	return out
}

func hasReason(row FocusFact, reason string) bool {
	for _, got := range row.Reasons {
		if got == reason {
			return true
		}
	}
	return false
}

func TestFocusClassifiesByRecordedReference(t *testing.T) {
	f := focusFixtures(t)
	db, _ := f.s.ReadView()
	rows, err := FocusItems(db, f.task.ID, focusVersionsOf(t, f.s))
	if err != nil {
		t.Fatal(err)
	}
	byID := focusRowsByID(rows)
	cases := []struct {
		fact     model.Fact
		wantTemp string
		want     string
	}{
		{f.required, FocusHot, FocusReasonRequired},
		{f.pinned, FocusHot, FocusReasonPinned},
		{f.reference, FocusHot, FocusReasonReference},
		{f.provenance, FocusWarm, FocusReasonProvenance},
		{f.crossBranch, FocusWarm, FocusReasonSameEntity},
	}
	for _, c := range cases {
		row, ok := byID[c.fact.ID]
		if !ok {
			t.Fatalf("row %s missing", c.fact.ID)
		}
		if row.Temperature != c.wantTemp || !hasReason(row, c.want) {
			t.Fatalf("%s: temperature=%s reasons=%v, want %s with %s", c.fact.ID, row.Temperature, row.Reasons, c.wantTemp, c.want)
		}
	}
	if row := byID[f.unrelated.ID]; row.Temperature != FocusCold || len(row.Reasons) != 0 {
		t.Fatalf("unrelated row = %s %v, want cold with no reason", row.Temperature, row.Reasons)
	}
	// Ordering is stable: hot first, then warm, then cold, id ascending inside a
	// temperature. A stable order is what makes the cursor chain safe.
	rank := map[string]int{FocusHot: 0, FocusWarm: 1, FocusCold: 2}
	for i := 1; i < len(rows); i++ {
		prev, cur := rows[i-1], rows[i]
		if rank[prev.Temperature] > rank[cur.Temperature] ||
			(rank[prev.Temperature] == rank[cur.Temperature] && prev.ID > cur.ID) {
			t.Fatalf("unstable order at %d: %s(%s) then %s(%s)", i, prev.ID, prev.Temperature, cur.ID, cur.Temperature)
		}
	}
}

func TestUnrelatedNewFactsDoNotEvictOldHotPrerequisite(t *testing.T) {
	f := focusFixtures(t)
	for i := 0; i < 200; i++ {
		if _, err := f.s.AddFact(model.Fact{Kind: "note", Title: fmt.Sprintf("noise %03d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	db, _ := f.s.ReadView()
	rows, err := FocusItems(db, f.task.ID, focusVersionsOf(t, f.s))
	if err != nil {
		t.Fatal(err)
	}
	hot := 0
	foundRequired := false
	for _, row := range rows {
		if row.Temperature == FocusHot {
			hot++
		}
		if row.ID == f.required.ID {
			foundRequired = row.Temperature == FocusHot && hasReason(row, FocusReasonRequired)
		}
	}
	if !foundRequired {
		t.Fatal("old required prerequisite was evicted or demoted by unrelated new facts")
	}
	if hot != 3 {
		t.Fatalf("hot count = %d, want 3 (required/pinned/reference)", hot)
	}
	// Nothing was deleted and nothing was capped: the projection still contains
	// every fact (6 fixture facts + 200 noise rows).
	if len(rows) != 206 {
		t.Fatalf("projection length = %d, want all 206 facts", len(rows))
	}
}

func TestColdLayerKeepsOldFactsQueryable(t *testing.T) {
	f := focusFixtures(t)
	old, err := f.s.AddFact(model.Fact{Kind: "note", Title: "old cold clue", Doc: "historical"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 60; i++ {
		if _, err := f.s.AddFact(model.Fact{Kind: "note", Title: fmt.Sprintf("later %02d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := Focus(f.s, FocusQuery{Intent: f.task.ID, Layer: FocusCold, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if first.Hot != 3 {
		t.Fatalf("hot count on a cold page = %d, want 3", first.Hot)
	}
	if first.Total != first.Cold {
		t.Fatalf("cold total = %d, cold count = %d", first.Total, first.Cold)
	}
	// Walk the cold layer to the end: old facts are always reachable.
	seen := map[string]bool{}
	q := FocusQuery{Intent: f.task.ID, Layer: FocusCold, Limit: 10}
	for {
		page, err := Focus(f.s, q)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range page.Facts {
			if row.Temperature != FocusCold {
				t.Fatalf("cold layer leaked a %s row", row.Temperature)
			}
			seen[row.ID] = true
		}
		if !page.HasMore {
			break
		}
		q.Cursor = page.NextCursor
	}
	if !seen[old.ID] {
		t.Fatal("old cold fact disappeared from the cold layer")
	}
	if len(seen) != first.Total {
		t.Fatalf("walked %d cold rows, want %d", len(seen), first.Total)
	}
}

func TestFocusMissingAndRejectedPremisesAreExplicit(t *testing.T) {
	s := ledger(t)
	t.Cleanup(func() { _ = s.Close() })
	ok, _ := s.AddFact(model.Fact{Kind: "note", Title: "recorded"})
	rejected, _ := s.AddFact(model.Fact{Kind: "note", Title: "rejected"})
	if _, err := s.ReviewFact(model.Review{FactID: rejected.ID, Verdict: "reject", Notes: "fixture"}); err != nil {
		t.Fatal(err)
	}
	task, err := s.CreateIntent(model.Intent{
		Title:       "missing premises",
		FromFactIDs: []string{ok.ID, "f_missing", rejected.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	db, version := s.ReadView()
	versions, err := s.FactVersionsAt(version)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := FocusItems(db, task.ID, versions)
	if err != nil {
		t.Fatal(err)
	}
	byID := focusRowsByID(rows)
	if row := byID["f_missing"]; row.Status != "missing" || row.RecordVersion != nil || row.Temperature != FocusHot {
		t.Fatalf("missing premise row = %+v", row)
	}
	if row := byID[rejected.ID]; row.Status != "rejected" || row.RecordVersion == nil || row.Temperature != FocusHot {
		t.Fatalf("rejected premise row = %+v", row)
	}
	if row := byID[ok.ID]; row.Status == "missing" || row.RecordVersion == nil {
		t.Fatalf("recorded premise row = %+v", row)
	}
	// The recorded version must be the snapshot's, not a fabricated zero.
	if want := versions[ok.ID]; byID[ok.ID].RecordVersion == nil || *byID[ok.ID].RecordVersion != want {
		t.Fatalf("record version = %v, want %d", byID[ok.ID].RecordVersion, want)
	}
}

func TestFocusPagesStayFrozenWhileFactsChange(t *testing.T) {
	s := ledger(t)
	t.Cleanup(func() { _ = s.Close() })
	task, _ := s.CreateIntent(model.Intent{Title: "frozen"})
	producer, _ := s.CreateIntent(model.Intent{Title: "frozen producer"})
	ids := []string{}
	for i := 0; i < 40; i++ {
		f, err := s.AddFact(model.Fact{IntentID: producer.ID, Kind: "note", Title: fmt.Sprintf("fact %02d", i), Doc: "original"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, f.ID)
	}
	if _, err := s.UpdateIntent(task.ID, func(in *model.Intent) { in.FromFactIDs = []string{ids[0]} }); err != nil {
		t.Fatal(err)
	}
	collect := func() []FocusFact {
		q := FocusQuery{Intent: task.ID, Layer: FocusCold, Limit: 5}
		out := []FocusFact{}
		for {
			page, err := Focus(s, q)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, page.Facts...)
			if !page.HasMore {
				return out
			}
			q.Cursor = page.NextCursor
		}
	}
	baseline := collect()
	if len(baseline) != 39 {
		t.Fatalf("baseline rows = %d, want 39 cold rows", len(baseline))
	}
	q := FocusQuery{Intent: task.ID, Layer: FocusCold, Limit: 5}
	first, err := Focus(s, q)
	if err != nil {
		t.Fatal(err)
	}
	got := append([]FocusFact{}, first.Facts...)
	// Mutate the same snapshot's records after the first page was cut.
	if _, err := s.UpdateFact(ids[3], func(f *model.Fact) { f.Doc = "changed" }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddFact(model.Fact{IntentID: task.ID, Kind: "note", Title: "after the cursor"}); err != nil {
		t.Fatal(err)
	}
	for first.HasMore {
		q.Cursor = first.NextCursor
		first, err = Focus(s, q)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, first.Facts...)
	}
	if !reflect.DeepEqual(got, baseline) {
		t.Fatal("pages moved under concurrent writes: snapshot was not frozen")
	}
	for _, row := range got {
		if row.RecordVersion == nil {
			t.Fatalf("row %s lost its record version", row.ID)
		}
	}
}

func TestFocusPagesAreByteBoundedAndProgress(t *testing.T) {
	s := ledger(t)
	t.Cleanup(func() { _ = s.Close() })
	task, _ := s.CreateIntent(model.Intent{Title: "bytes"})
	longText := ""
	for i := 0; i < 200; i++ {
		longText += "长"
	}
	for i := 0; i < 40; i++ {
		if _, err := s.AddFact(model.Fact{IntentID: task.ID, Kind: "note", Title: longText, Summary: longText}); err != nil {
			t.Fatal(err)
		}
	}
	q := FocusQuery{Intent: task.ID, Layer: FocusCold, Limit: 50}
	pages := 0
	for {
		page, err := Focus(s, q)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		if len(page.Facts) == 0 {
			t.Fatal("a page returned no rows before the end")
		}
		used := 0
		for _, row := range page.Facts {
			used += len(mustJSON(t, row))
		}
		if used > FocusPageBytes && len(page.Facts) > 1 {
			t.Fatalf("page %d is %d bytes over the %d budget", pages, used, FocusPageBytes)
		}
		if !page.HasMore {
			break
		}
		q.Cursor = page.NextCursor
	}
	if pages < 2 {
		t.Fatalf("byte budget was not exercised: %d page(s)", pages)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
