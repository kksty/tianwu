package ledger

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// Prose is a run log, not a fact. A run that never wrote facts.jsonl may leave
// no fact at all; the run row (declared_facts=0) and the log are the trace.
// This used to add one "执行者自述" note per attempt, which made a canceled
// attempt look like a finding and put duplicate nodes on the graph.
func TestProseDoesNotBecomeAFact(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	g := &Ingestor{Store: st, MaxEvidence: 200}
	caseRoot := t.TempDir()
	work := filepath.Join(caseRoot, "in_1")
	if n := g.OnFinish("r1", "in_1", caseRoot, work, time.Now()); n != 0 {
		t.Fatalf("declared = %d, want 0", n)
	}
	if n := len(st.Snapshot().Facts); n != 0 {
		t.Fatalf("a run without a handoff left %d facts", n)
	}
}

// The summary layer is the text the console's fact panel shows under the title,
// and the API returns it whole. The ledger must therefore store what the
// executor declared; the budgets live in the projections (dispatch prompt,
// graph.json, commander board). A 600-rune clip here silently ended a real
// finding mid-word before any reader could see the rest (D114).
func TestDeclaredSummaryIsStoredInFull(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	g := &Ingestor{Store: st, MaxEvidence: 5}
	caseRoot := t.TempDir()
	workDir := filepath.Join(caseRoot, "in_x")
	long := strings.Repeat("子域枚举与 host:port 指纹。", 120) // 1440 runes, past the old clip
	writeFile(t, filepath.Join(workDir, HandoffDir, HandoffFile),
		`{"kind":"note","title":"长摘要","summary":"`+long+`"}`+"\n")

	if n := g.OnFinish("run_1", "in_x", caseRoot, workDir, time.Now().Add(-time.Minute)); n != 1 {
		t.Fatalf("declared = %d, want 1", n)
	}

	facts := st.ListFacts("in_x", 0)
	if len(facts) != 1 {
		t.Fatalf("facts = %d, want 1", len(facts))
	}
	if got := facts[0].Summary; got != long {
		t.Fatalf("summary was clipped: stored %d runes, declared %d", len([]rune(got)), len([]rune(long)))
	}
}

// The auto-harvest cap is a budget, not a priority: a fact's own declared
// evidence must always be registered, even when the work dir is full of scratch
// files that would have eaten the budget first (a reverse-engineering session
// looks exactly like that).
func TestDeclaredEvidenceSurvivesTheCap(t *testing.T) {
	caseRoot := t.TempDir()
	work := filepath.Join(caseRoot, "in_x")
	for i := 0; i < 10; i++ {
		writeFile(t, filepath.Join(work, fmt.Sprintf("scratch-%02d.txt", i)), "noise")
	}
	writeFile(t, filepath.Join(work, "z-proof.txt"), "the real proof")
	writeFile(t, filepath.Join(work, HandoffDir, HandoffFile),
		`{"kind":"artifact","title":"固件 fw.bin","evidence":["z-proof.txt"]}`)

	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	g := &Ingestor{Store: st, MaxEvidence: 3}
	if n := g.OnFinish("r1", "in_x", caseRoot, work, time.Now().Add(-time.Minute)); n != 1 {
		t.Fatalf("declared facts = %d, want 1", n)
	}

	db := st.Snapshot()
	if len(db.Evidence) > 3 {
		t.Fatalf("evidence cap ignored: %d rows", len(db.Evidence))
	}
	found := false
	for _, f := range db.Facts {
		if f.Kind != "artifact" {
			continue
		}
		if len(f.EvidenceIDs) != 1 {
			t.Fatalf("artifact has %d evidence ids, want 1", len(f.EvidenceIDs))
		}
		e, ok := st.EvidenceByID(f.EvidenceIDs[0])
		if !ok {
			t.Fatal("evidence id does not resolve")
		}
		// Admission preserves the bytes: URI is the content-addressed copy and
		// the working path survives only as provenance (F2/D150).
		if e.SourceURI != "in_x/z-proof.txt" {
			t.Fatalf("source_uri = %q, want in_x/z-proof.txt", e.SourceURI)
		}
		if e.Digest == "" || e.URI != ".evidence/objects/"+e.Digest {
			t.Fatalf("evidence not preserved: uri=%q digest=%q", e.URI, e.Digest)
		}
		found = true
	}
	if !found {
		t.Fatalf("artifact fact missing: %+v", db.Facts)
	}
}

// A run's leftover files are a note, not loot: the ledger can see they exist,
// not whether they are a stolen dump or a scratch file (D54).
func TestHarvestIsANoteNotLoot(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	caseRoot := t.TempDir()
	work := filepath.Join(caseRoot, "in_x")
	writeFile(t, filepath.Join(work, "admin-200.html"), "<html>ok</html>")

	g := &Ingestor{Store: st, MaxEvidence: 10}
	g.OnFinish("r1", "in_x", caseRoot, work, time.Now().Add(-time.Minute))

	for _, f := range st.Snapshot().Facts {
		if f.Title != "本次产出文件" {
			continue
		}
		if f.Kind != "note" || f.Origin != model.OriginDerived {
			t.Fatalf("harvest fact = %+v", f)
		}
		return
	}
	t.Fatal("harvest fact missing")
}

// A retried or steered direction runs several times. The derived "this run's
// files" note is per direction, not per attempt: appending one per finish put
// 16 near-identical rows on a real board and made the graph look busier than
// the work was. The latest run's file list replaces the text in place.
func TestHarvestNoteIsRefreshedNotDuplicated(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	caseRoot := t.TempDir()
	work := filepath.Join(caseRoot, "in_x")
	writeFile(t, filepath.Join(work, "first.txt"), "one")
	g := &Ingestor{Store: st, MaxEvidence: 10}
	g.OnFinish("r1", "in_x", caseRoot, work, time.Now().Add(-time.Minute))

	writeFile(t, filepath.Join(work, "second.txt"), "two")
	g.OnFinish("r2", "in_x", caseRoot, work, time.Now().Add(-time.Minute))

	var notes []model.Fact
	for _, f := range st.Snapshot().Facts {
		if f.Title == derivedProductsNote {
			notes = append(notes, f)
		}
	}
	if len(notes) != 1 {
		t.Fatalf("derived note rows = %d, want 1: %+v", len(notes), notes)
	}
	if !strings.Contains(notes[0].Doc, "second.txt") || !strings.Contains(notes[0].Doc, "first.txt") {
		t.Fatalf("refreshed note lost files: %q", notes[0].Doc)
	}
}

func TestDeclaredEvidenceCarriesItsSource(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	g := &Ingestor{Store: st, MaxEvidence: 5}
	caseRoot := t.TempDir()
	workDir := filepath.Join(caseRoot, "in_x")
	writeFile(t, filepath.Join(workDir, "admin-200.html"), "<html>ok</html>")
	writeFile(t, filepath.Join(workDir, HandoffDir, HandoffFile),
		`{"kind":"vuln","title":"带出处","evidence":[{"file":"admin-200.html","url":"https://target/admin"}]}`+"\n")

	g.OnFinish("run_1", "in_x", caseRoot, workDir, time.Now().Add(-time.Minute))

	db := st.Snapshot()
	var ids []string
	for _, f := range db.Facts {
		if f.Kind == "vuln" {
			ids = f.EvidenceIDs
		}
	}
	if len(ids) != 1 {
		t.Fatalf("vuln evidence ids = %v (facts %+v)", ids, db.Facts)
	}
	e, ok := st.EvidenceByID(ids[0])
	if !ok {
		t.Fatal("evidence id does not resolve")
	}
	if e.From != "url:https://target/admin" {
		t.Fatalf("from = %q", e.From)
	}
}

// A finding whose only support is a live URL must reach the ledger, with the URL
// kept as a ref and no evidence rows. Dropping it (the old behaviour) meant the
// report could not even say "we saw it, we can no longer show it" (D44).
func TestDeclaredFactWithoutBytesKeepsItsRefs(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	g := &Ingestor{Store: st, MaxEvidence: 5}
	caseRoot := t.TempDir()
	workDir := filepath.Join(caseRoot, "in_x")
	writeFile(t, filepath.Join(workDir, HandoffDir, HandoffFile),
		`{"kind":"vuln","title":"只存在于线上","evidence":[{"url":"https://target/admin"}]}`+"\n")

	if n := g.OnFinish("run_1", "in_x", caseRoot, workDir, time.Now().Add(-time.Minute)); n != 1 {
		t.Fatalf("declared facts = %d, want 1 (the fact must not be dropped)", n)
	}

	db := st.Snapshot()
	if len(db.Evidence) != 0 {
		t.Fatalf("a url is not evidence: %+v", db.Evidence)
	}
	var got []model.Ref
	for _, f := range db.Facts {
		if f.Kind == "vuln" {
			got = f.Refs
			if len(f.EvidenceIDs) != 0 {
				t.Fatalf("vuln has evidence ids %v", f.EvidenceIDs)
			}
		}
	}
	want := model.Ref{Scheme: model.RefURL, Locator: "https://target/admin"}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("refs = %+v, want [%+v]", got, want)
	}
}

// A playbook line is a valid declaration but not a finding: it is routed to the
// project's playbook table, so the board, the report and the verifier never see
// it as a fact (D70).
func TestDeclaredPlaybookBecomesKnowledgeNotAFact(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	g := &Ingestor{Store: st, MaxEvidence: 5}
	caseRoot := t.TempDir()
	workDir := filepath.Join(caseRoot, "in_x")
	writeFile(t, filepath.Join(workDir, HandoffDir, HandoffFile),
		`{"kind":"playbook","title":"ThinkPHP RCE","summary":"5.0.x 且日志可写时可用","doc":"curl -s 'http://T/index.php?s=...'","tags":["product:thinkphp"],"outcome":"dead_end"}`+"\n")

	if n := g.OnFinish("run_1", "in_x", caseRoot, workDir, time.Now().Add(-time.Minute)); n != 1 {
		t.Fatalf("declared = %d, want 1 (a playbook still honours the handoff)", n)
	}
	db := st.Snapshot()
	for _, f := range db.Facts {
		if f.Kind == "playbook" {
			t.Fatalf("playbook leaked into facts: %+v", f)
		}
	}
	if len(db.Playbooks) != 1 {
		t.Fatalf("playbooks = %+v", db.Playbooks)
	}
	p := db.Playbooks[0]
	if p.Title != "ThinkPHP RCE" || p.Doc == "" || p.When == "" {
		t.Fatalf("playbook content lost: %+v", p)
	}
	if p.Status != model.PlaybookConfirmed || p.Origin != model.PlaybookFromExecutor {
		t.Fatalf("an executor declaration must be confirmed: %+v", p)
	}
	if p.Outcome != model.PlaybookDeadEnd || p.IntentID != "in_x" || len(p.Tags) != 1 {
		t.Fatalf("playbook provenance = %+v", p)
	}
}

// Declaration identity comes from the writer, never from the title. Two audit
// findings can share a title and a file and must land as two facts; a line that
// continues the same observation (same declaration_id) updates that row instead
// of adding a second one.
func TestDeclaredIdentitySeparatesObservationsAndAdoptsSourceRef(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	g := &Ingestor{Store: st, MaxEvidence: 5}
	caseRoot := t.TempDir()
	workDir := filepath.Join(caseRoot, "in_x")
	writeFile(t, filepath.Join(workDir, HandoffDir, HandoffFile),
		`{"kind":"vuln","title":"同名结论","declaration_id":"obs-1"}`+"\n"+
			`{"kind":"vuln","title":"同名结论","declaration_id":"obs-2"}`+"\n"+
			`{"kind":"vuln","title":"同名结论","source_ref":"report:audit-1#AUD-001"}`+"\n")

	if n := g.OnFinish("run_1", "in_x", caseRoot, workDir, time.Now().Add(-time.Minute)); n != 3 {
		t.Fatalf("declared = %d, want 3 distinct declarations", n)
	}
	before := st.Snapshot()
	if len(before.Facts) != 3 {
		t.Fatalf("three declarations collapsed into %d facts: %+v", len(before.Facts), before.Facts)
	}
	obs1, ok := st.FindDeclaration("in_x", "", "obs-1")
	if !ok {
		t.Fatalf("declaration identity lost: %+v", before.Facts)
	}

	// The report import later supplies a source ref for the same declaration:
	// it adopts the row recorded for obs-1 instead of becoming a fourth fact.
	writeFile(t, filepath.Join(workDir, HandoffDir, HandoffFile),
		`{"kind":"vuln","title":"同名结论","declaration_id":"obs-1","source_ref":"report:audit-1#AUD-009"}`+"\n")
	if n := g.OnFinish("run_2", "in_x", caseRoot, workDir, time.Now().Add(-time.Minute)); n != 1 {
		t.Fatalf("declared = %d, want 1 line", n)
	}
	after := st.Snapshot()
	if len(after.Facts) != 3 {
		t.Fatalf("adopting a source ref added a fact: %+v", after.Facts)
	}
	if got, ok := st.FindDeclaration("in_x", "report:audit-1#AUD-009", ""); !ok || got.ID != obs1.ID {
		t.Fatalf("the obs-1 row did not adopt the source ref: %+v %v (want %s)", got, ok, obs1.ID)
	}
}

// graph.json writes every evidence URI relative to the case directory with a
// parent hop ("../in_x/file", evidence_base=case_dir). A model that copies that
// form into facts.jsonl used to be rejected as 路径越界 and the whole fact was
// dropped, even though the file exists and belongs to the same project. A
// sibling case is inside the project cases tree, so the fact must be kept and
// the evidence row must canonicalise to the ledger's own case URI.
func TestDeclaredSiblingCaseEvidenceIsRegistered(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	caseRoot := t.TempDir()
	workDir := filepath.Join(caseRoot, "in_b")
	writeFile(t, filepath.Join(caseRoot, "in_a", "raw", "proof.txt"), "the proof")
	writeFile(t, filepath.Join(workDir, HandoffDir, HandoffFile),
		`{"kind":"vuln","title":"上一轮的证据","summary":"s","evidence":["../in_a/raw/proof.txt"]}`+"\n")

	run, err := st.AddRun(model.Run{IntentID: "in_b", Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	g := &Ingestor{Store: st, MaxEvidence: 5}
	if n := g.OnFinish(run.ID, "in_b", caseRoot, workDir, time.Now().Add(-time.Minute)); n != 1 {
		t.Fatalf("declared = %d, want the fact kept", n)
	}

	settled, ok := st.GetRun(run.ID)
	if !ok {
		t.Fatal("run missing")
	}
	if len(settled.Warnings) != 0 {
		t.Fatalf("a legal sibling-case path produced warnings: %v", settled.Warnings)
	}

	db := st.Snapshot()
	if len(db.Evidence) != 1 || db.Evidence[0].SourceURI != "in_a/raw/proof.txt" {
		t.Fatalf("evidence = %+v, want one row with SourceURI in_a/raw/proof.txt", db.Evidence)
	}
	var fact model.Fact
	for _, f := range db.Facts {
		if f.Title == "上一轮的证据" {
			fact = f
		}
	}
	if fact.Title == "" {
		t.Fatalf("the fact was dropped: %+v", db.Facts)
	}
	if len(fact.EvidenceIDs) == 0 {
		t.Fatalf("fact has no evidence ids: %+v", fact)
	}
}
