package retrieval

import (
	"fmt"
	"testing"

	"github.com/kksty/tianwu/internal/model"
)

func TestFactPagesStayCompleteWhileExecutionAndFactsChange(t *testing.T) {
	s := ledger(t)
	var firstID string
	for i := 0; i < 80; i++ {
		f, err := s.AddFact(model.Fact{Title: fmt.Sprint(i), Doc: "original"})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			firstID = f.ID
		}
	}
	q := FactQuery{Limit: 7, Ascending: true}
	page, err := Facts(s, q)
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := s.AddRun(model.Run{Purpose: "fixture"})
		if err == nil {
			_, err = s.UpdateFact(firstID, func(f *model.Fact) { f.Doc = "changed" })
		}
		close(ready)
		for i := 0; i < 20 && err == nil; i++ {
			_, err = s.AddFact(model.Fact{Title: fmt.Sprintf("later %d", i)})
			if err == nil {
				_, err = s.AddRun(model.Run{Purpose: "fixture"})
			}
		}
		done <- err
	}()
	<-ready
	seen := map[string]bool{}
	for {
		for _, f := range page.Facts {
			if seen[f.ID] || f.Doc != "original" {
				t.Fatal("snapshot moved")
			}
			seen[f.ID] = true
		}
		if !page.HasMore {
			break
		}
		q.Cursor = page.NextCursor
		page, err = Facts(s, q)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(seen) != 80 {
		t.Fatal("lost snapshot facts")
	}
}
