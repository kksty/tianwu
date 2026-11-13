package retrieval

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

func BenchmarkBookkeepingBetweenSnapshotPages(b *testing.B) {
	for _, n := range []int{1000, 10000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			db := store.DB{Version: store.CurrentSchemaVersion()}
			for i := 0; i < n; i++ {
				db.Facts = append(db.Facts, model.Fact{ID: fmt.Sprintf("f_%06d", i), Title: fmt.Sprint(i), Kind: "note", Doc: strings.Repeat("x", 4096), CreatedAt: time.Unix(int64(i), 0)})
			}
			s, err := scaleSeed(filepath.Join(b.TempDir(), "ledger.sqlite"), db)
			if err != nil {
				b.Fatal(err)
			}
			defer s.Close()
			writes, pages := time.Duration(0), time.Duration(0)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				first, err := Facts(s, FactQuery{Limit: 50})
				if err != nil {
					b.Fatal(err)
				}
				start := time.Now()
				_, err = s.AddRun(model.Run{Purpose: "fixture"})
				writes += time.Since(start)
				if err != nil {
					b.Fatal(err)
				}
				start = time.Now()
				p, err := Facts(s, FactQuery{Limit: 50, Cursor: first.NextCursor})
				pages += time.Since(start)
				if err != nil || len(p.Facts) != 50 || len(p.Facts[0].Doc) != 4096 {
					b.Fatal("snapshot moved", err)
				}
			}
			b.ReportMetric(float64(writes.Nanoseconds())/float64(b.N)/1e6, "run_write_ms")
			b.ReportMetric(float64(pages.Nanoseconds())/float64(b.N)/1e6, "page_ms")
		})
	}
}
