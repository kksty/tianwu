package report

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/evidence"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// preservedReportFixture creates a real content-addressed evidence row and
// returns a tamper function. The report must refuse bytes that no longer hash to
// the recorded digest: copying them would launder tampering into the deliverable
// (F2/D150).
func preservedReportFixture(t *testing.T) (caseRoot string, row model.Evidence, original, uri string, tamper func()) {
	t.Helper()
	caseRoot = t.TempDir()
	original = "HTTP/1.1 200 OK\nadmin visible"
	src := filepath.Join(caseRoot, "in_1", "admin.html")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := evidence.Preserve(caseRoot, src)
	if err != nil {
		t.Fatal(err)
	}
	row = model.Evidence{ID: "e_1", URI: p.URI, Digest: p.Digest, Name: "admin.html", Size: p.Size}
	uri = p.URI
	tamper = func() {
		t.Helper()
		if err := os.WriteFile(filepath.Join(caseRoot, filepath.FromSlash(p.URI)), []byte("tampered"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return caseRoot, row, original, uri, tamper
}

func TestCopyEvidenceRefusesDigestConflict(t *testing.T) {
	caseRoot, row, _, uri, tamper := preservedReportFixture(t)
	tamper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, EvidenceDir), 0o755); err != nil {
		t.Fatal(err)
	}
	fact := model.Fact{ID: "f_1", Kind: "vuln", Title: "t", EvidenceIDs: []string{row.ID}}
	db := store.DB{Evidence: []model.Evidence{row}}

	g := &Generator{CaseRoot: caseRoot}
	_, err := g.copyEvidence(dir, db, []model.Fact{fact})
	if !errors.Is(err, evidence.ErrDigestMismatch) {
		t.Fatalf("copy of tampered evidence: err = %v, want ErrDigestMismatch", err)
	}
	for _, want := range []string{row.ID, uri} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error must name the evidence id/file %q: %v", want, err)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(dir, EvidenceDir))
	if len(entries) != 0 {
		t.Fatalf("a failed verified copy left files behind: %v", entries)
	}
}

func TestCopyEvidenceVerifiesPreservedBytes(t *testing.T) {
	caseRoot, row, original, _, _ := preservedReportFixture(t)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, EvidenceDir), 0o755); err != nil {
		t.Fatal(err)
	}
	fact := model.Fact{ID: "f_1", Kind: "vuln", Title: "t", EvidenceIDs: []string{row.ID}}
	db := store.DB{Evidence: []model.Evidence{row}}

	g := &Generator{CaseRoot: caseRoot}
	got, err := g.copyEvidence(dir, db, []model.Fact{fact})
	if err != nil {
		t.Fatal(err)
	}
	// The copy is named after the admitted source file, not the sha256 address,
	// so a client can still tell what it is looking at.
	want := EvidenceDir + "/e_1-admin.html"
	if len(got["f_1"]) != 1 || got["f_1"][0] != want {
		t.Fatalf("copied = %v, want %s", got["f_1"], want)
	}
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(want)))
	if err != nil || string(b) != original {
		t.Fatalf("report copy = %q err=%v, want the preserved original", b, err)
	}
}

func TestPreflightRefusesDigestConflict(t *testing.T) {
	caseRoot, row, _, uri, tamper := preservedReportFixture(t)
	tamper()

	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	ev, err := st.AddEvidence(model.Evidence{URI: row.URI, Digest: row.Digest, Name: row.Name, Size: row.Size})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddFact(model.Fact{IntentID: "in_1", Kind: "vuln", Title: "t", EvidenceIDs: []string{ev.ID}}); err != nil {
		t.Fatal(err)
	}

	g := &Generator{Store: st, CaseRoot: caseRoot}
	err = g.Preflight()
	if !errors.Is(err, evidence.ErrDigestMismatch) {
		t.Fatalf("preflight over tampered evidence: err = %v, want ErrDigestMismatch", err)
	}
	for _, want := range []string{ev.ID, uri} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error must name the evidence id/file %q: %v", want, err)
		}
	}
}

// A legacy row (no digest) keeps working: there is nothing to check its bytes
// against, so the report copies it as before. This is the documented old-row
// boundary, not a silent claim that the bytes are proven.
func TestCopyEvidenceStillCopiesLegacyRowWithoutDigest(t *testing.T) {
	caseRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(caseRoot, "in_1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(caseRoot, "in_1", "legacy.txt"), []byte("legacy"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, EvidenceDir), 0o755); err != nil {
		t.Fatal(err)
	}
	fact := model.Fact{ID: "f_1", Kind: "vuln", Title: "t", EvidenceIDs: []string{"e_legacy"}}
	db := store.DB{Evidence: []model.Evidence{{ID: "e_legacy", URI: "in_1/legacy.txt"}}}
	g := &Generator{CaseRoot: caseRoot}
	got, err := g.copyEvidence(dir, db, []model.Fact{fact})
	if err != nil {
		t.Fatalf("legacy evidence must stay reportable: %v", err)
	}
	if len(got["f_1"]) != 1 || got["f_1"][0] != EvidenceDir+"/e_legacy-legacy.txt" {
		t.Fatalf("copied = %v", got["f_1"])
	}
}
