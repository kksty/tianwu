package evidence

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/model"
)

func TestPathResolvesInsideTheCase(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "in_1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "in_1", "a.html"), []byte("proof"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Path(root, model.Evidence{URI: "in_1/a.html"})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.ToSlash(p) != filepath.ToSlash(filepath.Join(root, "in_1", "a.html")) {
		t.Fatalf("path = %q", p)
	}
	b, err := Read(root, model.Evidence{URI: "in_1/a.html"})
	if err != nil || string(b) != "proof" {
		t.Fatalf("read = %q err=%v", b, err)
	}
}

// A URI that leaves the case root is not a missing file — it is the ledger
// pointing outside its own data, and the two must stay distinguishable
// (403 vs 404 at the API).
func TestPathRefusesEscape(t *testing.T) {
	root := t.TempDir()
	for _, uri := range []string{"../x", "in_1/../../x", filepath.Join(root, "abs.html")} {
		if _, err := Path(root, model.Evidence{URI: uri}); !errors.Is(err, ErrOutside) {
			t.Fatalf("Path(%q) err = %v, want ErrOutside", uri, err)
		}
	}
}

func TestPathReportsMissing(t *testing.T) {
	root := t.TempDir()
	for _, uri := range []string{"", "nope.html", "dir/"} {
		if _, err := Path(root, model.Evidence{URI: uri}); !errors.Is(err, ErrMissing) {
			t.Fatalf("Path(%q) err = %v, want ErrMissing", uri, err)
		}
	}
	// A directory is not an artifact, even though it exists.
	if err := os.MkdirAll(filepath.Join(root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Path(root, model.Evidence{URI: "dir"}); !errors.Is(err, ErrMissing) {
		t.Fatal("a directory must not resolve as evidence")
	}
}

// Windows artefacts a POSIX-style separator; both must name the same file.
func TestPathAcceptsBackslashes(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Path(root, model.Evidence{URI: "a.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Path(root, model.Evidence{URI: strings.ReplaceAll("a.txt", "/", "\\")}); err != nil {
		t.Fatalf("backslash URI refused: %v", err)
	}
}
