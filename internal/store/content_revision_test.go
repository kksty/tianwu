package store

import (
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/model"
)

func TestContentSnapshotsSurviveBookkeepingAndLaterContentWrites(t *testing.T) {
	s := open(t)
	f, _ := s.AddFact(model.Fact{Title: "observation", Doc: strings.Repeat("original", 2048)})
	_, v := s.ReadView()
	hash := s.ContentHash()
	_, err := s.AddRun(model.Run{Purpose: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	view, err := s.ReadAt(v)
	if err != nil || len(view.Facts) != 1 || view.Facts[0].Doc != f.Doc || hash != s.ContentHash() {
		t.Fatal("run changed fact snapshot")
	}
	_, _ = s.UpdateFact(f.ID, func(f *model.Fact) { f.Doc = "changed" })
	for i := 0; i < 3; i++ {
		old, err := s.ReadAt(v)
		if err != nil || old.Facts[0].Doc != f.Doc {
			t.Fatal("historical page moved")
		}
		old.Facts[0].Title = "caller mutation"
		if s.ContentHash() != contentHash(s.records) {
			t.Fatal("incremental content fingerprint differs")
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.ContentHash() != s.ContentHash() {
		t.Fatal("fingerprint changed across restart")
	}
	old, err := reopened.ReadAt(v)
	if err != nil || old.Facts[0].Doc != f.Doc || old.Facts[0].Title == "caller mutation" {
		t.Fatal("historical snapshot changed across restart")
	}
}
