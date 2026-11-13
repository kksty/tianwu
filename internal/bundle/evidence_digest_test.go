package bundle

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/evidence"
)

// preservedBundleCase creates a case root with one preserved evidence object and
// a ledger that points at it with the digest, like a real export input.
func preservedBundleCase(t *testing.T) (caseRoot, ledgerPath, uri string) {
	t.Helper()
	base := t.TempDir()
	caseRoot = filepath.Join(base, "cases")
	if err := os.MkdirAll(filepath.Join(caseRoot, "in_1"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(caseRoot, "in_1", "admin.html")
	if err := os.WriteFile(src, []byte("HTTP/1.1 200 OK"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := evidence.Preserve(caseRoot, src)
	if err != nil {
		t.Fatal(err)
	}
	ledgerPath = filepath.Join(base, "ledger.json")
	ledger := `{"version":1,"facts":[],"evidence":[{"id":"e_1","uri":"` + p.URI + `","digest":"` + p.Digest + `"}]}`
	if err := os.WriteFile(ledgerPath, []byte(ledger), 0o644); err != nil {
		t.Fatal(err)
	}
	return caseRoot, ledgerPath, p.URI
}

// Export must not launder tampered evidence into a transfer: the manifest hash
// proves the bytes traveled intact, not that they are the admitted proof.
func TestExportRefusesTamperedPreservedEvidence(t *testing.T) {
	caseRoot, ledgerPath, uri := preservedBundleCase(t)
	objectPath := filepath.Join(caseRoot, filepath.FromSlash(uri))
	if err := os.WriteFile(objectPath, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := Export(caseRoot, ledgerPath, filepath.Join(t.TempDir(), "case.zip"))
	if !errors.Is(err, evidence.ErrDigestMismatch) {
		t.Fatalf("Export over tampered evidence: err = %v, want ErrDigestMismatch (manifest %+v)", err, m)
	}
	for _, want := range []string{"e_1", uri} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error must name the evidence id/file %q: %v", want, err)
		}
	}
}

// A healthy case travels with its preserved objects, and Import verifies the
// restored ledger's digests before publishing the destination.
func TestExportCarriesPreservedObjectsAndImportVerifiesThem(t *testing.T) {
	caseRoot, ledgerPath, uri := preservedBundleCase(t)
	zipPath := filepath.Join(t.TempDir(), "case.zip")
	m, err := Export(caseRoot, ledgerPath, zipPath)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if len(m.Missing) != 0 {
		t.Fatalf("healthy preserved evidence reported missing: %v", m.Missing)
	}
	found := false
	for _, f := range m.Files {
		if strings.HasSuffix(f.Path, "/"+filepath.ToSlash(uri)) {
			found = true
		}
	}
	if !found {
		t.Fatalf("preserved object not in the bundle: %+v", m.Files)
	}

	dest := filepath.Join(t.TempDir(), "restored")
	if _, err := Import(zipPath, dest); err != nil {
		t.Fatalf("Import: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dest, caseDirName, filepath.FromSlash(uri)))
	if err != nil || string(b) != "HTTP/1.1 200 OK" {
		t.Fatalf("restored object = %q err=%v", b, err)
	}
}

// A bundle can be internally consistent (manifest hash matches the zip) while
// the ledger says the evidence is different bytes. Import must refuse it rather
// than publish a restored case whose ledger lies about its proof.
func TestImportRefusesBundleWhoseLedgerDigestConflicts(t *testing.T) {
	original := "HTTP/1.1 200 OK"
	tampered := "HTTP/1.1 500 OK"
	sumOriginal := sha256.Sum256([]byte(original))
	uri := ".evidence/objects/" + hex.EncodeToString(sumOriginal[:])
	ledger := `{"version":1,"facts":[],"evidence":[{"id":"e_1","uri":"` + uri + `","digest":"` + hex.EncodeToString(sumOriginal[:]) + `"}]}`

	zipPath := filepath.Join(t.TempDir(), "forged.zip")
	writeSignedBundle(t, zipPath, map[string][]byte{
		"ledger.json": []byte(ledger),
		"case/" + uri: []byte(tampered), // manifest hash matches this tampered body
	})

	dest := filepath.Join(t.TempDir(), "restored")
	_, err := Import(zipPath, dest)
	if !errors.Is(err, evidence.ErrDigestMismatch) {
		t.Fatalf("Import of a digest-conflicting bundle: err = %v, want ErrDigestMismatch", err)
	}
	if _, statErr := os.Stat(dest); statErr == nil {
		t.Fatal("a rejected import must not leave a destination behind")
	}
}

// A preserved original that was already gone at export time keeps the old
// Missing contract: the import still succeeds, but the gap is reported — this is
// a missing file, not tampering.
func TestExportRecordsMissingPreservedOriginal(t *testing.T) {
	caseRoot, ledgerPath, uri := preservedBundleCase(t)
	if err := os.Remove(filepath.Join(caseRoot, filepath.FromSlash(uri))); err != nil {
		t.Fatal(err)
	}
	m, err := Export(caseRoot, ledgerPath, filepath.Join(t.TempDir(), "case.zip"))
	if err != nil {
		t.Fatalf("a missing original is not a digest conflict: %v", err)
	}
	if len(m.Missing) != 1 || m.Missing[0] != uri {
		t.Fatalf("missing = %v, want [%s]", m.Missing, uri)
	}
}

// writeSignedBundle writes a zip whose manifest hashes match the bytes written,
// i.e. a transfer that is internally consistent. The ledger inside can still
// disagree with the bytes, which is exactly the case Import must catch.
func writeSignedBundle(t *testing.T, zipPath string, files map[string][]byte) {
	t.Helper()
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	var m Manifest
	m.Version = Version
	for _, name := range names {
		sum := sha256.Sum256(files[name])
		m.Files = append(m.Files, File{Path: name, Size: int64(len(files[name])), SHA256: hex.EncodeToString(sum[:])})
	}
	mb, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}

	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, name := range names {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(files[name]); err != nil {
			t.Fatal(err)
		}
	}
	w, err := zw.Create(ManifestFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(mb); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
