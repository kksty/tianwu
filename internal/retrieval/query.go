// Package retrieval owns the shared read contract for commander, workers and
// console. It retrieves recorded state; it does not choose an attack strategy.
package retrieval

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

type cursor struct {
	Edge     int               `json:"edge,omitempty"`
	Boundary int               `json:"boundary,omitempty"`
	Semantic uint64            `json:"semantic,omitempty"`
	Version  store.ReadVersion `json:"version"`
	Query    string            `json:"query"`
	Offset   int               `json:"offset"`
	After    uint64            `json:"after,omitempty"`
}

func encode(c cursor) string { b, _ := json.Marshal(c); return base64.RawURLEncoding.EncodeToString(b) }
func decode(token, key string) (cursor, error) {
	var c cursor
	b, e := base64.RawURLEncoding.DecodeString(token)
	if e != nil {
		return c, fmt.Errorf("invalid_cursor")
	}
	if e = json.Unmarshal(b, &c); e != nil || c.Query != key || c.Offset < 0 {
		return c, fmt.Errorf("invalid_cursor: query changed")
	}
	return c, nil
}
func queryKey(v any) string { b, _ := json.Marshal(v); return fmt.Sprintf("%x", sha256.Sum256(b)) }
func pageLimit(n int) int {
	if n <= 0 {
		return 50
	}
	if n > 500 {
		return 500
	}
	return n
}

type PageInfo struct {
	NextCursor string            `json:"next_cursor,omitempty"`
	HasMore    bool              `json:"has_more"`
	Total      int               `json:"total"`
	Version    store.ReadVersion `json:"snapshot_version"`
}

type FactQuery struct {
	Mode      string `json:"mode,omitempty"`
	Q         string `json:"q,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Intent    string `json:"intent,omitempty"`
	Status    string `json:"status,omitempty"`
	Tag       string `json:"tag,omitempty"`
	Cursor    string `json:"-"`
	Limit     int    `json:"-"`
	Ascending bool   `json:"ascending,omitempty"`
}
type FactPage struct {
	Search  *SearchStatus          `json:"search,omitempty"`
	Matches map[string]SearchMatch `json:"matches,omitempty"`
	Facts   []model.Fact           `json:"facts"`
	PageInfo
}

func read(s *store.Store, token, key string) (store.DB, cursor, error) {
	if token == "" {
		db, v := s.ReadView()
		return db, cursor{Version: v, Query: key}, nil
	}
	c, e := decode(token, key)
	if e != nil {
		return store.DB{}, c, e
	}
	db, e := s.ReadAt(c.Version)
	return db, c, e
}

// Terms are ANDed, with title/summary weighting; independent terms no longer
// require one contiguous substring. Exact IDs and explicit tags remain usable.
func TextScore(q, title, summary, doc string) (int, bool) {
	terms := strings.Fields(strings.ToLower(strings.TrimSpace(q)))
	if len(terms) == 0 {
		return 0, true
	}
	title, summary, doc = strings.ToLower(title), strings.ToLower(summary), strings.ToLower(doc)
	score := 0
	for _, term := range terms {
		switch {
		case strings.Contains(title, term):
			score += 8
		case strings.Contains(summary, term):
			score += 3
		case strings.Contains(doc, term):
			score++
		default:
			return 0, false
		}
	}
	return score, true
}

func Facts(s *store.Store, q FactQuery) (FactPage, error) {
	key := queryKey(struct {
		Type  string
		Query FactQuery
	}{"facts", q})
	db, c, e := read(s, q.Cursor, key)
	if e != nil {
		return FactPage{}, e
	}
	type ranked struct {
		fact  model.Fact
		score int
	}
	candidates, indexed, indexErr := s.FullTextCandidatesAt(q.Q, c.Version)
	if indexErr != nil {
		return FactPage{}, indexErr
	}
	rows := []ranked{}
	for _, f := range db.Facts {
		if indexed {
			if _, ok := candidates[f.ID]; !ok {
				direct := false
				for _, term := range strings.Fields(strings.ToLower(q.Q)) {
					if strings.Contains(strings.ToLower(f.ID), term) {
						direct = true
					}
				}
				if !direct {
					continue
				}
			}
		}
		if q.Kind != "" && f.Kind != q.Kind || q.Intent != "" && f.IntentID != q.Intent || q.Status != "" && f.Status != q.Status {
			continue
		}
		if q.Tag != "" {
			ok := false
			for _, t := range f.Tags {
				if strings.EqualFold(t, q.Tag) {
					ok = true
				}
			}
			if !ok {
				continue
			}
		}
		score, ok := TextScore(q.Q, f.ID+" "+f.Title, f.Summary, f.Doc)
		if ok {
			rows = append(rows, ranked{f, score})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.score != b.score {
			return a.score > b.score
		}
		if !a.fact.CreatedAt.Equal(b.fact.CreatedAt) {
			if q.Ascending {
				return a.fact.CreatedAt.Before(b.fact.CreatedAt)
			}
			return a.fact.CreatedAt.After(b.fact.CreatedAt)
		}
		return a.fact.ID < b.fact.ID
	})
	out := FactPage{Facts: []model.Fact{}, PageInfo: PageInfo{Total: len(rows), Version: c.Version}}
	if c.Offset > len(rows) {
		return out, fmt.Errorf("invalid_cursor: offset outside snapshot")
	}
	end := c.Offset + pageLimit(q.Limit)
	if end > len(rows) {
		end = len(rows)
	}
	for _, r := range rows[c.Offset:end] {
		out.Facts = append(out.Facts, r.fact)
	}
	out.HasMore = end < len(rows)
	if out.HasMore {
		c.Offset = end
		out.NextCursor = encode(c)
	}
	return out, nil
}

type IntentQuery struct {
	Status string `json:"status,omitempty"`
	Cursor string `json:"-"`
	Limit  int    `json:"-"`
}
type IntentPage struct {
	Intents []model.Intent `json:"intents"`
	PageInfo
}

func Intents(s *store.Store, q IntentQuery) (IntentPage, error) {
	key := queryKey(struct {
		Type  string
		Query IntentQuery
	}{"intents", q})
	db, c, e := read(s, q.Cursor, key)
	if e != nil {
		return IntentPage{}, e
	}
	rows := []model.Intent{}
	for _, in := range db.Intents {
		if q.Status == "" || in.Status == q.Status {
			rows = append(rows, in)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if !rows[i].CreatedAt.Equal(rows[j].CreatedAt) {
			return rows[i].CreatedAt.Before(rows[j].CreatedAt)
		}
		return rows[i].ID < rows[j].ID
	})
	out := IntentPage{Intents: []model.Intent{}, PageInfo: PageInfo{Total: len(rows), Version: c.Version}}
	if c.Offset > len(rows) {
		return out, fmt.Errorf("invalid_cursor")
	}
	end := c.Offset + pageLimit(q.Limit)
	if end > len(rows) {
		end = len(rows)
	}
	out.Intents = rows[c.Offset:end]
	out.HasMore = end < len(rows)
	if out.HasMore {
		c.Offset = end
		out.NextCursor = encode(c)
	}
	return out, nil
}

type ChangePage struct {
	Changes       []store.Change    `json:"changes"`
	NextCursor    string            `json:"next_cursor"`
	HasMore       bool              `json:"has_more"`
	HighWatermark store.ReadVersion `json:"high_watermark"`
	Pending       int               `json:"pending"`
}

func Changes(s *store.Store, token string, limit int) (ChangePage, error) {
	live := s.Watermark()
	c := cursor{Version: live, Query: "changes"}
	if token != "" {
		var e error
		c, e = decode(token, "changes")
		if e != nil {
			return ChangePage{}, e
		}
		if c.Version.Epoch != live.Epoch || c.Version.Seq > live.Seq {
			return ChangePage{}, fmt.Errorf("cursor_expired")
		}
		if c.After >= c.Version.Seq {
			c.Version = live
		}
	}
	rows, pending, err := s.ChangeWindow(c.Version, c.After, pageLimit(limit))
	if err != nil {
		return ChangePage{}, err
	}
	out := ChangePage{Changes: rows, Pending: pending, HighWatermark: c.Version}
	out.HasMore = out.Pending > len(out.Changes)
	if out.HasMore {
		c.After = out.Changes[len(out.Changes)-1].Seq
	} else {
		c.After = c.Version.Seq
	}
	out.NextCursor = encode(c)
	return out, nil
}
