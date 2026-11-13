package evidence

import "testing"

// The document-type decision is the security boundary of evidence previews:
// anything a browser would render as a document instead of an opaque blob must
// be pinned and CSP-sandboxed when served inline. Both the recorded media type
// and the file extension count, because most real rows are recorded as
// application/octet-stream while the file itself is HTML.
func TestDocumentTypeRecognizesBrowserDocuments(t *testing.T) {
	cases := []struct {
		name      string
		mediaType string
		filename  string
		want      string
		ok        bool
	}{
		{"declared html", "text/html", "capture.bin", "text/html", true},
		{"html extension despite octet-stream", "application/octet-stream", "page.html", "text/html", true},
		{"htm extension", "", "page.htm", "text/html", true},
		{"xhtml", "", "page.xhtml", "application/xhtml+xml", true},
		{"svg source", "image/svg+xml", "icon.bin", "image/svg+xml", true},
		{"svg extension", "", "icon.svg", "image/svg+xml", true},
		{"xml extension", "", "soap.xml", "application/xml", true},
		{"text media type", "text/xml", "soap.bin", "text/xml", true},
		{"plain text stays opaque-safe", "text/plain", "notes.txt", "", false},
		{"json stays opaque-safe", "application/json", "body.json", "", false},
		{"image stays opaque-safe", "image/png", "shot.png", "", false},
		{"no type, no extension", "", "blob", "", false},
		{"media type with parameters", "text/html; charset=utf-8", "capture.bin", "text/html", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := DocumentType(c.mediaType, c.filename)
			if ok != c.ok || got != c.want {
				t.Fatalf("DocumentType(%q, %q) = %q, %v; want %q, %v", c.mediaType, c.filename, got, ok, c.want, c.ok)
			}
		})
	}
}
