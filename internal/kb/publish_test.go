package kb

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func publishLibrary(t *testing.T) *Library {
	t.Helper()
	dir := t.TempDir()
	if _, err := Ensure(dir); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomic(filepath.Join(dir, BuiltinDir, "fixture-base.json"), Entry{ID: "fixture-base", Title: "测试基准事实", Description: "测试引用", Detail: "独立于随版本变化的真实知识内容", Source: "fixture"}); err != nil {
		t.Fatal(err)
	}
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func nomination(id string) Candidate {
	return Candidate{Entry: Entry{ID: id, Title: "协议事实", Description: "何时核对", Detail: "有明确适用条件的事实。", Applicability: "测试协议 v1", Source: "原始规范", SourceRefs: []string{"https://example.test/spec"}}, Reason: "协议约定不依赖本次目标", Origin: "private-project"}
}

func TestPublishingUpdatesSharedReadersAndSurvivesRestart(t *testing.T) {
	l := publishLibrary(t)
	c, err := l.SaveCandidate(nomination("world-example"))
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Search("测试协议 v1", nil, 5)) != 0 {
		t.Fatal("candidate entered published search")
	}
	e, err := l.Publish(c.ID, c.Version)
	if err != nil {
		t.Fatal(err)
	}
	if hits := l.Search("测试协议 v1", nil, 5); len(hits) != 1 {
		t.Fatalf("shared snapshot not updated: %v", hits)
	}
	if again, err := l.Publish(c.ID, c.Version); err != nil || Revision(again) != Revision(e) {
		t.Fatal("publish retry changed entry", err)
	}
	readers := l.Entries()
	for i := range readers {
		if readers[i].ID == e.ID {
			readers[i].SourceRefs[0] = "modified"
		}
	}
	if got, _ := findEntry(l.Entries(), e.ID); got.SourceRefs[0] != "https://example.test/spec" {
		t.Fatal("reader mutated shared slice")
	}
	reopened, err := Open(filepath.Dir(l.Dir()))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := reopened.Candidates()
	if err != nil || len(rows) != 1 || !rows[0].Published {
		t.Fatalf("publication not durable: %+v %v", rows, err)
	}
}

func TestCompetingNominationsCannotOverwritePublishedKnowledge(t *testing.T) {
	l := publishLibrary(t)
	a, _ := l.SaveCandidate(nomination("world-race"))
	b, _ := l.SaveCandidate(nomination("world-race"))
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, c := range []Candidate{a, b} {
		wg.Add(1)
		go func(c Candidate) { defer wg.Done(); _, err := l.Publish(c.ID, c.Version); results <- err }(c)
	}
	wg.Wait()
	close(results)
	success, conflicts := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("success=%d conflicts=%d", success, conflicts)
	}
}

func TestCandidateEditsAreVersionedAndPublishedRevisionsCanBeCorrected(t *testing.T) {
	l := publishLibrary(t)
	c, _ := l.SaveCandidate(nomination("world-edit"))
	if _, err := l.Publish(c.ID, c.Version); err != nil {
		t.Fatal(err)
	}
	c.Entry.Detail = "修订后的事实"
	updated, err := l.SaveCandidate(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.SaveCandidate(c); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale edit: %v", err)
	}
	if _, err := l.Publish(c.ID, c.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale publish: %v", err)
	}
	if e, err := l.Publish(updated.ID, updated.Version); err != nil || e.Detail != c.Entry.Detail {
		t.Fatalf("correction: %+v %v", e, err)
	}
}

func TestReloadRejectsPartialEditsWithoutLosingWorkingSnapshot(t *testing.T) {
	l := publishLibrary(t)
	count := len(l.Entries())
	p := filepath.Join(l.Dir(), "external.json")
	if err := os.WriteFile(p, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := l.Reload(); err == nil {
		t.Fatal("partial JSON accepted")
	}
	if len(l.Entries()) != count {
		t.Fatal("working snapshot lost")
	}
	e := nomination("world-external").Entry
	if err := writeJSONAtomic(p, e); err != nil {
		t.Fatal(err)
	}
	if err := l.Reload(); err != nil {
		t.Fatal(err)
	}
	draft := nomination(e.ID)
	draft.BaseRevision = Revision(e)
	c, err := l.SaveCandidate(draft)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Publish(c.ID, c.Version); err != nil {
		t.Fatal(err)
	}
	if err := l.Reload(); err != nil {
		t.Fatal(err)
	}
	if got, _ := findEntry(l.Entries(), e.ID); got.Promotion == "" {
		t.Fatal("publication lost after reload of differently named local file")
	}
	if _, err := os.Stat(filepath.Join(l.Dir(), e.ID+".json")); !os.IsNotExist(err) {
		t.Fatal("duplicate local entry created")
	}
}

func TestExportProducesRepositoryAssetsWithoutInstanceOrigins(t *testing.T) {
	l := publishLibrary(t)
	c, _ := l.SaveCandidate(nomination("world-export"))
	if _, err := l.Publish(c.ID, c.Version); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := l.Export(&b, []string{c.Entry.ID}); err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(b.Bytes()), int64(b.Len()))
	if err != nil || len(z.File) != 3 {
		t.Fatalf("export: %v", err)
	}
	if z.File[0].Name != "internal/kb/assets/entries/world-export.json" {
		t.Fatal(z.File[0].Name)
	}
	f, _ := z.File[0].Open()
	defer f.Close()
	raw, _ := io.ReadAll(f)
	if strings.Contains(string(raw), c.ID) || strings.Contains(string(raw), c.Origin) || strings.Contains(string(raw), "promotion") {
		t.Fatal("instance metadata exported")
	}
	if !strings.Contains(string(raw), "https://example.test/spec") {
		t.Fatal("source provenance missing")
	}
	bad := nomination("../escape")
	if _, err := l.SaveCandidate(bad); err == nil {
		t.Fatal("path traversal accepted")
	}
}

func TestCaseSnapshotUsesPublishedIDOverridesAndRemovesOnlyStaleCacheEntries(t *testing.T) {
	l := publishLibrary(t)
	override := l.Entries()[0]
	override.Title = "现场覆盖标题"
	if err := writeJSONAtomic(filepath.Join(l.Dir(), "different-name.json"), override); err != nil {
		t.Fatal(err)
	}
	if err := l.Reload(); err != nil {
		t.Fatal(err)
	}
	caseDir := t.TempDir()
	dst := filepath.Join(caseDir, ".tw", "kb")
	if err := l.CopyTo(dst); err != nil {
		t.Fatal(err)
	}
	copy, err := Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	if e, _ := findEntry(copy.Entries(), override.ID); e.Title != override.Title {
		t.Fatal("case snapshot disagrees with world query")
	}
	if err := writeJSONAtomic(filepath.Join(dst, "entries", "stale.json"), nomination("stale").Entry); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(caseDir, "operator.txt"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := l.CopyTo(dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "entries", "stale.json")); !os.IsNotExist(err) {
		t.Fatal("stale cache retained")
	}
	if _, err := os.Stat(filepath.Join(caseDir, "operator.txt")); err != nil {
		t.Fatal("case content changed", err)
	}
}
