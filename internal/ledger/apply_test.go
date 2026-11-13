package ledger

import (
	"path/filepath"
	"testing"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

func openApplyLedger(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func applyRun(t *testing.T, st *store.Store, start int64) model.Run {
	t.Helper()
	r, err := st.AddRun(model.Run{IntentID: "in_1", Node: "local", DeclarationStart: &start})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func applyLine(id, summary string, offset int64) DeclaredFact {
	return DeclaredFact{
		DeclarationID: id,
		Offset:        offset,
		Location:      ".tw/facts.jsonl#bytes=0-10",
		Kind:          "vuln",
		Title:         "同一观察",
		Summary:       summary,
		Confidence:    "observed",
		Proof:         "dynamic",
	}
}

// A declaration identity the ledger has never seen becomes a fact, with the
// writer's provenance recorded as declared.
func TestApplyDeclarationCreatesByWriterIdentity(t *testing.T) {
	st := openApplyLedger(t)
	run := applyRun(t, st, 0)

	res, err := ApplyDeclaration(st, "in_1", run.ID, applyLine("obs-1", "第一次", 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Created || !res.Changed || res.FactID == "" {
		t.Fatalf("result = %+v, want created+changed", res)
	}
	f, ok := st.FactByID(res.FactID)
	if !ok {
		t.Fatal("fact not stored")
	}
	if f.DeclarationID != "obs-1" || f.Summary != "第一次" || f.Origin != model.OriginDeclared || f.SourceRunID != run.ID {
		t.Fatalf("stored fact = %+v", f)
	}
	if f.ClaimVersion != 1 || f.Status != "unverified" {
		t.Fatalf("new fact version/status = v%d %q", f.ClaimVersion, f.Status)
	}
}

// The live watcher and the finish settlement re-read the same line. That second
// read must be inert: same row, same claim version, no notification path.
func TestApplyDeclarationIdenticalRereadIsANoOp(t *testing.T) {
	st := openApplyLedger(t)
	run := applyRun(t, st, 0)
	line := applyLine("obs-1", "第一次", 0)
	first, err := ApplyDeclaration(st, "in_1", run.ID, line, nil)
	if err != nil {
		t.Fatal(err)
	}
	again, err := ApplyDeclaration(st, "in_1", run.ID, line, []string{}) // finish passes an empty authoritative set
	if err != nil {
		t.Fatal(err)
	}
	if again.FactID != first.FactID || again.Created || again.Changed {
		t.Fatalf("duplicate read = %+v, want no-op on %s", again, first.FactID)
	}
	f, _ := st.FactByID(first.FactID)
	if f.ClaimVersion != 1 {
		t.Fatalf("duplicate read bumped the claim version to v%d", f.ClaimVersion)
	}
	if facts := st.ListFacts("in_1", 0); len(facts) != 1 {
		t.Fatalf("duplicate read added a row: %+v", facts)
	}
}

// A correction under the same identity updates the same row and starts a new
// assertion version; the display title and kind are not a second identity.
func TestApplyDeclarationCorrectsTheSameRow(t *testing.T) {
	st := openApplyLedger(t)
	run := applyRun(t, st, 0)
	first, err := ApplyDeclaration(st, "in_1", run.ID, applyLine("obs-1", "第一次", 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	corrected := applyLine("obs-1", "更正后的结论", 0)
	corrected.Kind = "cred"
	corrected.Title = "改了显示名"
	res, err := ApplyDeclaration(st, "in_1", run.ID, corrected, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.FactID != first.FactID || res.Created || !res.Changed {
		t.Fatalf("correction = %+v, want update of %s", res, first.FactID)
	}
	f, _ := st.FactByID(first.FactID)
	if f.Summary != "更正后的结论" || f.ClaimVersion != 2 {
		t.Fatalf("corrected fact = %+v (version %d)", f, f.ClaimVersion)
	}
	if facts := st.ListFacts("in_1", 0); len(facts) != 1 {
		t.Fatalf("correction added a second row: %+v", facts)
	}
}

// A mid-run read never owns the evidence set: evidence the finish settlement
// preserved must survive a live correction until the next settlement replaces it.
func TestApplyDeclarationLiveKeepsPreservedEvidence(t *testing.T) {
	st := openApplyLedger(t)
	run := applyRun(t, st, 0)
	settled, err := ApplyDeclaration(st, "in_1", run.ID, applyLine("obs-1", "第一次", 0), []string{"e_preserved"})
	if err != nil {
		t.Fatal(err)
	}
	// Live path: no evidence ids resolved, corrected summary.
	live, err := ApplyDeclaration(st, "in_1", run.ID, applyLine("obs-1", "运行中更正", 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := st.FactByID(settled.FactID)
	if live.FactID != settled.FactID || !live.Changed {
		t.Fatalf("live correction = %+v", live)
	}
	if f.Summary != "运行中更正" || f.ClaimVersion != 2 {
		t.Fatalf("live correction content/version = %+v v%d", f, f.ClaimVersion)
	}
	if len(f.EvidenceIDs) != 1 || f.EvidenceIDs[0] != "e_preserved" {
		t.Fatalf("live correction cleared preserved evidence: %v", f.EvidenceIDs)
	}

	// Finish later attaches the preserved ids it admitted: same row, same
	// content, the authoritative evidence set.
	finished, err := ApplyDeclaration(st, "in_1", run.ID, applyLine("obs-1", "运行中更正", 0), []string{"e_preserved"})
	if err != nil {
		t.Fatal(err)
	}
	if finished.FactID != settled.FactID || finished.Created {
		t.Fatalf("finish created a second row: %+v", finished)
	}
	f, _ = st.FactByID(settled.FactID)
	if f.Summary != "运行中更正" || len(f.EvidenceIDs) != 1 || f.EvidenceIDs[0] != "e_preserved" {
		t.Fatalf("finish lost live content or evidence: %+v", f)
	}
}

// The source run is bookkeeping, not part of the claim: a read that cannot prove
// ownership and changes nothing must not rewrite which run declared the fact.
func TestApplyDeclarationKeepsSourceRunWhenTheLineIsUnchanged(t *testing.T) {
	st := openApplyLedger(t)
	owner := applyRun(t, st, 0)
	line := applyLine("obs-1", "第一次", 0)
	if _, err := ApplyDeclaration(st, "in_1", owner.ID, line, nil); err != nil {
		t.Fatal(err)
	}
	// A later run in the same case re-reads the line; its declaration window
	// starts after this line, so it cannot claim it.
	later := applyRun(t, st, 100)
	res, err := ApplyDeclaration(st, "in_1", later.ID, line, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed {
		t.Fatalf("re-read with an unchanged line reported a change: %+v", res)
	}
	f, _ := st.FactByID(res.FactID)
	if f.SourceRunID != owner.ID {
		t.Fatalf("source run = %q, want the original owner %q", f.SourceRunID, owner.ID)
	}
}
