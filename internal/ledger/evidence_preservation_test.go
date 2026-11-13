package ledger

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/aop/tool"
	"github.com/kksty/tianwu/internal/artifact"
	"github.com/kksty/tianwu/internal/evidence"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// F2/D150 contract at the real admission entry points (not Store.AddEvidence):
// the bytes an evidence row points at are the bytes that existed at admission,
// even after the run's working path is overwritten by a retry.
//
// The immutability contract: Evidence.URI opens a preserved copy keyed by the
// SHA-256 of the admitted bytes; SourceURI keeps the working path only as
// provenance. Reading the row re-hashes the copy, so tampering is an explicit
// error instead of a silent change of meaning. Rows written before D150 have no
// digest; they still read as they always did (documented boundary, no migration
// scan).

func newPreservationLedger(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func preservationSHA(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

func factByTitle(t *testing.T, st *store.Store, intentID, title string) model.Fact {
	t.Helper()
	for _, f := range st.ListFacts(intentID, 0) {
		if f.Title == title {
			return f
		}
	}
	t.Fatalf("fact %q not found (facts %+v)", title, st.ListFacts(intentID, 0))
	return model.Fact{}
}

func soleEvidence(t *testing.T, st *store.Store, f model.Fact) model.Evidence {
	t.Helper()
	if len(f.EvidenceIDs) != 1 {
		t.Fatalf("fact %q has evidence ids %v, want exactly one", f.Title, f.EvidenceIDs)
	}
	e, ok := st.EvidenceByID(f.EvidenceIDs[0])
	if !ok {
		t.Fatalf("evidence %s does not resolve", f.EvidenceIDs[0])
	}
	return e
}

func appendText(t *testing.T, path, body string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(body); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestIngestPreservesBytesAgainstSameLengthOverwrite(t *testing.T) {
	st := newPreservationLedger(t)
	caseRoot := t.TempDir()
	workDir := filepath.Join(caseRoot, "in_x")
	original := "0123456789abcdef"
	rewritten := "fedcba9876543210" // same length on purpose: size checks cannot catch this
	if len(original) != len(rewritten) {
		t.Fatal("fixture must use same-length contents")
	}
	proof := filepath.Join(workDir, "proof.txt")
	writeFile(t, proof, original)
	writeFile(t, filepath.Join(workDir, HandoffDir, HandoffFile),
		`{"kind":"vuln","title":"保留原件","summary":"s","evidence":["proof.txt"]}`+"\n")

	g := &Ingestor{Store: st, MaxEvidence: 10}
	// Future `since` disables auto-harvest so this test covers the declared path
	// deterministically; harvest is covered separately.
	if n := g.OnFinish("r1", "in_x", caseRoot, workDir, time.Now().Add(time.Hour)); n != 1 {
		t.Fatalf("declared = %d, want 1", n)
	}

	fact := factByTitle(t, st, "in_x", "保留原件")
	row := soleEvidence(t, st, fact)
	if row.Digest != preservationSHA(original) {
		t.Fatalf("digest = %q, want %q", row.Digest, preservationSHA(original))
	}
	if row.URI != ".evidence/objects/"+row.Digest {
		t.Fatalf("uri = %q, want the content-addressed copy", row.URI)
	}
	if row.SourceURI != "in_x/proof.txt" {
		t.Fatalf("source_uri = %q, want in_x/proof.txt", row.SourceURI)
	}
	if row.Size != int64(len(original)) {
		t.Fatalf("size = %d, want %d", row.Size, len(original))
	}

	// Re-settling the same run (a duplicated finish callback) must be inert: the
	// historical evidence row and the fact's binding to it stay as they were.
	g.OnFinish("r1", "in_x", caseRoot, workDir, time.Now().Add(time.Hour))
	if again, ok := st.EvidenceByID(row.ID); !ok || again.URI != row.URI || again.Digest != row.Digest {
		t.Fatalf("re-settle changed the historical evidence row: %+v", again)
	}
	if again := factByTitle(t, st, "in_x", "保留原件"); len(again.EvidenceIDs) != 1 || again.EvidenceIDs[0] != row.ID {
		t.Fatalf("re-settle changed the fact's evidence binding: %v", again.EvidenceIDs)
	}

	// The retry overwrites the run's working path with different bytes of the
	// same length. The already-admitted fact must keep proving the old bytes.
	writeFile(t, proof, rewritten)
	if current, _ := os.ReadFile(proof); string(current) != rewritten {
		t.Fatalf("fixture: working path = %q, want the rewrite", current)
	}
	got, err := evidence.Read(caseRoot, row)
	if err != nil {
		t.Fatalf("reading the admitted evidence: %v", err)
	}
	if string(got) != original {
		t.Fatalf("admitted evidence reads %q, want the original %q", got, original)
	}

	// The row itself is unchanged, and the fact still cites it.
	after, ok := st.EvidenceByID(row.ID)
	if !ok || after.URI != row.URI || after.Digest != row.Digest || after.SourceURI != row.SourceURI || after.Size != row.Size {
		t.Fatalf("evidence row changed: before %+v after %+v", row, after)
	}
	stillCited := false
	for _, id := range fact.EvidenceIDs {
		if id == row.ID {
			stillCited = true
		}
	}
	if !stillCited {
		t.Fatalf("fact evidence ids = %v, want %s", fact.EvidenceIDs, row.ID)
	}
}

// Two contents admitted from the same working path must be two rows with two
// independently readable objects. This is what makes "the fact was verified
// against these bytes" meaningful after a retry rewrites the path.
func TestTwoContentsForSameSourcePathAreIndependentlyReadable(t *testing.T) {
	st := newPreservationLedger(t)
	caseRoot := t.TempDir()
	workDir := filepath.Join(caseRoot, "in_x")
	proof := filepath.Join(workDir, "response.txt")
	handoff := filepath.Join(workDir, HandoffDir, HandoffFile)

	writeFile(t, proof, "FIRST-OBSERVATION")
	writeFile(t, handoff, `{"kind":"vuln","title":"第一次观察","summary":"s","evidence":["response.txt"]}`+"\n")
	g := &Ingestor{Store: st, MaxEvidence: 10}
	noHarvest := time.Now().Add(time.Hour)
	g.OnFinish("r1", "in_x", caseRoot, workDir, noHarvest)
	first := soleEvidence(t, st, factByTitle(t, st, "in_x", "第一次观察"))

	writeFile(t, proof, "SECOND-OBSERVATION")
	appendText(t, handoff, `{"kind":"vuln","title":"第二次观察","summary":"s","evidence":["response.txt"]}`+"\n")
	g.OnFinish("r2", "in_x", caseRoot, workDir, noHarvest)
	second := soleEvidence(t, st, factByTitle(t, st, "in_x", "第二次观察"))

	if first.ID == second.ID || first.URI == second.URI {
		t.Fatalf("two contents reused one evidence row: %+v %+v", first, second)
	}
	for want, row := range map[string]model.Evidence{"FIRST-OBSERVATION": first, "SECOND-OBSERVATION": second} {
		b, err := evidence.Read(caseRoot, row)
		if err != nil {
			t.Fatalf("reading %s: %v", row.ID, err)
		}
		if string(b) != want {
			t.Fatalf("evidence %s reads %q, want %q", row.ID, b, want)
		}
	}
}

// A retry that re-declares the same identity updates the fact to the new
// evidence row; the historical row must not be destroyed (it is what the old
// review/claim version was proven against).
func TestRetryKeepsHistoricalEvidenceRow(t *testing.T) {
	st := newPreservationLedger(t)
	caseRoot := t.TempDir()
	workDir := filepath.Join(caseRoot, "in_x")
	proof := filepath.Join(workDir, "proof.txt")
	handoff := filepath.Join(workDir, HandoffDir, HandoffFile)

	writeFile(t, proof, "ORIGINAL-EVIDENCE")
	writeFile(t, handoff, `{"kind":"vuln","title":"重试目标","declaration_id":"obs-1","summary":"s","evidence":["proof.txt"]}`+"\n")
	g := &Ingestor{Store: st, MaxEvidence: 10}
	noHarvest := time.Now().Add(time.Hour)
	g.OnFinish("r1", "in_x", caseRoot, workDir, noHarvest)
	historical := soleEvidence(t, st, factByTitle(t, st, "in_x", "重试目标"))

	writeFile(t, proof, "RETRIED-EVIDENCE!")
	appendText(t, handoff, `{"kind":"vuln","title":"重试目标","declaration_id":"obs-1","summary":"s","evidence":["proof.txt"]}`+"\n")
	g.OnFinish("r2", "in_x", caseRoot, workDir, noHarvest)

	fact := factByTitle(t, st, "in_x", "重试目标")
	current := soleEvidence(t, st, fact)
	if current.ID == historical.ID {
		t.Fatal("the retry should admit the new bytes as a new evidence row")
	}
	oldRow, ok := st.EvidenceByID(historical.ID)
	if !ok {
		t.Fatal("the historical evidence row was destroyed by the retry")
	}
	if b, err := evidence.Read(caseRoot, oldRow); err != nil || string(b) != "ORIGINAL-EVIDENCE" {
		t.Fatalf("historical evidence = %q err=%v, want the original bytes", b, err)
	}
}

// Harvest is an admission path too: a file the run produced becomes evidence,
// so its admitted bytes must be preserved.
func TestHarvestPreservesBytes(t *testing.T) {
	st := newPreservationLedger(t)
	caseRoot := t.TempDir()
	workDir := filepath.Join(caseRoot, "in_x")
	original := "ADMIN-200-BODY!!"
	writeFile(t, filepath.Join(workDir, "admin-200.html"), original)

	g := &Ingestor{Store: st, MaxEvidence: 10}
	g.OnFinish("r1", "in_x", caseRoot, workDir, time.Now().Add(-time.Minute))

	var harvested model.Evidence
	for _, e := range st.Evidence() {
		if e.SourceURI == "in_x/admin-200.html" {
			harvested = e
		}
	}
	if harvested.ID == "" || harvested.Digest == "" {
		t.Fatalf("harvested evidence not preserved: %+v", st.Evidence())
	}
	writeFile(t, filepath.Join(workDir, "admin-200.html"), "REWRITTEN-BODY!!")
	if b, err := evidence.Read(caseRoot, harvested); err != nil || string(b) != original {
		t.Fatalf("harvested evidence = %q err=%v, want %q", b, err, original)
	}
}

// The manual import path: the copy into the case is a working file, the ledger
// address is the preserved object, and SourceURI keeps where the user imported
// from.
func TestAttachImportPreservesBytesAgainstWorkingCopyOverwrite(t *testing.T) {
	st := newPreservationLedger(t)
	caseRoot := t.TempDir()
	original := `{"results":[{"ip":"10.0.0.5","port":443}]}`
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "scan.jsonl")
	writeFile(t, src, original)

	imported, err := AttachImport(st, caseRoot, src, "scan", "资产导入", "扫描结果")
	if err != nil {
		t.Fatal(err)
	}
	row, ok := st.EvidenceByID(imported.EvidenceID)
	if !ok {
		t.Fatal("imported evidence missing")
	}
	if row.Digest != preservationSHA(original) {
		t.Fatalf("digest = %q, want %q", row.Digest, preservationSHA(original))
	}
	if got, want := row.URI, ".evidence/objects/"+row.Digest; got != want {
		t.Fatalf("uri = %q, want %q", got, want)
	}
	if filepath.ToSlash(row.SourceURI) != filepath.ToSlash(src) {
		t.Fatalf("source_uri = %q, want the imported path %q", row.SourceURI, src)
	}

	// Overwrite both the case copy and the original import path: the ledger
	// must still prove what was imported.
	writeFile(t, filepath.Join(caseRoot, filepath.FromSlash(imported.Path)), "OVERWRITTEN-WORKING-COPY")
	writeFile(t, src, "OVERWRITTEN-SOURCE")
	if b, err := evidence.Read(caseRoot, row); err != nil || string(b) != original {
		t.Fatalf("imported evidence = %q err=%v, want the imported bytes", b, err)
	}
}

// If the bytes cannot be preserved, admission refuses the evidence and says so
// on the run row; it never falls back to the mutable working path.
func TestAdmissionRefusesEvidenceWhenPreservationFails(t *testing.T) {
	st := newPreservationLedger(t)
	caseRoot := t.TempDir()
	workDir := filepath.Join(caseRoot, "in_x")
	writeFile(t, filepath.Join(workDir, "proof.txt"), "proof")
	// Block the store directory with a regular file so preservation cannot
	// create .evidence/objects.
	writeFile(t, filepath.Join(caseRoot, ".evidence"), "not a directory")
	writeFile(t, filepath.Join(workDir, HandoffDir, HandoffFile),
		`{"kind":"vuln","title":"无法保全","summary":"s","evidence":["proof.txt"]}`+"\n")

	run, err := st.AddRun(model.Run{IntentID: "in_x", Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	g := &Ingestor{Store: st, MaxEvidence: 10}
	g.OnFinish(run.ID, "in_x", caseRoot, workDir, time.Now().Add(time.Hour))

	if rows := st.Evidence(); len(rows) != 0 {
		t.Fatalf("evidence was admitted without preservation: %+v", rows)
	}
	settled, ok := st.GetRun(run.ID)
	if !ok || len(settled.Warnings) == 0 {
		t.Fatalf("preservation failure must be a run warning: %+v", settled)
	}
	joined := strings.Join(settled.Warnings, "\n")
	if !strings.Contains(joined, "无法保全") || !strings.Contains(joined, "proof.txt") {
		t.Fatalf("warning must name the failure and the path: %s", joined)
	}
}

// Native artifact bytes are materialized into the work dir and only then
// declared; ledger admission is what preserves them, so the citation path is
// covered without a second store in internal/artifact.
func TestArtifactMaterializationIsPreservedByAdmission(t *testing.T) {
	st := newPreservationLedger(t)
	root := t.TempDir()
	cases := filepath.Join(root, "cases")
	workDir := filepath.Join(cases, "in_x")
	original := `{"match":true}`
	if len(original) != 14 {
		t.Fatalf("fixture length = %d", len(original))
	}
	rec, err := artifact.Save(cases, "r_1", "in_x", "event", &tool.Artifact{
		Tool: "neutron", ResultId: "result", MediaType: "application/json", Data: []byte(original),
	})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(workDir, HandoffDir, HandoffFile),
		`{"kind":"note","title":"已核对原生结果","evidence":[{"artifact":"`+rec.Ref+`"}]}`+"\n")

	g := &Ingestor{Store: st, MaxEvidence: 5}
	g.OnFinish("r_1", "in_x", cases, workDir, time.Now().Add(time.Hour))

	fact := factByTitle(t, st, "in_x", "已核对原生结果")
	row := soleEvidence(t, st, fact)
	if row.Digest != preservationSHA(original) || !strings.HasPrefix(row.SourceURI, "in_x/native-evidence/") {
		t.Fatalf("native evidence not preserved from its materialized path: %+v", row)
	}
	materialized := filepath.Join(cases, filepath.FromSlash(row.SourceURI))
	writeFile(t, materialized, `{"MATCH":true}`) // same length, different bytes
	if b, err := evidence.Read(cases, row); err != nil || string(b) != original {
		t.Fatalf("native evidence = %q err=%v, want the materialized original", b, err)
	}
}
