package retrieval

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// focusHorizonFixture is a synthetic long-horizon ledger in the shape the
// contract has to survive: one very old required premise, thousands of newer
// unrelated facts spread over many directions and branches, a few explicitly
// pinned/referenced rows, and a deep target direction that lists only the old
// premise as its hard prerequisite.
//
// Everything here is recorded structure (intent fields, fact rows). Nothing is
// inferred from titles or prose: the test asserts the projection reflects the
// ledger, not a heuristic.
func focusHorizonFixture(t testing.TB, nFacts int) (*store.Store, model.Intent, model.Fact, model.Fact) {
	t.Helper()
	if nFacts < 400 {
		nFacts = 400
	}
	at := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	db := store.DB{Version: store.CurrentSchemaVersion()}

	root := model.Intent{
		ID:        "in_root",
		Title:     "root direction",
		Status:    model.StatusDone,
		CreatedAt: at,
	}
	branches := 8
	perBranch := 30
	chainLast := make([]string, branches)
	for b := 0; b < branches; b++ {
		parent := root.ID
		for i := 0; i < perBranch; i++ {
			id := fmt.Sprintf("in_b%02d_%02d", b, i)
			db.Intents = append(db.Intents, model.Intent{
				ID:        id,
				Title:     fmt.Sprintf("branch %d step %d", b, i),
				ParentID:  parent,
				Status:    model.StatusDone,
				CreatedAt: at.Add(time.Duration(1+b*perBranch+i) * time.Second),
			})
			parent = id
		}
		chainLast[b] = parent
	}

	facts := make([]model.Fact, 0, nFacts)
	// The old key fact: a verified credential, produced by the root direction.
	early := model.Fact{
		ID:           "f_early",
		IntentID:     root.ID,
		Kind:         "cred",
		Title:        "early admin credential",
		Summary:      "found in the first hour and never repeated",
		Doc:          "original proof bytes",
		Status:       "verified",
		ClaimVersion: 1,
		CreatedAt:    at,
	}
	facts = append(facts, early)
	// A pinned row and an explicit reference: both are hot by recorded choice,
	// not by age.
	pinned := model.Fact{
		ID: "f_pinned", IntentID: chainLast[0], Kind: "service",
		Title: "pinned service", EntityRef: "host:web", Status: "unverified",
		ClaimVersion: 1, CreatedAt: at.Add(time.Second),
	}
	referenced := model.Fact{
		ID: "f_referenced", IntentID: chainLast[1], Kind: "host",
		Title: "explicit reference", Status: "unverified",
		ClaimVersion: 1, CreatedAt: at.Add(2 * time.Second),
	}
	// Provenance row: root derived from it, and the target derives from the
	// root's old fact, so the recorded source chain has to reach it.
	provenance := model.Fact{
		ID: "f_provenance", IntentID: root.ID, Kind: "note",
		Title: "root provenance", Status: "unverified",
		ClaimVersion: 1, CreatedAt: at.Add(3 * time.Second),
	}
	facts = append(facts, pinned, referenced, provenance)
	root.FromFactIDs = []string{provenance.ID}
	db.Intents = append(db.Intents, root)

	// Same recorded entity as the pinned fact, but on unrelated directions: the
	// warm layer must recall all of them without dragging in the whole ledger.
	const sameEntity = 100
	for i := 0; i < sameEntity && len(facts) < nFacts; i++ {
		b := i % branches
		facts = append(facts, model.Fact{
			ID:        fmt.Sprintf("f_entity_%03d", i),
			IntentID:  fmt.Sprintf("in_b%02d_%02d", b, i%perBranch),
			Kind:      "host",
			Title:     fmt.Sprintf("same recorded host %03d", i),
			EntityRef: "host:web",
			Status:    "unverified",
			CreatedAt: at.Add(time.Duration(10+i) * time.Second),
		})
	}
	// The distractors: newer observations, one per later second, distributed
	// across every branch. They are cold for the target direction.
	for len(facts) < nFacts {
		i := len(facts)
		b := i % branches
		facts = append(facts, model.Fact{
			ID:           fmt.Sprintf("f_%06d", i),
			IntentID:     fmt.Sprintf("in_b%02d_%02d", b, i%perBranch),
			Kind:         "note",
			Title:        fmt.Sprintf("later unrelated observation %06d", i),
			Summary:      "newer information on a different branch",
			Status:       "unverified",
			ClaimVersion: 1,
			CreatedAt:    at.Add(time.Duration(1+i) * time.Minute),
		})
	}
	db.Facts = facts

	target := model.Intent{
		ID:             "in_target_deep",
		Title:          "deep target direction",
		ParentID:       chainLast[branches-1],
		Status:         model.StatusPending,
		CreatedAt:      at.Add(time.Duration(nFacts+1000) * time.Minute),
		FromFactIDs:    []string{early.ID},
		PinnedFactIDs:  []string{pinned.ID},
		ContextFactIDs: []string{referenced.ID},
	}
	db.Intents = append(db.Intents, target)

	s, err := scaleSeed(t.TempDir()+"/ledger.sqlite", db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, target, early, pinned
}

func focusWalk(t *testing.T, s *store.Store, q FocusQuery, limit int) []FocusFact {
	t.Helper()
	q.Limit = limit
	rows := []FocusFact{}
	first := true
	var snapshot store.ReadVersion
	for pages := 0; ; pages++ {
		if pages > 10000 {
			t.Fatal("focus page chain did not terminate")
		}
		page, err := Focus(s, q)
		if err != nil {
			t.Fatal(err)
		}
		if first {
			snapshot = page.Version
			first = false
		} else if page.Version != snapshot {
			t.Fatalf("page %d snapshot moved: %+v, want %+v", pages+1, page.Version, snapshot)
		}
		if page.HasMore && page.NextCursor == "" {
			t.Fatalf("page %d has_more without a continuation", pages+1)
		}
		if !page.HasMore && page.NextCursor != "" {
			t.Fatalf("last page %d still advertises a cursor", pages+1)
		}
		rows = append(rows, page.Facts...)
		if !page.HasMore {
			return rows
		}
		q.Cursor = page.NextCursor
	}
}

// The contract named in docs/32 stage 3: after thousands of newer facts on many
// branches, the old required premise is still classified hot, still enumerable
// through the frozen pagination chain, and still carries its reason, snapshot
// version and original link.
func TestOldRequiredPremiseSurvivesThousandsOfNewerFacts(t *testing.T) {
	s, target, early, _ := focusHorizonFixture(t, 4000)

	focusRows := focusWalk(t, s, FocusQuery{Intent: target.ID}, 2)
	if len(focusRows) < 2 {
		t.Fatalf("focus layer rows = %d, want hot+warm rows", len(focusRows))
	}
	var got *FocusFact
	for i := range focusRows {
		row := &focusRows[i]
		if row.Temperature != FocusHot && row.Temperature != FocusWarm {
			t.Fatalf("focus layer leaked %s row %s", row.Temperature, row.ID)
		}
		if row.ID == early.ID {
			got = row
		}
	}
	if got == nil {
		t.Fatal("old required premise was evicted from the focus layer")
	}
	if got.Temperature != FocusHot || !hasReason(*got, FocusReasonRequired) {
		t.Fatalf("old premise row = %+v, want hot/required", *got)
	}
	if got.Status != "verified" || got.RecordVersion == nil {
		t.Fatalf("old premise lost its recorded state: %+v", *got)
	}
	if got.Query != "/api/facts/"+early.ID {
		t.Fatalf("old premise has no original link: %q", got.Query)
	}

	// The whole heat ledger is still there: the projection never drops a fact.
	db, snapshot := s.ReadView()
	versions, err := s.FactVersionsAt(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	all, err := FocusItems(db, target.ID, versions)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != len(db.Facts) {
		t.Fatalf("projection length = %d, want every fact %d", len(all), len(db.Facts))
	}

	// The cold continuation reaches the oldest distractor too; no age deletion.
	coldRows := focusWalk(t, s, FocusQuery{Intent: target.ID, Layer: FocusCold}, 400)
	// The first four rows are the named hot/provenance facts; the same-entity
	// block starts at f_entity_000 and the age-distributed distractors start at
	// f_000104, so f_000104 is the oldest ordinary observation.
	oldestDistractor := "f_000104"
	foundOld := false
	for _, row := range coldRows {
		if row.Temperature != FocusCold {
			t.Fatalf("cold layer leaked %s row %s", row.Temperature, row.ID)
		}
		if row.ID == oldestDistractor {
			foundOld = true
		}
	}
	if !foundOld {
		t.Fatalf("oldest cold distractor %s missing from %d cold rows", oldestDistractor, len(coldRows))
	}
	// 3 hot (required/pinned/reference) and 101 warm (provenance + 100
	// same-recorded-entity rows).
	if len(coldRows) != len(all)-104 {
		t.Fatalf("cold rows = %d, want %d", len(coldRows), len(all)-104)
	}
}

// The projection must be readable from the ledger alone: a fresh model session
// or a different physical session gets the same stable ids, snapshot and reason
// for the same frozen ledger. Nothing here comes from session history.
func TestFocusProjectionIsLedgerDerivedNotSessionDerived(t *testing.T) {
	s, target, early, _ := focusHorizonFixture(t, 1200)
	_, v := s.ReadView()
	versions, err := s.FactVersionsAt(v)
	if err != nil {
		t.Fatal(err)
	}
	db, _ := s.ReadView()
	first, err := FocusItems(db, target.ID, versions)
	if err != nil {
		t.Fatal(err)
	}
	// A second reader with a fresh version map (this is what a different
	// session process would recompute) produces byte-identical rows.
	db2, v2 := s.ReadView()
	versions2, err := s.FactVersionsAt(v2)
	if err != nil {
		t.Fatal(err)
	}
	second, err := FocusItems(db2, target.ID, versions2)
	if err != nil {
		t.Fatal(err)
	}
	if string(mustJSON(t, first)) != string(mustJSON(t, second)) {
		t.Fatal("focus projection depended on reader state, not on the frozen ledger")
	}
	if v != v2 {
		t.Fatalf("snapshot moved without a write: %+v != %+v", v, v2)
	}
	// Committing unrelated new facts must not relabel or move the old premise.
	if _, err := s.AddFact(model.Fact{Kind: "note", Title: "post-compaction noise"}); err != nil {
		t.Fatal(err)
	}
	db3, v3 := s.ReadView()
	versions3, err := s.FactVersionsAt(v3)
	if err != nil {
		t.Fatal(err)
	}
	third, err := FocusItems(db3, target.ID, versions3)
	if err != nil {
		t.Fatal(err)
	}
	var after *FocusFact
	for i := range third {
		if third[i].ID == early.ID {
			after = &third[i]
		}
	}
	if after == nil || after.Temperature != FocusHot || !hasReason(*after, FocusReasonRequired) {
		t.Fatalf("old premise changed after unrelated write: %+v", after)
	}
	if v3 == v {
		t.Fatal("write did not advance the snapshot version")
	}
}

// A referenced row that is missing from the snapshot is not the same as a hot
// row that exists: missing keeps a hot reason (the task needs it), no record
// version, and an explicit "missing" state. A provenance-only missing id is
// warm, not hot — provenance is a recorded source, not a hard dependency.
func TestMissingProvenanceIsWarmAndMissingHotPremiseIsExplicit(t *testing.T) {
	producer := model.Fact{ID: "f_producer", IntentID: "in_producer", Kind: "vuln", Title: "producer", Status: "unverified"}
	db := store.DB{
		Facts: []model.Fact{producer},
		Intents: []model.Intent{
			{ID: "in_producer", FromFactIDs: []string{"f_gone"}},
			{ID: "in_target", FromFactIDs: []string{producer.ID}, ContextFactIDs: []string{"f_missing"}},
		},
	}
	rows, err := FocusItems(db, "in_target", map[string]uint64{"f_producer": 7})
	if err != nil {
		t.Fatal(err)
	}
	byID := focusRowsByID(rows)
	if row := byID["f_producer"]; row.Temperature != FocusHot || !hasReason(row, FocusReasonRequired) || row.RecordVersion == nil {
		t.Fatalf("recorded premise row = %+v", row)
	}
	gone, ok := byID["f_gone"]
	if !ok {
		t.Fatal("missing provenance row was silently dropped")
	}
	if gone.Temperature != FocusWarm || !hasReason(gone, FocusReasonProvenance) {
		t.Fatalf("missing provenance row = %+v, want warm/provenance", gone)
	}
	if gone.RecordVersion != nil {
		t.Fatalf("missing provenance fabricated a record version: %+v", gone)
	}
	missing, ok := byID["f_missing"]
	if !ok {
		t.Fatal("missing hot premise row was silently dropped")
	}
	if missing.Temperature != FocusHot || !hasReason(missing, FocusReasonReference) || missing.RecordVersion != nil || missing.Status != "missing" {
		t.Fatalf("missing hot premise row = %+v", missing)
	}
}

// Heat is task reference, not trust: a rejected fact that the task still lists
// as a required premise stays hot and stays visibly rejected. The page carries
// the contract text so a reader cannot mistake heat for verification.
func TestHeatIsNotTrustAndStatesStayDistinct(t *testing.T) {
	s := ledger(t)
	t.Cleanup(func() { _ = s.Close() })
	producer, err := s.CreateIntent(model.Intent{Title: "state producer"})
	if err != nil {
		t.Fatal(err)
	}
	verified, err := s.AddFact(model.Fact{IntentID: producer.ID, Kind: "note", Title: "verified"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviewFact(model.Review{FactID: verified.ID, Verdict: "pass", Notes: "fixture"}); err != nil {
		t.Fatal(err)
	}
	rejected, err := s.AddFact(model.Fact{IntentID: producer.ID, Kind: "note", Title: "rejected"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviewFact(model.Review{FactID: rejected.ID, Verdict: "reject", Notes: "fixture"}); err != nil {
		t.Fatal(err)
	}
	unverified, err := s.AddFact(model.Fact{IntentID: producer.ID, Kind: "note", Title: "unverified"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.CreateIntent(model.Intent{
		Title:       "states",
		FromFactIDs: []string{verified.ID, rejected.ID, unverified.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err := Focus(s, FocusQuery{Intent: task.ID, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	byID := focusRowsByID(page.Facts)
	for _, c := range []struct{ id, status string }{
		{verified.ID, "verified"},
		{rejected.ID, "rejected"},
		{unverified.ID, "unverified"},
	} {
		row, ok := byID[c.id]
		if !ok {
			t.Fatalf("row %s missing", c.id)
		}
		if row.Temperature != FocusHot {
			t.Fatalf("required row %s temperature = %s, want hot", c.id, row.Temperature)
		}
		// Same temperature, different recorded trust: heat did not rewrite the
		// fact's state.
		if row.Status != c.status {
			t.Fatalf("row %s status = %q, want %q", c.id, row.Status, c.status)
		}
	}
	// The contract travels with the page: heat is a reference reason, and a
	// bounded page is not the whole layer.
	if !strings.Contains(page.Contract.Heat, "不代表") || !strings.Contains(page.Contract.Heat, "为真") {
		t.Fatalf("heat contract does not disclaim truth: %q", page.Contract.Heat)
	}
	if !strings.Contains(page.Contract.Status, "missing") || !strings.Contains(page.Contract.Status, "rejected") {
		t.Fatalf("status contract does not distinguish missing/rejected: %q", page.Contract.Status)
	}
}

// No age-based deletion: a fact whose CreatedAt is years behind every other row
// is still classified and enumerable. Nothing in the projection reads age.
func TestNoAgeBasedEviction(t *testing.T) {
	s := ledger(t)
	t.Cleanup(func() { _ = s.Close() })
	task, err := s.CreateIntent(model.Intent{Title: "age"})
	if err != nil {
		t.Fatal(err)
	}
	ancient, err := s.AddFact(model.Fact{
		IntentID: task.ID, Kind: "note", Title: "ancient observation",
		CreatedAt: time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 300; i++ {
		if _, err := s.AddFact(model.Fact{IntentID: task.ID, Kind: "note", Title: fmt.Sprintf("modern %03d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	rows := focusWalk(t, s, FocusQuery{Intent: task.ID, Layer: FocusCold}, 40)
	found := false
	for _, row := range rows {
		if row.ID == ancient.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("ancient fact was evicted from the cold layer")
	}
	if len(rows) != 301 {
		t.Fatalf("cold rows = %d, want 301 (no age cap)", len(rows))
	}
}

// BenchmarkFocusScaleReadPaths measures the frozen focus path as the ledger
// grows. The path classifies the whole snapshot on every page (counts describe
// the whole projection), so the result is linear in facts, not constant; the
// numbers are recorded in the task report and are the only basis for any
// optimization.
func BenchmarkFocusScaleReadPaths(b *testing.B) {
	for _, n := range []int{1000, 10000, 100000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			s, target, _, _ := focusHorizonFixture(b, n)
			versions := func() map[string]uint64 {
				_, v := s.ReadView()
				m, err := s.FactVersionsAt(v)
				if err != nil {
					b.Fatal(err)
				}
				return m
			}
			q := FocusQuery{Intent: target.ID, Layer: FocusCold, Limit: 200}
			b.Run("focus_page", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					page, err := Focus(s, q)
					if err != nil {
						b.Fatal(err)
					}
					if len(page.Facts) == 0 {
						b.Fatal("empty page")
					}
				}
			})
			b.Run("focus_items", func(b *testing.B) {
				db, _ := s.ReadView()
				v := versions()
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					rows, err := FocusItems(db, target.ID, v)
					if err != nil {
						b.Fatal(err)
					}
					if len(rows) == 0 {
						b.Fatal("empty projection")
					}
				}
			})
			b.Run("read_view", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					db, _ := s.ReadView()
					if len(db.Facts) == 0 {
						b.Fatal("empty snapshot")
					}
				}
			})
			b.Run("fact_versions_at", func(b *testing.B) {
				_, v := s.ReadView()
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if _, err := s.FactVersionsAt(v); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}
