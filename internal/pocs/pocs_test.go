package pocs

import (
	"os"
	"path/filepath"
	"testing"
)

const sample = `id: CVE-2021-44228

info:
  name: Log4Shell
  severity: critical
  tags: cve,log4j,rce

http:
  - method: GET
    path:
      - "{{BaseURL}}/api"
`

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseFileReadsNucleiSchema(t *testing.T) {
	p := write(t, t.TempDir(), "log4j.yaml", sample)
	poc, err := parseFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if poc.ID != "CVE-2021-44228" || poc.Name != "Log4Shell" || poc.Severity != "critical" {
		t.Fatalf("parsed = %+v", poc)
	}
	if poc.Tags != "cve,log4j,rce" {
		t.Fatalf("tags = %q", poc.Tags)
	}
}

func TestListSkipsNonYAMLAndKeepsBrokenOnes(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.yaml", sample)
	write(t, dir, "b.yml", "id: simple\ninfo:\n  name: Simple\n  severity: low\n")
	write(t, dir, "readme.txt", "not a template")
	write(t, dir, "broken.yaml", "no: id\n")
	s := New(dir)
	list := s.List()
	if len(list) != 3 {
		t.Fatalf("list = %+v", list)
	}
	// A file without id is still visible, keyed by file name: it must not
	// disappear from the console just because it is malformed.
	found := false
	for _, p := range list {
		if p.ID == "broken" {
			found = true
		}
	}
	if !found {
		t.Fatalf("broken template missing from list: %+v", list)
	}
}

func TestImportFileAndDirectory(t *testing.T) {
	src := t.TempDir()
	write(t, src, "x/log4j.yaml", sample)
	write(t, src, "x/simple.yml", "id: simple\ninfo:\n  name: Simple\n  severity: low\n")
	write(t, src, "x/noid.yaml", "info:\n  name: NoId\n")
	write(t, src, "x/notes.md", "# not a template")

	dst := t.TempDir()
	s := New(dst)
	res, err := s.Import(src)
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported != 2 || res.Skipped != 1 {
		t.Fatalf("result = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(dst, "CVE-2021-44228.yaml")); err != nil {
		t.Fatalf("template not copied: %v", err)
	}
	if list := s.List(); len(list) != 2 {
		t.Fatalf("stored = %+v", list)
	}
}

func TestImportOverwritesSameID(t *testing.T) {
	src := t.TempDir()
	f := write(t, src, "log4j.yaml", sample)
	dst := t.TempDir()
	s := New(dst)

	if _, err := s.Import(f); err != nil {
		t.Fatal(err)
	}
	updated := "id: CVE-2021-44228\ninfo:\n  name: Log4Shell v2\n  severity: high\n"
	if err := os.WriteFile(f, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Import(f); err != nil {
		t.Fatal(err)
	}
	list := s.List()
	if len(list) != 1 || list[0].Name != "Log4Shell v2" || list[0].Severity != "high" {
		t.Fatalf("update did not replace: %+v", list)
	}
}

func TestImportRejectsBadID(t *testing.T) {
	src := t.TempDir()
	f := write(t, src, "bad.yaml", "id: ../escape\ninfo:\n  name: Bad\n")
	s := New(t.TempDir())
	res, err := s.Import(f)
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported != 0 || res.Skipped != 1 {
		t.Fatalf("result = %+v", res)
	}
}

func TestImportMissingPath(t *testing.T) {
	s := New(t.TempDir())
	if _, err := s.Import(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("missing path must error")
	}
}

// The console and every dispatch read List repeatedly; the cached parse result
// must still follow adds, edits and deletes made outside Import (operators edit
// the directory by hand).
func TestListReflectsDirectoryChanges(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.yaml", sample)
	s := New(dir)

	if got := s.List(); len(got) != 1 || got[0].ID != "CVE-2021-44228" {
		t.Fatalf("initial list = %+v", got)
	}

	write(t, dir, "b.yaml", "id: simple\ninfo:\n  name: Simple Check\n  severity: low\n")
	got := s.List()
	if len(got) != 2 || got[0].ID != "CVE-2021-44228" || got[1].ID != "simple" {
		t.Fatalf("added template missing or unsorted: %+v", got)
	}

	write(t, dir, "a.yaml", "id: CVE-2021-44228\ninfo:\n  name: Log4Shell v2\n  severity: high\n")
	got = s.List()
	if got[0].Name != "Log4Shell v2" || got[0].Severity != "high" {
		t.Fatalf("edited template not re-read: %+v", got[0])
	}

	if err := os.Remove(filepath.Join(dir, "b.yaml")); err != nil {
		t.Fatal(err)
	}
	if got := s.List(); len(got) != 1 {
		t.Fatalf("deleted template still listed: %+v", got)
	}
}

// List returns a copy: a caller (or a test) mutating the result must not poison
// every later read from the cache.
func TestListReturnsIndependentCopies(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.yaml", sample)
	s := New(dir)

	first := s.List()
	first[0].Name = "mutated"
	first = append(first, Poc{ID: "injected"})

	second := s.List()
	if len(second) != 1 || second[0].Name != "Log4Shell" {
		t.Fatalf("cached result was mutated by caller: %+v", second)
	}
}

func TestPageFiltersAndPaginates(t *testing.T) {
	all := []Poc{
		{ID: "CVE-2021-44228", Name: "Log4Shell", Tags: "cve,log4j,rce", File: "CVE-2021-44228.yaml"},
		{ID: "simple", Name: "Simple Check", Tags: "misc", File: "simple.yaml"},
		{ID: "apache-detect", Name: "Apache Detection", Tags: "tech,apache", File: "apache-detect.yaml"},
	}

	for _, tc := range []struct {
		name        string
		q           string
		offset      int
		limit       int
		wantLen     int
		wantMatched int
	}{
		{"no filter no limit", "", 0, 0, 3, 3},
		{"case-insensitive match on tags", "LOG4J", 0, 0, 1, 1},
		{"match on name", "apache detection", 0, 0, 1, 1},
		{"match on file name", "yaml", 0, 0, 3, 3},
		{"first page", "", 0, 2, 2, 3},
		{"second page", "", 2, 2, 1, 3},
		{"offset past end", "", 10, 2, 0, 3},
		{"no match", "nothing-here", 0, 10, 0, 0},
		{"negative offset and limit", "", -5, -1, 3, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page, matched := Page(all, tc.q, tc.offset, tc.limit)
			if len(page) != tc.wantLen || matched != tc.wantMatched {
				t.Fatalf("len=%d matched=%d, want len=%d matched=%d (page=%+v)",
					len(page), matched, tc.wantLen, tc.wantMatched, page)
			}
		})
	}
}
