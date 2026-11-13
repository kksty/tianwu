package retrieval

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
	"path/filepath"
	"testing"
	"time"
)

func scaleLedger(t testing.TB, n int) *store.Store {
	t.Helper()
	db := store.DB{Version: store.CurrentSchemaVersion()}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		f := model.Fact{ID: fmt.Sprintf("f_%06d", i), Kind: "note", Title: fmt.Sprintf("noise observation %d", i), Summary: "unrelated current information", CreatedAt: at.Add(time.Duration(i) * time.Second)}
		if i == 0 {
			f.Title = "early credential"
			f.Doc = "original critical evidence"
		}
		if i > 0 && i < 129 {
			in := model.Intent{ID: fmt.Sprintf("in_%06d", i), Title: fmt.Sprintf("causal step %d", i), FromFactIDs: []string{fmt.Sprintf("f_%06d", i-1)}, Status: model.StatusDone, CreatedAt: at.Add(time.Duration(i) * time.Second)}
			db.Intents = append(db.Intents, in)
			f.IntentID = in.ID
		}
		db.Facts = append(db.Facts, f)
	}
	s, err := scaleSeed(filepath.Join(t.TempDir(), "ledger.sqlite"), db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
func TestLongChainSurvivesBeyondRecentFactWindow(t *testing.T) {
	s := scaleLedger(t, 1500)
	p, err := Graph(s, GraphQuery{Roots: []string{"fact:f_000128"}, Direction: "ancestors", Depth: 0, Limit: 30})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for {
		for _, f := range p.Facts {
			seen[f.ID] = true
		}
		if !p.HasMore {
			break
		}
		p, err = Graph(s, GraphQuery{Roots: []string{"fact:f_000128"}, Direction: "ancestors", Depth: 0, Limit: 30, Cursor: p.NextCursor})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 129 || !seen["f_000000"] {
		t.Fatalf("lost old chain: %d", len(seen))
	}
	page, err := Facts(s, FactQuery{Q: "credential", Limit: 1})
	if err != nil || len(page.Facts) != 1 || page.Facts[0].ID != "f_000000" {
		t.Fatalf("lost old retrieval: %+v %v", page, err)
	}
	page, err = Facts(s, FactQuery{Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	foundOld := false
	count := 0
	for {
		for _, f := range page.Facts {
			count++
			if f.ID == "f_000000" {
				foundOld = true
			}
		}
		if !page.HasMore {
			break
		}
		page, err = Facts(s, FactQuery{Limit: 500, Cursor: page.NextCursor})
		if err != nil {
			t.Fatal(err)
		}
	}
	if count != 1500 || !foundOld {
		t.Fatalf("directory truncated: %d", count)
	}
}
func BenchmarkScaleReadPaths(b *testing.B) {
	for _, n := range []int{1000, 10000, 100000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			s := scaleLedger(b, n)
			b.ReportAllocs()
			b.ResetTimer()
			factTime, graphTime := time.Duration(0), time.Duration(0)
			for i := 0; i < b.N; i++ {
				start := time.Now()
				f, err := Facts(s, FactQuery{Q: "credential", Limit: 20})
				factTime += time.Since(start)
				if err != nil || len(f.Facts) != 1 {
					b.Fatal(err)
				}
				start = time.Now()
				g, err := Graph(s, GraphQuery{Roots: []string{"fact:f_000128"}, Direction: "ancestors", Depth: 0, Limit: 500})
				graphTime += time.Since(start)
				if err != nil || len(g.Facts) != 129 {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(factTime.Microseconds())/float64(b.N)/1000, "fact_ms")
			b.ReportMetric(float64(graphTime.Microseconds())/float64(b.N)/1000, "chain_ms")
		})
	}
}

// Bulk fixture setup avoids timing thousands of unrelated model transactions.
func scaleSeed(path string, snapshot store.DB) (*store.Store, error) {
	s, err := store.Open(path)
	if err != nil {
		return nil, err
	}
	s.Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	memory := store.Memory{Epoch: "scale-fixture", Checkpoints: map[string]store.Checkpoint{}}
	var hash [32]byte
	put := func(kind, id string, pos int, v any) error {
		raw, _ := json.Marshal(v)
		memory.Seq++
		h := sha256.Sum256(append([]byte(kind+":"+id+"\x00"), raw...))
		for i := range hash {
			hash[i] ^= h[i]
		}
		c := store.Change{Seq: memory.Seq, Transaction: "fixture", Kind: kind, ID: id, Version: 1, Operation: "create", Position: pos, Value: raw}
		change, _ := json.Marshal(c)
		if _, e := tx.Exec("INSERT INTO objects VALUES(?,?,?,?,?,?)", kind, id, pos, 1, memory.Seq, raw); e != nil {
			return e
		}
		_, e := tx.Exec("INSERT INTO versions VALUES(?,?,?,?,?)", memory.Seq, kind, id, 1, change)
		return e
	}
	for i, f := range snapshot.Facts {
		if err = put("fact", f.ID, i, f); err != nil {
			return nil, err
		}
	}
	for i, in := range snapshot.Intents {
		if err = put("intent", in.ID, i, in); err != nil {
			return nil, err
		}
	}
	memory.ContentHash = hex.EncodeToString(hash[:])
	meta, _ := json.Marshal(map[string]any{"version": snapshot.Version, "memory": memory})
	if _, err = tx.Exec("UPDATE meta SET payload=?", meta); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	db.Close()
	s, err = store.Open(path)
	if err != nil {
		return nil, err
	}
	if err = s.RebuildIndexes(); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}
