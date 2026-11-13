package bundle

import (
	"archive/zip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedCase builds a case dir with one evidence file, one report dir and a
// ledger that points at both.
func seedCase(t *testing.T) (caseRoot, ledgerPath string) {
	t.Helper()
	base := t.TempDir()
	caseRoot = filepath.Join(base, "cases")
	if err := os.MkdirAll(filepath.Join(caseRoot, "in_1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(caseRoot, "reports", "rp_1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(caseRoot, "in_1", "admin.html"), []byte("HTTP/1.1 200 OK"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(caseRoot, "reports", "rp_1", "report.json"), []byte(`{"id":"rp_1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// .tw must not travel: it is the handoff channel, not a product.
	if err := os.MkdirAll(filepath.Join(caseRoot, "in_1", ".tw"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(caseRoot, "in_1", ".tw", "facts.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ledgerPath = filepath.Join(base, "ledger.json")
	ledger := `{"version":1,"facts":[{"id":"f_1"}],
	 "evidence":[{"id":"e_1","uri":"in_1/admin.html"},{"id":"e_2","uri":"in_1/missing.html"}]}`
	if err := os.WriteFile(ledgerPath, []byte(ledger), 0o644); err != nil {
		t.Fatal(err)
	}
	return caseRoot, ledgerPath
}

func TestExportImportRoundTrip(t *testing.T) {
	caseRoot, ledgerPath := seedCase(t)
	zipPath := filepath.Join(t.TempDir(), "case.zip")

	m, err := Export(caseRoot, ledgerPath, zipPath)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if m.Counts.Facts != 1 || m.Counts.Evidence != 2 || m.Counts.Reports != 1 {
		t.Fatalf("counts = %+v", m.Counts)
	}
	if len(m.Missing) != 1 || m.Missing[0] != "in_1/missing.html" {
		t.Fatalf("missing evidence must be recorded, got %v", m.Missing)
	}

	dest := filepath.Join(t.TempDir(), "restored")
	got, err := Import(zipPath, dest)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(got.Files) != len(m.Files) {
		t.Fatalf("files = %d, want %d", len(got.Files), len(m.Files))
	}
	for _, p := range []string{
		filepath.Join(dest, "case", "in_1", "admin.html"),
		filepath.Join(dest, "case", "reports", "rp_1", "report.json"),
		filepath.Join(dest, "ledger.json"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("restored file missing: %s", p)
		}
	}
	if _, err := os.Stat(filepath.Join(dest, "case", "in_1", ".tw", "facts.jsonl")); err == nil {
		t.Fatal(".tw must not be exported")
	}
}

func TestExportOmitsPrivateInputsAndIncludesSelectedNativeEvidence(t *testing.T) {
	root, ledger := seedCase(t)
	for _, rel := range []string{".access/ac_test/material.json", ".native/result/raw.json", "in_1/.tw/access/ac_test.json", "in_1/native-evidence/selected.json"} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	m, err := Export(root, ledger, filepath.Join(t.TempDir(), "case.zip"))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, file := range m.Files {
		if strings.Contains(file.Path, "/.access/") || strings.Contains(file.Path, "/.native/") || strings.Contains(file.Path, "/.tw/") {
			t.Fatalf("private input exported: %s", file.Path)
		}
		if strings.HasSuffix(file.Path, "/native-evidence/selected.json") {
			found = true
		}
	}
	if !found {
		t.Fatal("selected native evidence missing from bundle")
	}
}

// corrupt rewrites one entry of the zip without updating the manifest.
func corrupt(t *testing.T, zipPath string) {
	t.Helper()
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	type entry struct {
		name string
		body []byte
	}
	var items []entry
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(f.Name, "admin.html") {
			body = []byte("tampered")
		}
		items = append(items, entry{f.Name, body})
	}
	zr.Close()

	out, err := os.Create(zipPath + ".tmp")
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(out)
	for _, it := range items {
		w, err := zw.Create(it.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(it.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(zipPath+".tmp", zipPath); err != nil {
		t.Fatal(err)
	}
}

func TestImportRejectsCorruptedBundle(t *testing.T) {
	caseRoot, ledgerPath := seedCase(t)
	zipPath := filepath.Join(t.TempDir(), "case.zip")
	if _, err := Export(caseRoot, ledgerPath, zipPath); err != nil {
		t.Fatal(err)
	}
	corrupt(t, zipPath)

	dest := filepath.Join(t.TempDir(), "restored")
	if _, err := Import(zipPath, dest); err == nil {
		t.Fatal("a tampered entry must be rejected")
	}
	if _, err := os.Stat(dest); err == nil {
		t.Fatal("a rejected import must not leave a directory behind")
	}
}

func TestImportRejectsEscapingEntry(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "evil.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	manifest, _ := json.Marshal(Manifest{Version: Version, Files: []File{{Path: "../evil.txt", Size: 1, SHA256: "x"}}})
	w, _ := zw.Create(ManifestFile)
	_, _ = w.Write(manifest)
	w2, _ := zw.Create("../evil.txt")
	_, _ = w2.Write([]byte("x"))
	_ = zw.Close()
	_ = f.Close()

	if _, err := Import(zipPath, filepath.Join(dir, "out")); err == nil {
		t.Fatal("an entry escaping the destination must be rejected")
	}
}

func TestSafeName(t *testing.T) {
	for _, bad := range []string{"../x", "/abs", "C:/x", "a/../../b", ""} {
		if _, err := safeName(bad); err == nil {
			t.Fatalf("safeName(%q) must fail", bad)
		}
	}
	if got, err := safeName("case/in_1/a.html"); err != nil || got != "case/in_1/a.html" {
		t.Fatalf("safeName kept = %q err = %v", got, err)
	}
}
