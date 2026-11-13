package store

import (
	"encoding/json"
	"github.com/kksty/tianwu/internal/model"
	"path/filepath"
	"testing"
)

func sqliteLedger(t *testing.T) *Store {
	t.Helper()
	s, e := OpenSQLite(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
func TestSQLiteCommitsVersionsAndRestoresFailedTransaction(t *testing.T) {
	s := sqliteLedger(t)
	f, e := s.AddFact(model.Fact{Title: "old observation", Doc: "original evidence"})
	if e != nil {
		t.Fatal(e)
	}
	before := s.ContentHash()
	_, version, _ := s.FactVersion(f.ID)
	if _, e = s.sqlite.db.Exec(`CREATE TRIGGER fail_fact BEFORE UPDATE ON objects WHEN NEW.kind='fact' BEGIN SELECT RAISE(ABORT,'fixture disk transaction failure'); END`); e != nil {
		t.Fatal(e)
	}
	_, e = s.UpdateFact(f.ID, func(f *model.Fact) { f.Doc = "must roll back" })
	if e == nil {
		t.Fatal("failed commit accepted")
	}
	got, _ := s.FactByID(f.ID)
	if got.Doc != "original evidence" || s.ContentHash() != before || func() bool { _, v, _ := s.FactVersion(f.ID); return v != version }() {
		t.Fatal("failed transaction leaked")
	}
	if _, e = s.sqlite.db.Exec("DROP TRIGGER fail_fact"); e != nil {
		t.Fatal(e)
	}
	_, e = s.UpdateFact(f.ID, func(f *model.Fact) { f.Doc = "revised evidence" })
	if e != nil {
		t.Fatal(e)
	}
	history := s.History("fact", f.ID)
	if len(history) != 2 || s.ContentHash() == before {
		t.Fatalf("history=%d", len(history))
	}
	var old model.Fact
	if e = json.Unmarshal(history[0].Value, &old); e != nil || old.Doc != "original evidence" {
		t.Fatal("historical observation changed")
	}
	path := s.Path()
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s2, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer s2.Close()
	if s2.ContentHash() != s.ContentHash() || len(s2.History("fact", f.ID)) != 2 {
		t.Fatal("restart lost versions")
	}
}

func TestIndexRebuildPreservesContentAndVersions(t *testing.T) {
	s := sqliteLedger(t)
	f, err := s.AddFact(model.Fact{Title: "original credential", Tags: []string{"original"}})
	if err != nil {
		t.Fatal(err)
	}
	before, watermark := s.ContentHash(), s.Watermark()
	f.Tags[0] = "external mutation"
	got, _ := s.FactByID(f.ID)
	if got.Tags[0] != "original" {
		t.Fatal("caller mutated committed fact")
	}
	if _, err = s.sqlite.db.Exec("DELETE FROM fact_search"); err != nil {
		t.Fatal(err)
	}
	if err = s.RebuildIndexes(); err != nil {
		t.Fatal(err)
	}
	matches, used, err := s.FullTextCandidates("credential")
	if err != nil || !used || len(matches) != 1 {
		t.Fatalf("index not restored: %v %v", matches, err)
	}
	if before != s.ContentHash() || watermark != s.Watermark() {
		t.Fatal("derived rebuild changed content")
	}
}
