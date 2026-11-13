package kb

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEntriesAreClean(t *testing.T) {
	dir := t.TempDir()
	n, err := Ensure(dir)
	if err != nil {
		t.Fatal(err)
	}
	items, err := os.ReadDir(filepath.Join(dir, BuiltinDir))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, item := range items {
		name := item.Name()
		b, err := os.ReadFile(filepath.Join(dir, BuiltinDir, name))
		if err != nil {
			t.Fatal(err)
		}
		var e Entry
		if err := json.Unmarshal(b, &e); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if want := strings.TrimSuffix(name, ".json"); e.ID != want {
			t.Fatalf("%s: id %q does not match the file name", name, e.ID)
		}
		if strings.TrimSpace(e.Title) == "" || strings.TrimSpace(e.Description) == "" || strings.TrimSpace(e.Detail) == "" {
			t.Fatalf("%s: title/description/detail must not be empty", name)
		}
		if err := validateEntry(e); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if seen[e.ID] {
			t.Fatalf("%s: duplicate id %q", name, e.ID)
		}
		seen[e.ID] = true
	}
	lib, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(lib.Entries()); got != n {
		t.Fatalf("Open loaded %d entries, Ensure wrote %d", got, n)
	}
}

func TestEnsureAndSearch(t *testing.T) {
	dir := t.TempDir()
	n, err := Ensure(dir)
	if err != nil {
		t.Fatal(err)
	}
	lib, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(lib.Entries()); got != n {
		t.Fatalf("loaded %d entries, materialised %d", got, n)
	}
	for _, e := range []Entry{
		{ID: "fixture-sqlmap", Title: "sqlmap fixture", Description: "测试检索", Detail: "测试事实", Source: "fixture", Tags: []string{"cve"}},
		{ID: "fixture-tagged", Title: "另一个事实", Description: "测试标签", Detail: "测试正文", Source: "fixture", Tags: []string{"cve"}},
	} {
		if err := writeJSONAtomic(filepath.Join(dir, LocalDir, e.ID+".json"), e); err != nil {
			t.Fatal(err)
		}
	}
	if err := lib.Reload(); err != nil {
		t.Fatal(err)
	}

	hits := lib.Search("sqlmap", nil, 5)
	if len(hits) == 0 || !strings.Contains(strings.ToLower(hits[0].Title), "sql") {
		t.Fatalf("sqlmap search = %+v", hits)
	}
	tagged := lib.Search("", []string{"cve"}, 5)
	if len(tagged) == 0 {
		t.Fatal("tag search returned nothing")
	}
	for _, e := range tagged {
		found := false
		for _, tag := range e.Tags {
			if tag == "cve" {
				found = true
			}
		}
		if !found {
			t.Fatalf("entry %s returned for tag cve without it: %+v", e.ID, e.Tags)
		}
	}
}

func TestEnsureIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	first, err := Ensure(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Ensure(dir)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("ensure counts differ: %d then %d", first, second)
	}
}

func TestLocalEntryWinsAndSurvivesEnsure(t *testing.T) {
	dir := t.TempDir()
	if _, err := Ensure(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, LocalDir)); err != nil {
		t.Fatalf("Ensure did not create the local dir: %v", err)
	}
	local := filepath.Join(dir, LocalDir, "kb001.json")
	if err := os.WriteFile(local, []byte(`{"id":"kb001","title":"现场条目","detail":"覆盖内置"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Ensure(dir); err != nil {
		t.Fatal(err)
	}
	// Ensure owns builtin/ only: a second run must leave the local file alone.
	if b, err := os.ReadFile(local); err != nil || !strings.Contains(string(b), "现场条目") {
		t.Fatalf("local entry was rewritten: %q, %v", b, err)
	}
	lib, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, e := range lib.Entries() {
		if e.ID != "kb001" {
			continue
		}
		found++
		if e.Title != "现场条目" {
			t.Fatalf("builtin entry did not yield to the local one: %q", e.Title)
		}
	}
	if found != 1 {
		t.Fatalf("kb001 appears %d times after merge", found)
	}
}

func TestEnsureMigratesPreSplitEntries(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, LocalDir), 0o755); err != nil {
		t.Fatal(err)
	}
	legacyFile := filepath.Join(dir, LocalDir, "kb001.json")
	if err := os.WriteFile(legacyFile, []byte(`{"id":"kb001","title":"旧条目","detail":"旧内容"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Ensure(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacyFile); !os.IsNotExist(err) {
		t.Fatalf("the pre-split copy still shadows builtin: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "entries.legacy", "kb001.json")); err != nil {
		t.Fatalf("the pre-split copy was not preserved: %v", err)
	}
	// Once migrated, entries/ is the operator's again and must not be touched.
	local := filepath.Join(dir, LocalDir, "kb999.json")
	if err := os.WriteFile(local, []byte(`{"id":"kb999","title":"现场条目"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Ensure(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(local); err != nil {
		t.Fatalf("a post-migration local entry was moved: %v", err)
	}
}

func TestCopyToCase(t *testing.T) {
	src := t.TempDir()
	if _, err := Ensure(src); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(src, LocalDir, "kb999.json")
	if err := os.WriteFile(local, []byte(`{"id":"kb999","title":"现场条目","detail":"随 case 带走"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), ".tw", "kb")
	if err := CopyTo(src, dst); err != nil {
		t.Fatal(err)
	}
	lib, err := Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	localSeen := false
	for _, e := range lib.Entries() {
		if e.ID == "kb999" {
			localSeen = true
		}
	}
	if !localSeen {
		t.Fatal("the local entry did not reach the case copy")
	}
}
