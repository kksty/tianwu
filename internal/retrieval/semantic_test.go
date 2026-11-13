package retrieval

import (
	"context"
	"errors"
	"fmt"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
	"path/filepath"
	"strings"
	"testing"
)

type fixtureEmbedder struct{ calls int }

func (e *fixtureEmbedder) Key() string  { return "fixture-model-1" }
func (e *fixtureEmbedder) Name() string { return "fixture" }
func (e *fixtureEmbedder) Embed(_ context.Context, texts []string) ([][]float32, EmbeddingUsage, error) {
	e.calls++
	out := [][]float32{}
	for _, text := range texts {
		switch {
		case strings.Contains(text, "credential") || strings.Contains(text, "口令"):
			out = append(out, []float32{1, 0})
		default:
			out = append(out, []float32{0, 1})
		}
	}
	return out, EmbeddingUsage{Tokens: len(texts), Requests: 1}, nil
}
func TestSemanticRecallUsesCompleteVersionedIndex(t *testing.T) {
	s, e := store.OpenSQLite(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	wanted, _ := s.AddFact(model.Fact{Title: "早期口令线索"})
	for i := 0; i < 36; i++ {
		_, _ = s.AddFact(model.Fact{Title: fmt.Sprintf("unrelated %02d", i)})
	}
	embed := &fixtureEmbedder{}
	_, e = SemanticFacts(context.Background(), s, FactQuery{Q: "credential", Mode: "semantic", Limit: 1}, embed)
	var incomplete *IndexIncomplete
	if !errors.As(e, &incomplete) || incomplete.Total != 37 {
		t.Fatalf("missing index was hidden: %v", e)
	}
	p, e := IndexFacts(context.Background(), s, embed, "")
	if e != nil || !p.HasMore || p.Indexed != 32 {
		t.Fatalf("index page %+v %v", p, e)
	}
	if _, e = IndexFacts(context.Background(), s, embed, p.NextCursor); e != nil {
		t.Fatal(e)
	}
	first, e := SemanticFacts(context.Background(), s, FactQuery{Q: "credential", Mode: "semantic", Limit: 1}, embed)
	if e != nil || len(first.Facts) != 1 || first.Facts[0].ID != wanted.ID || first.Matches[wanted.ID].Lexical != 0 || !first.Search.Complete {
		t.Fatalf("semantic result %+v %v", first, e)
	}
	_, _ = s.AddFact(model.Fact{Title: "later unknown"})
	second, e := SemanticFacts(context.Background(), s, FactQuery{Q: "credential", Mode: "semantic", Limit: 1, Cursor: first.NextCursor}, embed)
	if e != nil || second.Facts[0].ID == wanted.ID {
		t.Fatalf("snapshot moved: %+v %v", second, e)
	}
	_, e = SemanticFacts(context.Background(), s, FactQuery{Q: "credential", Mode: "hybrid"}, embed)
	if !errors.As(e, &incomplete) {
		t.Fatalf("new fact incorrectly marked indexed: %v", e)
	}
}
func TestEmbeddingCacheIsNotAFactChange(t *testing.T) {
	s, e := store.OpenSQLite(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	before := s.ContentHash()
	wm := s.Watermark()
	if e = s.CacheVectors("fixture", map[string][]float32{"hash": {1, 2}}); e != nil {
		t.Fatal(e)
	}
	if before != s.ContentHash() || wm != s.Watermark() {
		t.Fatal("derived index changed project state")
	}
}
