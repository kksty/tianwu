// Package evidence turns an evidence row into bytes, and is the only place that
// decides whether it can.
//
// Three readers ask that question today — the API (stream the original), the
// report (copy the originals it cites), the bundle (list the originals that are
// gone) — and before this package each answered it with its own `os.Stat`, so a
// vanished file could mean 404, a silently thinner report, or a manifest entry.
// When the artifact store lands (docs/05, D44) it becomes one more source behind
// this same function, and the three readers learn nothing new.
//
// Since D150 admission also preserves the exact bytes: Preserve writes them into
// an immutable content-addressed store inside the case root and the row records
// the SHA-256. A row whose URI is a preserved object is read through Read/Verify/
// CopyTo, which re-hash the bytes and fail explicitly on mismatch instead of
// serving whatever the working path holds now. Rows written before D150 have no
// Digest; they stay readable (the URI is all we ever recorded for them) and are
// a documented limitation, not silently upgraded.
package evidence

import (
	"errors"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/kksty/tianwu/internal/model"
)

var (
	// ErrOutside means the recorded URI does not resolve inside the case root.
	// It is a refusal, not a missing file: the ledger's own data is pointing
	// somewhere it never should. 403, never 404.
	ErrOutside = errors.New("证据路径越界")
	// ErrMissing means the row exists but its bytes do not: the file was
	// deleted, the case was partially copied, or the node never returned it.
	ErrMissing = errors.New("证据文件已不存在")
	// ErrDigestMismatch means the bytes a row points at no longer hash to the
	// digest the row recorded. The preserved copy was altered (or the row's
	// digest was), so serving these bytes as that proof would be a lie. It is a
	// refusal, not a missing file: the operator must restore the original, not
	// accept the new bytes.
	ErrDigestMismatch = errors.New("证据内容与登记摘要不符")
	// ErrCorrupt means the content-addressed store itself disagrees with its own
	// name: an object saved as <sha256> no longer hashes to <sha256>. Admission
	// refuses to reuse such an object because "same digest" is the whole reason
	// reuse is allowed.
	ErrCorrupt = errors.New("证据保全副本已损坏")
)

// Path resolves row.URI to an existing file inside caseRoot.
//
// URIs are stored slash-separated and relative to the case root (docs/02), so
// the containment check is the same one the report and the API need: anything
// that leaves the root is refused outright.
func Path(caseRoot string, row model.Evidence) (string, error) {
	uri := strings.TrimSpace(row.URI)
	if uri == "" {
		return "", ErrMissing
	}
	native := filepath.FromSlash(strings.ReplaceAll(uri, "\\", "/"))
	if filepath.IsAbs(native) {
		return "", ErrOutside
	}
	abs := filepath.Join(caseRoot, native)
	rel, err := filepath.Rel(caseRoot, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", ErrOutside
	}
	st, err := os.Stat(abs)
	if err != nil || st.IsDir() {
		return "", ErrMissing
	}
	return abs, nil
}

// Read returns the bytes of one evidence row, verifying the recorded digest.
//
// A row with no Digest (written before D150) is read as-is: URI is all that was
// recorded, so there is nothing to check the bytes against. A row whose URI is
// a preserved object but whose Digest is missing is impossible through the
// ledger API and is refused rather than served unverified.
func Read(caseRoot string, row model.Evidence) ([]byte, error) {
	path, err := Path(caseRoot, row)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Path already stat'ed, so this is a delete between the two calls;
			// it keeps ErrMissing's meaning instead of leaking an os error.
			return nil, ErrMissing
		}
		return nil, err
	}
	if err := checkDigest(row, b); err != nil {
		return nil, err
	}
	return b, nil
}

// documentTypes maps the media types a browser renders as a document, not as an
// opaque blob, to the Content-Type we pin for them. They are exactly the types
// whose preview can execute or interpret markup (HTML, XHTML, SVG, XML), so
// they are also the ones that need CSP sandbox when served inline.
var documentTypes = map[string]string{
	"text/html":             "text/html",
	"application/xhtml+xml": "application/xhtml+xml",
	"image/svg+xml":         "image/svg+xml",
	"text/xml":              "text/xml",
	"application/xml":       "application/xml",
}

// DocumentType reports the type to serve an evidence artifact as when a browser
// would treat its bytes as an executable document.
//
// Both sources count, because both are what a browser may act on: the media
// type is what the executor declared (most real rows say
// application/octet-stream, so it alone would miss HTML files), the extension
// is what ServeFile would derive on its own. A document type from either one is
// enough to be unsafe inline.
//
// This is shared by the project evidence endpoint and the vulnerability
// library's evidence copies: both serve attacker-influenced bytes from the
// console's own origin, so both need the same answer.
func DocumentType(mediaType, filename string) (string, bool) {
	if parsed, _, err := mime.ParseMediaType(strings.TrimSpace(mediaType)); err == nil {
		if ct, ok := documentTypes[strings.ToLower(parsed)]; ok {
			return ct, true
		}
	}
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".html", ".htm":
		return "text/html", true
	case ".xhtml":
		return "application/xhtml+xml", true
	case ".svg":
		return "image/svg+xml", true
	case ".xml":
		return "application/xml", true
	}
	return "", false
}
