package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/vulnlib"
)

// newVulnEvidenceHub builds a real vulnerability library on disk with one
// captured HTML file, and returns the hub that serves it. The handler is
// exercised directly (no mux) because it is the preview boundary under test.
func newVulnEvidenceHub(t *testing.T, body string) (*hub, string) {
	t.Helper()
	root := t.TempDir()
	lib, err := vulnlib.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	rel := "evidence/acme/in_x/resp.html"
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return &hub{vulns: lib}, rel
}

func getVulnEvidence(t *testing.T, h *hub, rel, query string, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/vulns/evidence/"+rel+query, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.serveVulnEvidence(rec, req)
	res := rec.Result()
	defer res.Body.Close()
	got, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res, got
}

// The vulnerability library's evidence copies are attacker-influenced bytes
// served from the console's own origin. A click must preview them, never make
// the browser execute captured HTML as a same-origin document, and never force a
// download; saving is the explicit ?download=1 action.
func TestServeVulnEvidencePreviewsInline(t *testing.T) {
	body := "<!doctype html><html><body><script>alert('same-origin')</script>证据</body></html>\n"
	h, rel := newVulnEvidenceHub(t, body)

	res, got := getVulnEvidence(t, h, rel, "", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if cd := res.Header.Get("Content-Disposition"); cd != "inline" {
		t.Fatalf("Content-Disposition = %q, want inline", cd)
	}
	if got := res.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := res.Header.Get("X-Frame-Options"); got != "SAMEORIGIN" {
		t.Fatalf("X-Frame-Options = %q, want SAMEORIGIN (PDF preview frame)", got)
	}
	if got := res.Header.Get("Content-Type"); got != "text/html" {
		t.Fatalf("Content-Type = %q, want text/html", got)
	}
	if got := res.Header.Get("Content-Security-Policy"); got != "sandbox" {
		t.Fatalf("Content-Security-Policy = %q, want sandbox", got)
	}
	if string(got) != body {
		t.Fatalf("body = %q, want the stored bytes", string(got))
	}
}

// A preview reads a bounded range of a large file; ServeFile must keep
// answering Range requests through this handler.
func TestServeVulnEvidenceSupportsRangePreview(t *testing.T) {
	h, rel := newVulnEvidenceHub(t, "0123456789abcdef")
	res, got := getVulnEvidence(t, h, rel, "", map[string]string{"Range": "bytes=0-9"})
	if res.StatusCode != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", res.StatusCode)
	}
	if string(got) != "0123456789" {
		t.Fatalf("body = %q, want the requested range", string(got))
	}
}

func TestServeVulnEvidenceDownloadsOnlyOnExplicitRequest(t *testing.T) {
	h, rel := newVulnEvidenceHub(t, "proof")
	res, _ := getVulnEvidence(t, h, rel, "?download=1", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	cd := res.Header.Get("Content-Disposition")
	if !strings.HasPrefix(cd, "attachment;") || !strings.Contains(cd, "resp.html") {
		t.Fatalf("Content-Disposition = %q, want an attachment named resp.html", cd)
	}
	if csp := res.Header.Get("Content-Security-Policy"); csp != "" {
		t.Fatalf("download response carries CSP %q; the sandbox is for inline preview only", csp)
	}
}

func TestServeVulnEvidenceRefusesUnknownAndEscapingPaths(t *testing.T) {
	h, _ := newVulnEvidenceHub(t, "proof")
	for _, rel := range []string{
		"evidence/acme/in_x/missing.html",
		"evidence/../../vulns.json",
		"vulns.json",
	} {
		res, got := getVulnEvidence(t, h, rel, "", nil)
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: status = %d, want 404 (%s)", rel, res.StatusCode, got)
		}
	}
}
