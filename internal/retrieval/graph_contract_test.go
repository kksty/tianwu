package retrieval

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// graphLedger is the shared fixture constructor: a real SQLite ledger in a temp
// dir, closed with the test. The tests below only use recorded ledger fields.
func graphLedger(t *testing.T) *store.Store {
	t.Helper()
	s := ledger(t)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func setIntentCreatedAt(t *testing.T, s *store.Store, id string, at time.Time, edit func(*model.Intent)) model.Intent {
	t.Helper()
	in, err := s.UpdateIntent(id, func(current *model.Intent) {
		current.CreatedAt = at
		if edit != nil {
			edit(current)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	return in
}

type graphNodeKind struct {
	kind string
	id   string
}

func graphPageNodes(page GraphPage) []graphNodeKind {
	out := []graphNodeKind{}
	for _, in := range page.Intents {
		out = append(out, graphNodeKind{"intent", in.ID})
	}
	for _, g := range page.Goals {
		out = append(out, graphNodeKind{"goal", g.ID})
	}
	for _, f := range page.Facts {
		out = append(out, graphNodeKind{"fact", f.ID})
	}
	return out
}

func graphPageMeta(page GraphPage) map[string]FactMeta {
	out := make(map[string]FactMeta, len(page.FactMeta))
	for _, m := range page.FactMeta {
		out[m.ID] = m
	}
	return out
}

// structural fixture: P (depth 1) produces f1 (depth 2); child C (depth 3, via
// premise f1 and parent P) produces f2 (depth 4); f3 has no producer (depth 1).
type structuralFixture struct {
	producer, child model.Intent
	premise, output factRef
	orphan          factRef
}

// factRef is a tiny wrapper so the fixture reads by role, not raw struct.
type factRef struct {
	ID string
}

func buildStructuralFixture(t *testing.T, s *store.Store) structuralFixture {
	t.Helper()
	at := func(seconds int) time.Time {
		return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(seconds) * time.Second)
	}
	producer, err := s.CreateIntent(model.Intent{Title: "producer"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.CreateIntent(model.Intent{Title: "child", ParentID: producer.ID})
	if err != nil {
		t.Fatal(err)
	}
	premiseFact, err := s.AddFact(model.Fact{IntentID: producer.ID, Kind: "note", Title: "premise", CreatedAt: at(20)})
	if err != nil {
		t.Fatal(err)
	}
	outputFact, err := s.AddFact(model.Fact{IntentID: child.ID, Kind: "note", Title: "output", CreatedAt: at(30)})
	if err != nil {
		t.Fatal(err)
	}
	orphan, err := s.AddFact(model.Fact{IntentID: "", Kind: "note", Title: "orphan", CreatedAt: at(0)})
	if err != nil {
		t.Fatal(err)
	}
	// Two passes mirror the two recorded fields: the premise and parent depth
	// are both ledger state (model.ts computes max of both).
	producer = setIntentCreatedAt(t, s, producer.ID, at(0), nil)
	child = setIntentCreatedAt(t, s, child.ID, at(10), func(in *model.Intent) {
		in.FromFactIDs = []string{premiseFact.ID}
	})
	return structuralFixture{
		producer: producer, child: child,
		premise: factRef{premiseFact.ID}, output: factRef{outputFact.ID}, orphan: factRef{orphan.ID},
	}
}

// Structural order pages by chain depth, not by node type: a premise fact sits
// one depth above the intent citing it and a produced fact one depth below its
// producer, so any page prefix is a legal subgraph — an intent never appears
// before (or without) its premise facts. The order must be a total order inside
// one snapshot, cursor-stable, and the default stays id-lexicographic.
func TestGraphStructuralOrderPagesByChainDepthDeterministically(t *testing.T) {
	s := graphLedger(t)
	fx := buildStructuralFixture(t, s)

	// Expected walk: f3 (depth 1, no producer), P (depth 1 intent), f1 (depth 2
	// premise), C (depth 3 intent, premise f1 and parent P), f2 (depth 4 output).
	want := []graphNodeKind{
		{"fact", fx.orphan.ID}, {"intent", fx.producer.ID},
		{"fact", fx.premise.ID}, {"intent", fx.child.ID},
		{"fact", fx.output.ID},
	}

	walk := func() []graphNodeKind {
		q := GraphQuery{Order: GraphOrderStructural, Limit: 1, MaxBytes: 256 << 10}
		seen := []graphNodeKind{}
		for pages := 0; ; pages++ {
			if pages > 20 {
				t.Fatal("cursor walk did not terminate")
			}
			page, err := Graph(s, q)
			if err != nil {
				t.Fatal(err)
			}
			nodes := graphPageNodes(page)
			if len(nodes) > 1 {
				t.Fatalf("limit 1 page returned %d nodes", len(nodes))
			}
			seen = append(seen, nodes...)
			if !page.HasMore {
				return seen
			}
			q.Cursor = page.NextCursor
		}
	}
	first := walk()
	second := walk()
	if !equalNodes(first, second) {
		t.Fatalf("structural order is not deterministic:\n a=%v\n b=%v", first, second)
	}
	if !equalNodes(first, want) {
		t.Fatalf("cursor walk = %v, want %v (depth asc, same-depth fact/intent/goal, created_at desc, id)", first, want)
	}

	// The page-1 acceptance property: the first page can carry a depth-1 intent
	// together with the depth-2 fact it produced, so buildGraph has no missing
	// premise to draw as a boundary "expand" node. (GraphPage exposes typed
	// slices, so compare contents per type; the cross-type order is the walk
	// asserted above.)
	prefix, err := Graph(s, GraphQuery{Order: GraphOrderStructural, Limit: 3, MaxBytes: 256 << 10})
	if err != nil {
		t.Fatal(err)
	}
	if got, wantFacts := factIDs(prefix.Facts), []string{fx.orphan.ID, fx.premise.ID}; !equalStrings(got, wantFacts) {
		t.Fatalf("first page facts = %v, want the depth-1 operand and depth-2 premise %v", got, wantFacts)
	}
	if got, wantIntents := intentIDs(prefix.Intents), []string{fx.producer.ID}; !equalStrings(got, wantIntents) {
		t.Fatalf("first page intents = %v, want the depth-1 producer %v", got, wantIntents)
	}
	if len(prefix.Facts)+len(prefix.Intents)+len(prefix.Goals) != 3 {
		t.Fatalf("limit 3 first page returned %d nodes", len(prefix.Facts)+len(prefix.Intents)+len(prefix.Goals))
	}
	if !equalNodes(first[:3], want[:3]) {
		t.Fatalf("first walk prefix = %v, want %v", first[:3], want[:3])
	}

	// Total order: a premise precedes its intent and a produced fact follows its
	// producer, so a cursor prefix is always a legal subgraph.
	pos := map[string]int{}
	for i, node := range first {
		pos[node.kind+":"+node.id] = i
	}
	if pos["fact:"+fx.premise.ID] >= pos["intent:"+fx.child.ID] {
		t.Fatalf("premise fact did not precede the intent citing it: %v", first)
	}
	if pos["intent:"+fx.producer.ID] >= pos["fact:"+fx.premise.ID] {
		t.Fatalf("producer intent did not precede its produced fact: %v", first)
	}
	if pos["intent:"+fx.child.ID] >= pos["fact:"+fx.output.ID] {
		t.Fatalf("child intent did not precede its produced fact: %v", first)
	}

	// One full page keeps the same order inside each typed slice.
	one, err := Graph(s, GraphQuery{Order: GraphOrderStructural, Limit: 100, MaxBytes: 256 << 10})
	if err != nil {
		t.Fatal(err)
	}
	if got, wantFacts := factIDs(one.Facts), []string{fx.orphan.ID, fx.premise.ID, fx.output.ID}; !equalStrings(got, wantFacts) {
		t.Fatalf("structural facts = %v, want %v", got, wantFacts)
	}
	if got, wantIntents := intentIDs(one.Intents), []string{fx.producer.ID, fx.child.ID}; !equalStrings(got, wantIntents) {
		t.Fatalf("structural intents = %v, want %v", got, wantIntents)
	}

	// Default (no order) stays the historical id-lexicographic order: the agent
	// contract must not change.
	def, err := Graph(s, GraphQuery{Limit: 100, MaxBytes: 256 << 10})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(def.Facts); i++ {
		if def.Facts[i-1].ID > def.Facts[i].ID {
			t.Fatalf("default fact order is not id-lexicographic: %v", factIDs(def.Facts))
		}
	}
	if len(def.Facts) != 3 || len(def.Intents) != 2 {
		t.Fatalf("default page = %d facts / %d intents, want the whole tiny fixture", len(def.Facts), len(def.Intents))
	}
}

func TestGraphRecentOrderIsCreatedAtDescendingWithUndatedLast(t *testing.T) {
	s := graphLedger(t)
	base := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	older, _ := s.AddFact(model.Fact{Kind: "note", Title: "older", CreatedAt: base.Add(1 * time.Second)})
	newer, _ := s.AddFact(model.Fact{Kind: "note", Title: "newer", CreatedAt: base.Add(3 * time.Second)})
	middle, _ := s.AddFact(model.Fact{Kind: "note", Title: "middle", CreatedAt: base.Add(2 * time.Second)})
	undated, _ := s.AddFact(model.Fact{Kind: "note", Title: "undated", CreatedAt: base})
	if _, err := s.UpdateFact(undated.ID, func(f *model.Fact) { f.CreatedAt = time.Time{} }); err != nil {
		t.Fatal(err)
	}
	page, err := Graph(s, GraphQuery{Order: GraphOrderRecent, Limit: 100, MaxBytes: 256 << 10})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{newer.ID, middle.ID, older.ID, undated.ID}
	if got := factIDs(page.Facts); !equalStrings(got, want) {
		t.Fatalf("recent order = %v, want %v", got, want)
	}
}

// pin and reference are distinct recorded fields and must both survive edge
// assembly, including when the same fact is recorded in both lists.
func TestGraphPinAndReferenceEdgesCoexist(t *testing.T) {
	s := graphLedger(t)
	producer, _ := s.CreateIntent(model.Intent{Title: "producer"})
	pinned, _ := s.AddFact(model.Fact{IntentID: producer.ID, Kind: "note", Title: "pinned"})
	referenced, _ := s.AddFact(model.Fact{IntentID: producer.ID, Kind: "note", Title: "referenced"})
	both, _ := s.AddFact(model.Fact{IntentID: producer.ID, Kind: "note", Title: "both"})
	in, err := s.CreateIntent(model.Intent{
		Title:          "selects",
		PinnedFactIDs:  []string{pinned.ID, both.ID},
		ContextFactIDs: []string{referenced.ID, both.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err := Graph(s, GraphQuery{Limit: 100, MaxBytes: 256 << 10})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string][]string{}
	for _, edge := range page.Edges {
		kinds[edge.Source+"\x00"+edge.Target] = append(kinds[edge.Source+"\x00"+edge.Target], edge.Kind)
	}
	edgeKinds := func(factID string) []string {
		found := kinds["fact:"+factID+"\x00intent:"+in.ID]
		out := make([]string, len(found))
		copy(out, found)
		return out
	}
	if got := edgeKinds(pinned.ID); !containsString(got, "pin") {
		t.Fatalf("pinned fact has no pin edge: %v", got)
	}
	if got := edgeKinds(referenced.ID); !containsString(got, "reference") {
		t.Fatalf("context fact has no reference edge: %v", got)
	}
	if got := edgeKinds(both.ID); !containsString(got, "pin") || !containsString(got, "reference") {
		t.Fatalf("fact recorded as both pin and reference lost one edge: %v", got)
	}
	// Both endpoint facts are part of the full snapshot, so both are reachable
	// in the page — a pin is not swallowed by the reference stream.
	shown := map[string]bool{}
	for _, f := range page.Facts {
		shown[f.ID] = true
	}
	for _, id := range []string{pinned.ID, referenced.ID, both.ID} {
		if !shown[id] {
			t.Fatalf("fact %s missing from graph page", id)
		}
	}
}

// The graph projection removes the body before the byte budget is measured and
// reports the counts in fact_meta, without claiming a `details` truncation.
func TestGraphProjectionTrimsFactsBeforeBudgetAndKeepsCounts(t *testing.T) {
	s := graphLedger(t)
	producer, _ := s.CreateIntent(model.Intent{Title: "producer"})
	combined := strings.Repeat("长", 30000) // ~90KB of doc, over the per-node budget
	big, err := s.AddFact(model.Fact{
		IntentID:    producer.ID,
		Kind:        "vuln",
		Title:       "big finding",
		Summary:     "short summary",
		Doc:         combined,
		Proof:       "dynamic",
		Confidence:  "observed",
		EntityRef:   "host:web",
		EvidenceIDs: []string{"ev_1", "ev_2"},
		Refs:        []model.Ref{{Scheme: "url", Locator: "https://example.test/a"}, {Scheme: "file", Locator: "/tmp/a"}, {Scheme: "artifact", Locator: "ax_1"}},
		Tags:        []string{"product:demo", "technique:rce"},
	})
	if err != nil {
		t.Fatal(err)
	}
	small, err := s.AddFact(model.Fact{IntentID: producer.ID, Kind: "note", Title: "small", Doc: "body"})
	if err != nil {
		t.Fatal(err)
	}
	// Full mode must have exercised a body truncation: the fixture is valid.
	full, err := Graph(s, GraphQuery{Limit: 100, MaxBytes: 16384})
	if err != nil {
		t.Fatal(err)
	}
	fullSmall := model.Fact{}
	for _, f := range full.Facts {
		if f.ID == small.ID {
			fullSmall = f
		}
	}
	if fullSmall.Doc != "body" {
		t.Fatalf("full mode must keep a small fact body: %+v", fullSmall)
	}
	hadBodyDetail := false
	for _, d := range full.Details {
		for _, omitted := range d.Omitted {
			if d.ID == "fact:"+big.ID && omitted == "doc" {
				hadBodyDetail = true
			}
		}
	}
	if !hadBodyDetail {
		t.Fatalf("fixture did not exceed the full-mode body budget: %+v", full.Details)
	}

	page, err := Graph(s, GraphQuery{Projection: GraphProjectionGraph, Limit: 100, MaxBytes: 16384})
	if err != nil {
		t.Fatal(err)
	}
	var got *model.Fact
	for i := range page.Facts {
		if page.Facts[i].ID == big.ID {
			got = &page.Facts[i]
		}
	}
	if got == nil {
		t.Fatal("projected fact missing from page")
	}
	if got.Doc != "" || got.EvidenceIDs != nil || got.Refs != nil || got.Tags != nil {
		t.Fatalf("projection kept body fields: %+v", got)
	}
	if got.Title != "big finding" || got.ClaimVersion == 0 || got.Status != "unverified" || got.Proof != "dynamic" || got.EntityRef != "host:web" || got.IntentID != producer.ID || got.CreatedAt.IsZero() {
		t.Fatalf("projection dropped a kept field: %+v", got)
	}
	// "Not in this projection" is a contract, not a data gap: no details entry.
	for _, d := range page.Details {
		if d.ID == "fact:"+big.ID {
			t.Fatalf("projection wrote a details omission for the graph body trim: %+v", d)
		}
	}
	meta := graphPageMeta(page)[big.ID]
	if meta.EvidenceCount != 2 || meta.RefCount != 3 {
		t.Fatalf("projected fact_meta counts = %d/%d, want 2/3", meta.EvidenceCount, meta.RefCount)
	}
	if len(page.FactMeta) != len(page.Facts) {
		t.Fatalf("fact_meta covers %d rows for %d page facts", len(page.FactMeta), len(page.Facts))
	}
}

// Under the same byte budget the projection must fit strictly more facts than
// the full body page: that is the point of trimming before measuring bytes.
func TestGraphProjectionBuysMoreFactsPerByteBudget(t *testing.T) {
	s := graphLedger(t)
	producer, _ := s.CreateIntent(model.Intent{Title: "producer"})
	for i := 0; i < 10; i++ {
		if _, err := s.AddFact(model.Fact{
			IntentID: producer.ID, Kind: "note",
			Title:   fmt.Sprintf("fact %02d", i),
			Summary: "summary",
			Doc:     strings.Repeat("d", 1000),
		}); err != nil {
			t.Fatal(err)
		}
	}
	const budget = 8192
	full, err := Graph(s, GraphQuery{Limit: 100, MaxBytes: budget})
	if err != nil {
		t.Fatal(err)
	}
	projected, err := Graph(s, GraphQuery{Projection: GraphProjectionGraph, Limit: 100, MaxBytes: budget})
	if err != nil {
		t.Fatal(err)
	}
	if len(projected.Facts) <= len(full.Facts) {
		t.Fatalf("projection did not buy more facts: full=%d projected=%d", len(full.Facts), len(projected.Facts))
	}
	if len(projected.Facts) != 10 {
		t.Fatalf("projection page only fit %d/10 facts", len(projected.Facts))
	}
}

// needs_review means "the newest review proved an older claim": a reviewed
// current claim is clean, an upgraded claim is stale, a never-reviewed fact
// carries reviewed_claim_version=0.
func TestGraphFactMetaNeedsReviewTracksClaimVersion(t *testing.T) {
	s := graphLedger(t)
	current, _ := s.AddFact(model.Fact{Kind: "note", Title: "reviewed current", EvidenceIDs: []string{"ev_a"}})
	if _, err := s.ReviewFact(model.Review{FactID: current.ID, Verdict: "pass", Notes: "fixture"}); err != nil {
		t.Fatal(err)
	}
	stale, _ := s.AddFact(model.Fact{Kind: "note", Title: "stale review"})
	if _, err := s.ReviewFact(model.Review{FactID: stale.ID, Verdict: "pass", Notes: "fixture"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateFact(stale.ID, func(f *model.Fact) { f.Summary = "rewritten claim" }); err != nil {
		t.Fatal(err)
	}
	never, _ := s.AddFact(model.Fact{Kind: "note", Title: "never reviewed", Refs: []model.Ref{{Scheme: "url", Locator: "https://example.test"}, {Scheme: "file", Locator: "/tmp/x"}}})

	page, err := Graph(s, GraphQuery{Projection: GraphProjectionGraph, Limit: 100, MaxBytes: 256 << 10})
	if err != nil {
		t.Fatal(err)
	}
	meta := graphPageMeta(page)
	if m := meta[current.ID]; m.NeedsReview || m.ReviewedClaimVersion != 1 || m.EvidenceCount != 1 {
		t.Fatalf("current claim meta = %+v, want no review need at v1 with 1 evidence", m)
	}
	if m := meta[stale.ID]; !m.NeedsReview || m.ReviewedClaimVersion != 1 {
		t.Fatalf("stale claim meta = %+v, want needs_review at reviewed v1", m)
	}
	if m := meta[never.ID]; m.NeedsReview || m.ReviewedClaimVersion != 0 || m.RefCount != 2 {
		t.Fatalf("never-reviewed meta = %+v, want reviewed=0/ref_count=2", m)
	}
}

// Totals count the ids selected by this query (the whole graph when roots are
// empty; the reached subgraph when rooted), which is what batch paging uses to
// decide "all intents are loaded".
func TestGraphTotalsCountSelectedNodes(t *testing.T) {
	s := graphLedger(t)
	a, _ := s.CreateIntent(model.Intent{Title: "a"})
	b, _ := s.CreateIntent(model.Intent{Title: "b"})
	premise, _ := s.AddFact(model.Fact{IntentID: a.ID, Kind: "note", Title: "premise"})
	if _, err := s.UpdateIntent(b.ID, func(in *model.Intent) { in.FromFactIDs = []string{premise.ID} }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddFact(model.Fact{IntentID: a.ID, Kind: "note", Title: "other"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddGoal(model.Goal{Statement: "win"}); err != nil {
		t.Fatal(err)
	}
	all, err := Graph(s, GraphQuery{Limit: 100, MaxBytes: 256 << 10})
	if err != nil {
		t.Fatal(err)
	}
	if all.IntentsTotal != 2 || all.FactsTotal != 2 || all.GoalsTotal != 1 {
		t.Fatalf("full totals = %d/%d/%d, want 2/2/1", all.IntentsTotal, all.FactsTotal, all.GoalsTotal)
	}
	if all.IntentsTotal != len(all.Intents) || all.FactsTotal != len(all.Facts) || all.GoalsTotal != len(all.Goals) {
		t.Fatalf("totals disagree with one page that fits everything: %+v", all)
	}
	sub, err := Graph(s, GraphQuery{Roots: []string{"intent:" + b.ID}, Depth: 1, Limit: 100, MaxBytes: 256 << 10})
	if err != nil {
		t.Fatal(err)
	}
	if sub.IntentsTotal != 1 || sub.FactsTotal != 1 || sub.GoalsTotal != 0 {
		t.Fatalf("rooted totals = %d/%d/%d, want 1/1/0", sub.IntentsTotal, sub.FactsTotal, sub.GoalsTotal)
	}
}

func intentIDs(intents []model.Intent) []string {
	out := make([]string, len(intents))
	for i, in := range intents {
		out[i] = in.ID
	}
	return out
}

func factIDs(facts []model.Fact) []string {
	out := make([]string, len(facts))
	for i, f := range facts {
		out[i] = f.ID
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalNodes(a, b []graphNodeKind) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsString(list []string, want string) bool {
	for _, got := range list {
		if got == want {
			return true
		}
	}
	return false
}
