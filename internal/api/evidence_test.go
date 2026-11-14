package api

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/evidence"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// TestServeEvidenceDistinguishesEscapeFromMissing pins the two refusals the API
// must not merge: a URI outside the case is 403 (our own ledger is wrong), a
// vanished file is 404 (the run's bytes are gone).
func TestServeEvidenceDistinguishesEscapeFromMissing(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	outside, err := st.AddEvidence(model.Evidence{RunID: "r1", IntentID: "in_1", Name: "x", URI: "../outside.txt"})
	if err != nil {
		t.Fatal(err)
	}
	gone, err := st.AddEvidence(model.Evidence{RunID: "r1", IntentID: "in_1", Name: "gone.html", URI: "in_1/gone.html"})
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, nil, dir, filepath.Join(dir, "cases"))
	mux := http.NewServeMux()
	s.Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cases := []struct {
		id    string
		query string
		want  int
	}{
		{outside.ID, "", http.StatusForbidden},
		{outside.ID, "?download=1", http.StatusForbidden},
		{gone.ID, "", http.StatusNotFound},
		{gone.ID, "?download=1", http.StatusNotFound},
	}
	for _, c := range cases {
		res, err := http.Get(srv.URL + "/api/evidence/" + c.id + c.query)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != c.want {
			t.Fatalf("%s%s: status %d, want %d", c.id, c.query, res.StatusCode, c.want)
		}
	}
}

// newEvidenceTestServer wires the same surface main uses: a real ServeMux with
// the API routes, behind an httptest server, against a case root on disk.
func newEvidenceTestServer(t *testing.T) (*store.Store, *httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	caseRoot := filepath.Join(dir, "cases")
	if err := os.MkdirAll(caseRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, nil, dir, caseRoot)
	mux := http.NewServeMux()
	s.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return st, srv, caseRoot
}

// addEvidenceFile writes rel under the case root and registers the row that
// points at it, so tests exercise the real path resolution, not a fixture.
func addEvidenceFile(t *testing.T, st *store.Store, caseRoot, rel, name, mimeType string, body []byte) model.Evidence {
	t.Helper()
	abs := filepath.Join(caseRoot, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, body, 0o644); err != nil {
		t.Fatal(err)
	}
	preserved, err := evidence.Preserve(caseRoot, abs)
	if err != nil {
		t.Fatal(err)
	}
	e, err := st.AddEvidence(model.Evidence{
		RunID: "r1", IntentID: "in_1", Name: name, Mime: mimeType,
		Size: preserved.Size, URI: rel, Digest: preserved.Digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func getEvidence(t *testing.T, srv *httptest.Server, id, query string) (*http.Response, []byte) {
	t.Helper()
	res, err := http.Get(srv.URL + "/api/evidence/" + id + query)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res, body
}

// TestServeEvidencePreviewsHTMLInline is the ordinary click path: an HTML
// artifact (the ledger records it as application/octet-stream, as it does for
// most real rows) must preview as source, never as a same-origin document and
// never as a download.
func TestServeEvidencePreviewsHTMLInline(t *testing.T) {
	st, srv, caseRoot := newEvidenceTestServer(t)
	body := []byte("<!doctype html><html><body><script>alert('same-origin')</script>证据</body></html>\n")
	e := addEvidenceFile(t, st, caseRoot, "in_1/page.html", "page.html", "application/octet-stream", body)

	res, got := getEvidence(t, srv, e.ID, "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", res.StatusCode)
	}
	if cd := res.Header.Get("Content-Disposition"); cd != "inline" {
		t.Fatalf("Content-Disposition = %q, want inline", cd)
	}
	if ct := res.Header.Get("Content-Type"); ct != "text/html" {
		t.Fatalf("Content-Type = %q, want text/html", ct)
	}
	if csp := res.Header.Get("Content-Security-Policy"); csp != "sandbox" {
		t.Fatalf("Content-Security-Policy = %q, want sandbox", csp)
	}
	if xcto := res.Header.Get("X-Content-Type-Options"); xcto != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", xcto)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("preview body = %q, want the original bytes", got)
	}
}

// TestServeEvidencePreviewsPlainTextInline keeps the preview default honest for
// ordinary text: inline and nosniff, but no sandbox, which the console's own UI
// does not need and which would be a lie about the content type.
func TestServeEvidencePreviewsPlainTextInline(t *testing.T) {
	st, srv, caseRoot := newEvidenceTestServer(t)
	body := []byte("hello 证据\nsecond line\n")
	e := addEvidenceFile(t, st, caseRoot, "in_1/notes.txt", "notes.txt", "text/plain; charset=utf-8", body)

	res, got := getEvidence(t, srv, e.ID, "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", res.StatusCode)
	}
	if cd := res.Header.Get("Content-Disposition"); cd != "inline" {
		t.Fatalf("Content-Disposition = %q, want inline", cd)
	}
	if csp := res.Header.Get("Content-Security-Policy"); csp != "" {
		t.Fatalf("Content-Security-Policy = %q, plain text must not be sandboxed", csp)
	}
	if xcto := res.Header.Get("X-Content-Type-Options"); xcto != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", xcto)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("preview body = %q, want the original bytes", got)
	}
}

// TestServeEvidenceAllowsSameOriginFraming pins the one embedding the console
// needs: the preview sheet frames a PDF. The running instance wraps every
// response in X-Frame-Options: DENY, and this handler writes SAMEORIGIN into the
// same header map, so the frame is allowed while the console's own pages keep
// DENY. A download is never framed, so it must not relax the policy.
func TestServeEvidenceAllowsSameOriginFraming(t *testing.T) {
	st, srv, caseRoot := newEvidenceTestServer(t)
	body := []byte("%PDF-1.4\n1 0 obj<<>>endobj\n%%EOF\n")
	e := addEvidenceFile(t, st, caseRoot, "in_1/report.pdf", "report.pdf", "application/pdf", body)

	res, got := getEvidence(t, srv, e.ID, "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", res.StatusCode)
	}
	if xfo := res.Header.Get("X-Frame-Options"); xfo != "SAMEORIGIN" {
		t.Fatalf("X-Frame-Options = %q, want SAMEORIGIN (the sheet frames this PDF)", xfo)
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/pdf" {
		t.Fatalf("Content-Type = %q, want application/pdf", ct)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("preview body = %q, want the original bytes", got)
	}

	dl, _ := getEvidence(t, srv, e.ID, "?download=1")
	if xfo := dl.Header.Get("X-Frame-Options"); xfo != "" {
		t.Fatalf("download X-Frame-Options = %q, the attachment path must not relax framing", xfo)
	}
}

// TestServeEvidenceDownloadIsAttachment pins the explicit download path: only
// ?download=1 turns the same bytes into a save, named after the evidence row.
func TestServeEvidenceDownloadIsAttachment(t *testing.T) {
	st, srv, caseRoot := newEvidenceTestServer(t)
	body := []byte{0x89, 'P', 'N', 'G', 0x00, 0x1a, 0xff}
	e := addEvidenceFile(t, st, caseRoot, "in_1/artifact.png", "artifact.png", "image/png", body)

	res, got := getEvidence(t, srv, e.ID, "?download=1")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", res.StatusCode)
	}
	mediaType, params, err := mime.ParseMediaType(res.Header.Get("Content-Disposition"))
	if err != nil {
		t.Fatalf("parse Content-Disposition %q: %v", res.Header.Get("Content-Disposition"), err)
	}
	if mediaType != "attachment" {
		t.Fatalf("disposition type = %q, want attachment", mediaType)
	}
	if params["filename"] != e.Name {
		t.Fatalf("filename = %q, want %q", params["filename"], e.Name)
	}
	if xcto := res.Header.Get("X-Content-Type-Options"); xcto != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", xcto)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("download body = %x, want the original bytes", got)
	}
}

// TestServeEvidenceDownloadKeepsNonASCIIFilename pins RFC 5987 encoding: a name
// with CJK chars and spaces must round-trip through the header, which only
// works if it is emitted as filename* (no bare duplicate beside it).
func TestServeEvidenceDownloadKeepsNonASCIIFilename(t *testing.T) {
	st, srv, caseRoot := newEvidenceTestServer(t)
	name := "报告 截图 1.png"
	e := addEvidenceFile(t, st, caseRoot, "in_1/shot-1.png", name, "image/png", []byte("png"))

	res, _ := getEvidence(t, srv, e.ID, "?download=1")
	cd := res.Header.Get("Content-Disposition")
	mediaType, params, err := mime.ParseMediaType(cd)
	if err != nil {
		t.Fatalf("parse Content-Disposition %q: %v", cd, err)
	}
	if mediaType != "attachment" {
		t.Fatalf("disposition type = %q, want attachment", mediaType)
	}
	if params["filename"] != name {
		t.Fatalf("filename = %q, want %q", params["filename"], name)
	}
	if !strings.Contains(cd, "filename*=utf-8''") {
		t.Fatalf("Content-Disposition %q does not use RFC 5987 filename*", cd)
	}
}

// TestServeEvidenceSandboxesDocumentTypes covers the classification itself:
// the Mime and the extension are both untrusted inputs, and a document type
// from either one must pin Content-Type and sandbox the preview.
func TestServeEvidenceSandboxesDocumentTypes(t *testing.T) {
	cases := []struct {
		name   string
		mime   string
		rel    string
		wantCT string
	}{
		{"extension html", "application/octet-stream", "in_1/a.html", "text/html"},
		{"mime html", "text/html; charset=utf-8", "in_1/b.dat", "text/html"},
		{"extension xhtml", "", "in_1/c.xhtml", "application/xhtml+xml"},
		{"extension svg", "application/octet-stream", "in_1/d.svg", "image/svg+xml"},
		{"extension xml", "application/octet-stream", "in_1/e.xml", "application/xml"},
		{"mime text/xml", "text/xml", "in_1/f.bin", "text/xml"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st, srv, caseRoot := newEvidenceTestServer(t)
			e := addEvidenceFile(t, st, caseRoot, c.rel, filepath.Base(c.rel), c.mime, []byte("<x/>"))
			res, _ := getEvidence(t, srv, e.ID, "")
			if res.StatusCode != http.StatusOK {
				t.Fatalf("status %d, want 200", res.StatusCode)
			}
			if ct := res.Header.Get("Content-Type"); ct != c.wantCT {
				t.Fatalf("Content-Type = %q, want %q", ct, c.wantCT)
			}
			if csp := res.Header.Get("Content-Security-Policy"); csp != "sandbox" {
				t.Fatalf("Content-Security-Policy = %q, want sandbox", csp)
			}
			if cd := res.Header.Get("Content-Disposition"); cd != "inline" {
				t.Fatalf("Content-Disposition = %q, want inline", cd)
			}
		})
	}
}

// TestFactDetailExposesRefs is the operator-facing half of D44: a fact cited as
// a URL (or a vanished file) has no evidence rows, so the only way a human can
// see what it was based on is the fact's refs.
func TestFactDetailExposesRefs(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	f, err := st.AddFact(model.Fact{
		IntentID: "in_1", Kind: "vuln", Title: "只在线上", Status: "unverified",
		Refs: []model.Ref{
			{Scheme: model.RefURL, Locator: "https://target/admin"},
			{Scheme: model.RefArtifact, Locator: "sha256:abc"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: st}
	mux := http.NewServeMux()
	s.Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	res, err := http.Get(srv.URL + "/api/facts/" + f.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body struct {
		Fact model.Fact `json:"fact"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Fact.Refs) != 2 {
		t.Fatalf("refs = %+v", body.Fact.Refs)
	}
	if body.Fact.Refs[0].Scheme != model.RefURL || body.Fact.Refs[0].Locator != "https://target/admin" {
		t.Fatalf("first ref = %+v", body.Fact.Refs[0])
	}
	if len(body.Fact.EvidenceIDs) != 0 {
		t.Fatalf("a ref must not become evidence: %v", body.Fact.EvidenceIDs)
	}
}
