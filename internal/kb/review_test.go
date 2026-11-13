package kb

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestNominationRejectsAnEditorBasedOnOutdatedPublishedContent(t *testing.T) {
	l := publishLibrary(t)
	first, _ := l.SaveCandidate(nomination("world-stale-editor"))
	old, err := l.Publish(first.ID, first.Version)
	if err != nil {
		t.Fatal(err)
	}
	current := nomination(old.ID)
	current.BaseRevision, current.Entry.Detail = Revision(old), "另一个编辑已发布的内容"
	second, err := l.SaveCandidate(current)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Publish(second.ID, second.Version); err != nil {
		t.Fatal(err)
	}
	stale := nomination(old.ID)
	stale.BaseRevision, stale.Entry.Detail = Revision(old), "从旧页面提交的修改"
	if _, err := l.SaveCandidate(stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale editor was allowed to nominate against a newer base: %v", err)
	}
}

func TestPublishingDoesNotReloadUnrelatedExternalEdits(t *testing.T) {
	l := publishLibrary(t)
	c, _ := l.SaveCandidate(nomination("world-explicit-publish"))
	unloaded := nomination("world-not-reloaded").Entry
	if err := writeJSONAtomic(filepath.Join(l.Dir(), unloaded.ID+".json"), unloaded); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := l.Publish(c.ID, c.Version); err != nil {
			t.Fatal(err)
		}
		if _, ok := findEntry(l.Entries(), unloaded.ID); ok {
			t.Fatal("publishing implicitly reloaded unrelated disk content")
		}
	}
	if err := l.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, ok := findEntry(l.Entries(), unloaded.ID); !ok {
		t.Fatal("explicit reload did not load external content")
	}
}

func TestPublicationCannotOverwriteAFileOwnedByAnotherKnowledgeID(t *testing.T) {
	l := publishLibrary(t)
	path := filepath.Join(l.Dir(), "world-destination.json")
	other := nomination("world-owner").Entry
	if err := writeJSONAtomic(path, other); err != nil {
		t.Fatal(err)
	}
	if err := l.Reload(); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	c, err := l.SaveCandidate(nomination("world-destination"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Publish(c.ID, c.Version); err == nil {
		t.Fatal("publication overwrote another ID's file")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("conflicting file was changed")
	}
}

func TestExportRejectsIncompleteLegacyEntriesBeforeRepositoryImport(t *testing.T) {
	l := publishLibrary(t)
	e := Entry{ID: "legacy-incomplete", Title: "旧条目"}
	if err := writeJSONAtomic(filepath.Join(l.Dir(), e.ID+".json"), e); err != nil {
		t.Fatal(err)
	}
	if err := l.Reload(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := l.Export(&out, []string{e.ID}); err == nil {
		t.Fatal("incomplete entry exported as a builtin")
	}
}
