package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/model"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func sha256Hex(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// Admission must leave an immutable, content-addressed copy: the URI is the
// SHA-256 of the admitted bytes and re-admitting identical bytes reuses the
// object instead of writing a second one.
func TestPreserveIsContentAddressedAndReusable(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "in_x", "proof.txt")
	writeFile(t, src, "admiralty-proof")

	p, err := Preserve(root, src)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256Hex("admiralty-proof")
	if p.Digest != want || p.URI != ".evidence/objects/"+want || p.Size != int64(len("admiralty-proof")) {
		t.Fatalf("preserved = %+v, want digest %s", p, want)
	}
	objectPath := filepath.Join(root, filepath.FromSlash(p.URI))
	if b, err := os.ReadFile(objectPath); err != nil || string(b) != "admiralty-proof" {
		t.Fatalf("preserved object = %q err=%v", b, err)
	}

	again, err := Preserve(root, src)
	if err != nil {
		t.Fatalf("re-admitting identical bytes: %v", err)
	}
	if again != p {
		t.Fatalf("re-admission = %+v, want %+v", again, p)
	}

	if digest, ok := ObjectDigest(p.URI); !ok || digest != want {
		t.Fatalf("ObjectDigest(%q) = %q, %v", p.URI, digest, ok)
	}
	if _, ok := ObjectDigest("in_x/proof.txt"); ok {
		t.Fatal("a plain working path must not be mistaken for a preserved object")
	}
}

// Two different contents admitted from the same working path are two objects.
// This is the F2 failure: the path is overwritten between admissions, so a row
// that pointed at it would silently change meaning.
func TestPreserveKeepsDistinctContentsApart(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "in_x", "response.txt")
	writeFile(t, src, "FIRST-CONTENT")
	first, err := Preserve(root, src)
	if err != nil {
		t.Fatal(err)
	}

	// Same length on purpose: incremental digest checks are the only way to
	// notice the rewrite, a size check is not.
	writeFile(t, src, "OTHER-CONTENT")
	second, err := Preserve(root, src)
	if err != nil {
		t.Fatal(err)
	}
	if first.URI == second.URI || first.Digest == second.Digest {
		t.Fatalf("two contents collapsed into one object: %+v %+v", first, second)
	}
	read := func(p Preserved) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p.URI)))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if read(first) != "FIRST-CONTENT" || read(second) != "OTHER-CONTENT" {
		t.Fatal("objects do not hold the bytes admitted for them")
	}
}

// Reuse is only sound because the object's name is its digest. A tampered
// object must stop the next admission instead of being served as the proof.
func TestPreserveRefusesTamperedObjectOnReuse(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "in_x", "proof.txt")
	writeFile(t, src, "the original bytes")
	p, err := Preserve(root, src)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, filepath.FromSlash(p.URI)), "tampered")

	if _, err := Preserve(root, src); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Preserve over a tampered object: err = %v, want ErrCorrupt", err)
	}
}

// A digested row must verify on read; a mismatch is a distinct refusal, not a
// missing file and not "here are whatever bytes are on disk now".
func TestReadVerifiesTheRecordedDigest(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "in_x", "proof.html")
	writeFile(t, src, "<html>original</html>")
	p, err := Preserve(root, src)
	if err != nil {
		t.Fatal(err)
	}
	row := model.Evidence{ID: "e_1", URI: p.URI, Digest: p.Digest}
	if b, err := Read(root, row); err != nil || string(b) != "<html>original</html>" {
		t.Fatalf("Read = %q err=%v", b, err)
	}

	writeFile(t, filepath.Join(root, filepath.FromSlash(p.URI)), "<html>tampered</html>")
	_, err = Read(root, row)
	if !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("Read after tampering: err = %v, want ErrDigestMismatch", err)
	}
	for _, want := range []string{"e_1", p.URI} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("digest error must name the evidence id/file %q: %v", want, err)
		}
	}
}

// Old rows recorded only a URI. They keep working as a documented limitation;
// they are not silently treated as preserved, and a preserved address without a
// digest (an impossible row through the store API) is refused.
func TestLegacyRowsWithoutDigestStillRead(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "in_1", "legacy.txt"), "legacy bytes")
	b, err := Read(root, model.Evidence{ID: "e_old", URI: "in_1/legacy.txt"})
	if err != nil || string(b) != "legacy bytes" {
		t.Fatalf("legacy Read = %q err=%v", b, err)
	}

	// A preserved address without a digest is impossible through the store API;
	// if a hand-edited ledger contains one, it is refused rather than served
	// unverified.
	orphanURI := ".evidence/objects/" + strings.Repeat("a", 64)
	writeFile(t, filepath.Join(root, filepath.FromSlash(orphanURI)), "orphaned bytes")
	_, err = Read(root, model.Evidence{ID: "e_bad", URI: orphanURI})
	if !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("preserved URI without digest: err = %v, want ErrDigestMismatch", err)
	}
}

// A deleted original keeps its exact old meaning at the read boundary, and a
// failed verified copy leaves nothing behind for a report to cite.
func TestMissingStaysMissingAndCopyFailureLeavesNoFile(t *testing.T) {
	root := t.TempDir()
	row := model.Evidence{ID: "e_1", URI: "in_1/gone.txt", Digest: sha256Hex("x")}
	if _, err := Read(root, row); !errors.Is(err, ErrMissing) {
		t.Fatalf("Read(missing) err = %v, want ErrMissing", err)
	}
	if err := Verify(root, row); !errors.Is(err, ErrMissing) {
		t.Fatalf("Verify(missing) err = %v, want ErrMissing", err)
	}
	dst := filepath.Join(root, "copy.txt")
	if err := CopyTo(root, row, dst); !errors.Is(err, ErrMissing) {
		t.Fatalf("CopyTo(missing) err = %v, want ErrMissing", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatal("a failed copy must not leave a file to be cited")
	}

	src := filepath.Join(root, "in_x", "proof.txt")
	writeFile(t, src, "real bytes")
	p, err := Preserve(root, src)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, filepath.FromSlash(p.URI)), "altered")
	dst = filepath.Join(root, "copy2.txt")
	if err := CopyTo(root, model.Evidence{ID: "e_2", URI: p.URI, Digest: p.Digest}, dst); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("CopyTo(tampered) err = %v, want ErrDigestMismatch", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatal("a tampered copy must be removed, not left for the report")
	}
}

// SourceURI is provenance: case-relative for files in the case, the path as
// given for imports from elsewhere. It is never the reader's address.
func TestOriginPath(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "in_x", "raw", "proof.txt")
	writeFile(t, inside, "x")
	if got := OriginPath(root, inside); got != "in_x/raw/proof.txt" {
		t.Fatalf("OriginPath(inside) = %q", got)
	}
	outside := filepath.Join(t.TempDir(), "capture.har")
	writeFile(t, outside, "x")
	if got := OriginPath(root, outside); filepath.ToSlash(got) != filepath.ToSlash(outside) {
		t.Fatalf("OriginPath(outside) = %q, want %q", got, outside)
	}
}

// A directory is not an artifact and must not become one by preservation.
func TestPreserveRefusesDirectory(t *testing.T) {
	root := t.TempDir()
	if _, err := Preserve(root, filepath.Join(root, "in_x")); err == nil {
		t.Fatal("a directory must not be preserved as evidence")
	}
	if _, err := Preserve(root, filepath.Join(root, "nope.txt")); err == nil {
		t.Fatal("a missing file must not be preserved")
	}
}
