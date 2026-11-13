package vulnlib

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kksty/tianwu/internal/evidence"
)

func preservedLibraryFixture(t *testing.T, body string) (*Library, string, evidence.Preserved) {
	t.Helper()
	l, err := Open(filepath.Join(t.TempDir(), "lib"))
	if err != nil {
		t.Fatal(err)
	}
	caseRoot := t.TempDir()
	src := filepath.Join(caseRoot, "in_1", "admin.html")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := evidence.Preserve(caseRoot, src)
	if err != nil {
		t.Fatal(err)
	}
	return l, caseRoot, p
}

// The library outlives the project, so its evidence copy must be the preserved
// object verbatim: a tampered source is refused (dropped), never archived as if
// it were the proof (F2/D150).
func TestKeepEvidenceRefusesTamperedPreservedObject(t *testing.T) {
	l, caseRoot, p := preservedLibraryFixture(t, "the admitted proof")
	if err := os.WriteFile(filepath.Join(caseRoot, filepath.FromSlash(p.URI)), []byte("tampered bytes!!"), 0o644); err != nil {
		t.Fatal(err)
	}

	kept, dropped := l.KeepEvidence("p_1", caseRoot, []string{p.URI})
	if len(kept) != 0 || len(dropped) != 1 || dropped[0] != p.URI {
		t.Fatalf("kept = %v dropped = %v, want the tampered object dropped", kept, dropped)
	}
	// No library file may exist for the refused object (empty parent dirs are
	// harmless; a copied byte is not).
	err := filepath.WalkDir(filepath.Join(l.root, EvidenceDir), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			t.Fatalf("library accepted bytes that failed the digest check: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Healthy preserved objects are copied verbatim, and a later hook call re-checks
// the library copy instead of trusting it blindly.
func TestKeepEvidenceVerifiesItsLibraryCopy(t *testing.T) {
	l, caseRoot, p := preservedLibraryFixture(t, "the admitted proof")

	kept, dropped := l.KeepEvidence("p_1", caseRoot, []string{p.URI})
	if len(kept) != 1 || len(dropped) != 0 {
		t.Fatalf("kept = %v dropped = %v", kept, dropped)
	}
	libPath, ok := l.EvidencePath(kept[0])
	if !ok {
		t.Fatalf("kept path %q does not resolve", kept[0])
	}
	if b, err := os.ReadFile(libPath); err != nil || string(b) != "the admitted proof" {
		t.Fatalf("library copy = %q err=%v", b, err)
	}

	// A tampered library copy is rebuilt from the still-verified preserved
	// object on the next call; it must not silently stay accepted.
	if err := os.WriteFile(libPath, []byte("library tampered!"), 0o644); err != nil {
		t.Fatal(err)
	}
	kept, dropped = l.KeepEvidence("p_1", caseRoot, []string{p.URI})
	if len(kept) != 1 || len(dropped) != 0 {
		t.Fatalf("second call kept = %v dropped = %v", kept, dropped)
	}
	if b, err := os.ReadFile(libPath); err != nil || string(b) != "the admitted proof" {
		t.Fatalf("library copy was not rebuilt: %q err=%v", b, err)
	}
}

// A legacy working-path URI has no digest, so the library copy keeps the old
// existence-only behaviour. This is the documented old-row limitation.
func TestKeepEvidenceStillCopiesLegacyPath(t *testing.T) {
	l, caseRoot, _ := preservedLibraryFixture(t, "legacy bytes")
	legacy := filepath.Join(caseRoot, "in_1", "legacy.txt")
	if err := os.WriteFile(legacy, []byte("legacy bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	kept, dropped := l.KeepEvidence("p_1", caseRoot, []string{"in_1/legacy.txt"})
	if len(kept) != 1 || len(dropped) != 0 {
		t.Fatalf("legacy copy kept = %v dropped = %v", kept, dropped)
	}
}
