package traffic

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/kksty/tianwu/internal/evidence"
	"github.com/kksty/tianwu/internal/store"
)

// The HAR import is an admission path: it records a preserved copy plus the
// SHA-256 of the admitted bytes, and every derived fact points at that row.
// Overwriting the capture afterwards cannot change what the import proved
// (F2/D150).
func TestImportPreservesTheCaptureBytes(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	caseRoot := t.TempDir()
	capture := writeHAR(t)
	original, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}

	res, err := (&Importer{Store: st, CaseRoot: caseRoot}).Import(capture)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	row, ok := st.EvidenceByID(res.EvidenceID)
	if !ok {
		t.Fatal("evidence row missing")
	}
	sum := sha256.Sum256(original)
	want := hex.EncodeToString(sum[:])
	if row.Digest != want {
		t.Fatalf("digest = %q, want %q", row.Digest, want)
	}
	if row.URI != ".evidence/objects/"+want {
		t.Fatalf("uri = %q, want the content-addressed copy", row.URI)
	}
	if filepath.ToSlash(row.SourceURI) != filepath.ToSlash(capture) {
		t.Fatalf("source_uri = %q, want %q", row.SourceURI, capture)
	}
	if row.Size != int64(len(original)) {
		t.Fatalf("size = %d, want %d", row.Size, len(original))
	}

	for _, f := range st.ListFacts("", 0) {
		if len(f.EvidenceIDs) != 1 || f.EvidenceIDs[0] != row.ID {
			t.Fatalf("derived fact %q must point at the preserved capture: %+v", f.Title, f.EvidenceIDs)
		}
	}

	// Overwrite the capture with same-length different bytes; the ledger still
	// proves the bytes that were imported.
	rewritten := bytes.Repeat([]byte("X"), len(original))
	if err := os.WriteFile(capture, rewritten, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := evidence.Read(caseRoot, row)
	if err != nil {
		t.Fatalf("reading the admitted capture: %v", err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("admitted capture = %q, want the original bytes", got)
	}
}
