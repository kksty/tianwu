package store

import (
	"database/sql"
	"fmt"
	"github.com/kksty/tianwu/internal/model"
	"strings"
)

type Relation struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Kind   string `json:"kind"`
}

// ReadNeighborhood uses indexed explicit relationships for a current snapshot.
// Older snapshots use the version store instead of applying today's edges.
func (s *Store) ReadNeighborhood(v ReadVersion, roots []string, direction string, depth int) (DB, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.sqlite == nil || len(roots) == 0 {
		return DB{}, false, nil
	}
	if s.db.Memory == nil || v.Epoch != s.db.Memory.Epoch || v.Seq > s.db.Memory.Seq {
		return DB{}, false, fmt.Errorf("cursor_expired")
	}
	history := s.db.Memory.History
	if len(history) > 0 && history[len(history)-1].Seq > v.Seq {
		return DB{}, false, nil
	}
	tx, err := s.sqlite.db.Begin()
	if err != nil {
		return DB{}, false, err
	}
	defer tx.Rollback()
	db := DB{Version: s.db.Version, ReadExistence: map[string]string{}}
	type node struct {
		id    string
		depth int
	}
	queue := []node{}
	seen := map[string]bool{}
	for _, id := range roots {
		if !seen[id] {
			queue = append(queue, node{id, 0})
			seen[id] = true
		}
	}
	relations := map[string]bool{}
	for head := 0; head < len(queue); head++ {
		current := queue[head]
		r, exists := s.records[current.id]
		if !exists {
			db.ReadExistence[current.id] = "missing"
			continue
		}
		if err = appendRow(&db, r.kind, r.raw); err != nil {
			return DB{}, false, err
		}
		// 'pin' is a recorded fact->intent edge like 'reference': a pinned fact
		// must lead back to the intent that pinned it, otherwise a root scoped
		// read (/api/graph?root=fact:<id>) would lose the pin relation that the
		// whole-graph read still shows. Keep it in the indexed relation set.
		rows, err := tx.Query("SELECT source,target,relation FROM relations WHERE (source=? OR target=?) AND relation IN ('premise','reference','produces','delegates','supports','pin') ORDER BY source,target,relation", current.id, current.id)
		if err != nil {
			return DB{}, false, err
		}
		for rows.Next() {
			var edge Relation
			if err = rows.Scan(&edge.Source, &edge.Target, &edge.Kind); err != nil {
				_ = rows.Close()
				return DB{}, false, err
			}
			key := edge.Source + "\x00" + edge.Target + "\x00" + edge.Kind
			if !relations[key] {
				relations[key] = true
				db.ReadEdges = append(db.ReadEdges, edge)
			}
			for _, id := range []string{edge.Source, edge.Target} {
				if _, known := db.ReadExistence[id]; !known {
					state := "not_loaded"
					other, found := s.records[id]
					if !found {
						state = "missing"
					} else if f, ok := other.value.(model.Fact); ok && f.Status == "rejected" {
						state = "rejected"
					}
					db.ReadExistence[id] = state
				}
			}
			if depth > 0 && current.depth >= depth {
				continue
			}
			neighbor := ""
			if edge.Source == current.id && direction != "ancestors" {
				neighbor = edge.Target
			}
			if edge.Target == current.id && direction != "descendants" {
				neighbor = edge.Source
			}
			if neighbor != "" && !seen[neighbor] {
				seen[neighbor] = true
				queue = append(queue, node{neighbor, current.depth + 1})
			}
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil && err != sql.ErrNoRows {
			return DB{}, false, err
		}
	}
	for _, root := range roots {
		if !strings.Contains(root, ":") {
			return DB{}, false, fmt.Errorf("invalid graph root")
		}
	}
	return db, true, nil
}
