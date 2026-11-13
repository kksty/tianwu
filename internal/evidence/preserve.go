package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/kksty/tianwu/internal/model"
)

// objectDir is the immutable store inside the case root. Addresses are the
// lowercase SHA-256 of the bytes, so the same content admitted twice is one
// object and no admission can ever replace another admission's bytes.
const objectDir = ".evidence/objects"

// Preserved is the row metadata that makes an immutable copy the evidence's
// address: URI opens the copy, Digest lets every reader prove the bytes are the
// ones admitted, and Size is the preserved size (equal to the source's).
type Preserved struct {
	URI    string
	Digest string
	Size   int64
}

// Preserve copies src into the case's content-addressed store and returns the
// metadata for the evidence row.
//
// The copy is written to a temp file in the same directory, hashed on the way
// in, and renamed to its digest. An existing object is never overwritten: reuse
// re-hashes the stored bytes first, so a tampered object becomes ErrCorrupt
// instead of being served as if it were the admitted proof.
func Preserve(caseRoot, src string) (Preserved, error) {
	if strings.TrimSpace(caseRoot) == "" {
		return Preserved{}, fmt.Errorf("证据保全需要 case 根目录")
	}
	st, err := os.Stat(src)
	if err != nil {
		return Preserved{}, fmt.Errorf("读取证据原件失败：%w", err)
	}
	if st.IsDir() {
		return Preserved{}, fmt.Errorf("证据原件是目录：%s", src)
	}

	dir := filepath.Join(caseRoot, filepath.FromSlash(objectDir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Preserved{}, err
	}
	in, err := os.Open(src)
	if err != nil {
		return Preserved{}, fmt.Errorf("读取证据原件失败：%w", err)
	}
	defer in.Close()

	tmp, err := os.CreateTemp(dir, ".incoming-")
	if err != nil {
		return Preserved{}, err
	}
	tmpName := tmp.Name()
	// cleanup covers every failure before the rename; after the rename the temp
	// path no longer exists and Remove is a harmless no-op.
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}

	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), in)
	if err != nil {
		cleanup()
		return Preserved{}, fmt.Errorf("保全证据原件失败：%w", err)
	}
	// Sync before publishing: the object is the case's proof, and a later crash
	// must not leave a correctly named object with truncated bytes.
	if err := tmp.Sync(); err != nil {
		cleanup()
		return Preserved{}, err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return Preserved{}, err
	}

	digest := hex.EncodeToString(h.Sum(nil))
	p := Preserved{URI: objectDir + "/" + digest, Digest: digest, Size: n}
	target := filepath.Join(dir, digest)

	if _, err := os.Stat(target); err == nil {
		// Same content admitted again: keep the object, but prove it still is
		// these bytes. Reuse is only sound because the address is the digest.
		_ = os.Remove(tmpName)
		if err := verifyFileDigest(target, digest); err != nil {
			return Preserved{}, err
		}
		return p, nil
	} else if !os.IsNotExist(err) {
		_ = os.Remove(tmpName)
		return Preserved{}, err
	}

	if err := os.Rename(tmpName, target); err != nil {
		// Another admission of the same content may have published the object
		// between our Stat and Rename (Rename cannot replace it, especially on
		// Windows). Accept the winner only after re-hashing it.
		if _, statErr := os.Stat(target); statErr == nil {
			_ = os.Remove(tmpName)
			if verifyErr := verifyFileDigest(target, digest); verifyErr != nil {
				return Preserved{}, verifyErr
			}
			return p, nil
		}
		_ = os.Remove(tmpName)
		return Preserved{}, err
	}
	return p, nil
}

// ObjectDigest reports the digest encoded in a preserved URI, and whether the
// URI is in the content-addressed store at all. Callers that only have a URI
// (the vulnerability library's copy hook, the ledger's structural checks) can
// still verify the bytes without the original row.
func ObjectDigest(uri string) (string, bool) {
	u := strings.TrimSpace(strings.ReplaceAll(uri, "\\", "/"))
	prefix := objectDir + "/"
	if !strings.HasPrefix(u, prefix) {
		return "", false
	}
	name := strings.TrimPrefix(u, prefix)
	if len(name) != 64 || strings.Contains(name, "/") {
		return "", false
	}
	if _, err := hex.DecodeString(name); err != nil {
		return "", false
	}
	return strings.ToLower(name), true
}

// OriginPath is the provenance string recorded in Evidence.SourceURI: slash
// separated and case-relative when the source is inside the case, otherwise the
// path as given. It is an address for a human, never the reader's address; the
// reader opens URI.
func OriginPath(caseRoot, src string) string {
	src = strings.TrimSpace(src)
	if src == "" {
		return ""
	}
	if caseRoot != "" {
		if rel, err := filepath.Rel(caseRoot, src); err == nil &&
			rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(src)
}

// Verify re-hashes the bytes behind a row and checks them against Digest. Rows
// without a digest are checked only for existence (the documented old-row
// boundary); rows whose URI is a preserved object without a digest are refused
// because the store's invariant says that state cannot be legitimate.
func Verify(caseRoot string, row model.Evidence) error {
	path, err := Path(caseRoot, row)
	if err != nil {
		return err
	}
	if strings.TrimSpace(row.Digest) == "" {
		if _, preserved := ObjectDigest(row.URI); preserved {
			return fmt.Errorf("%w：证据 %s 指向保全副本却没有摘要，无法证明内容", ErrDigestMismatch, evidenceLabel(row))
		}
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrMissing
		}
		return err
	}
	defer f.Close()
	got, err := hashReader(f)
	if err != nil {
		return err
	}
	return compareDigest(row, got)
}

// CopyTo copies the verified bytes behind a row to dst, so report/export/bundle
// copy paths cannot accept bytes that fail the digest check. On any failure dst
// is removed: a half-written or wrong copy must never be cited as the proof.
func CopyTo(caseRoot string, row model.Evidence, dst string) error {
	path, err := Path(caseRoot, row)
	if err != nil {
		return err
	}
	if strings.TrimSpace(row.Digest) == "" {
		if _, preserved := ObjectDigest(row.URI); preserved {
			return fmt.Errorf("%w：证据 %s 指向保全副本却没有摘要，无法证明内容", ErrDigestMismatch, evidenceLabel(row))
		}
	}
	in, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrMissing
		}
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(out, h), in)
	if copyErr == nil {
		copyErr = out.Sync()
	}
	if closeErr := out.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		_ = os.Remove(dst)
		return copyErr
	}
	if n != st.Size() {
		_ = os.Remove(dst)
		return fmt.Errorf("证据复制不完整：%s 应为 %d 字节，实际 %d", filepath.Base(path), st.Size(), n)
	}
	if strings.TrimSpace(row.Digest) != "" {
		if err := compareDigest(row, hex.EncodeToString(h.Sum(nil))); err != nil {
			_ = os.Remove(dst)
			return err
		}
	}
	return nil
}

// checkDigest is Read's half of the contract: compare bytes already in memory.
func checkDigest(row model.Evidence, b []byte) error {
	digest := strings.TrimSpace(row.Digest)
	if digest == "" {
		// Legacy boundary: rows admitted before D150 recorded only URI, so the
		// bytes there are whatever the working path holds; they cannot be
		// retroactively proven and no startup scan pretends otherwise.
		if _, preserved := ObjectDigest(row.URI); preserved {
			return fmt.Errorf("%w：证据 %s 指向保全副本却没有摘要，无法证明内容", ErrDigestMismatch, evidenceLabel(row))
		}
		return nil
	}
	sum := sha256.Sum256(b)
	return compareDigest(row, hex.EncodeToString(sum[:]))
}

func compareDigest(row model.Evidence, got string) error {
	want := strings.ToLower(strings.TrimSpace(row.Digest))
	if strings.EqualFold(got, want) {
		return nil
	}
	return fmt.Errorf("%w：证据 %s 登记 %s，实际 %s", ErrDigestMismatch, evidenceLabel(row), want, got)
}

func verifyFileDigest(path, digest string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	got, err := hashReader(f)
	if err != nil {
		return err
	}
	if !strings.EqualFold(got, digest) {
		return fmt.Errorf("%w：保全副本 %s 实际摘要 %s，路径名登记 %s", ErrCorrupt, filepath.Base(path), got, digest)
	}
	return nil
}

func hashReader(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// evidenceLabel names a row in an error the way an operator can act on it: the
// ledger id and the address that failed. Exports and reads must name the file,
// not just say "one proof is bad".
func evidenceLabel(row model.Evidence) string {
	switch {
	case row.ID != "" && row.URI != "":
		return row.ID + "（" + row.URI + "）"
	case row.ID != "":
		return row.ID
	case row.URI != "":
		return row.URI
	default:
		return "（无 id/uri）"
	}
}
