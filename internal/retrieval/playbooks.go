package retrieval

import (
	"fmt"
	"sort"
	"strings"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

type PlaybookQuery struct {
	Q        string `json:"q,omitempty"`
	Tag      string `json:"tag,omitempty"`
	Status   string `json:"status,omitempty"`
	Archived string `json:"archived,omitempty"`
	Cursor   string `json:"-"`
	Limit    int    `json:"-"`
}
type PlaybookPage struct {
	Playbooks []model.Playbook `json:"playbooks"`
	Counts    map[string]int   `json:"counts"`
	Count     int              `json:"count"`
	PageInfo
}

func Playbooks(s *store.Store, q PlaybookQuery) (PlaybookPage, error) {
	if q.Archived != "" && q.Archived != "exclude" && q.Archived != "include" && q.Archived != "only" {
		return PlaybookPage{}, fmt.Errorf("invalid archived filter")
	}
	key := queryKey(struct {
		Type  string
		Query PlaybookQuery
	}{"playbooks", q})
	db, c, err := read(s, q.Cursor, key)
	if err != nil {
		return PlaybookPage{}, err
	}
	rows := []model.Playbook{}
	counts := map[string]int{}
	for _, p := range db.Playbooks {
		if q.Archived == "only" && !p.Archived || q.Archived != "only" && q.Archived != "include" && p.Archived {
			continue
		}
		if _, ok := TextScore(q.Q, p.ID+" "+p.Title, p.When, p.Doc+" "+strings.Join(p.Tags, " ")); !ok {
			continue
		}
		if q.Tag != "" {
			found := false
			for _, tag := range p.Tags {
				if strings.EqualFold(tag, q.Tag) {
					found = true
				}
			}
			if !found {
				continue
			}
		}
		counts[p.Status]++
		if q.Status == "" || p.Status == q.Status {
			rows = append(rows, p)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if (a.Status == model.PlaybookConfirmed) != (b.Status == model.PlaybookConfirmed) {
			return a.Status == model.PlaybookConfirmed
		}
		if !a.UpdatedAt.Equal(b.UpdatedAt) {
			return a.UpdatedAt.After(b.UpdatedAt)
		}
		return a.ID < b.ID
	})
	out := PlaybookPage{Playbooks: []model.Playbook{}, Counts: counts, PageInfo: PageInfo{Total: len(rows), Version: c.Version}}
	if c.Offset > len(rows) {
		return out, fmt.Errorf("invalid_cursor")
	}
	end := c.Offset + pageLimit(q.Limit)
	if end > len(rows) {
		end = len(rows)
	}
	out.Playbooks = rows[c.Offset:end]
	out.Count = len(out.Playbooks)
	out.HasMore = end < len(rows)
	if out.HasMore {
		c.Offset = end
		out.NextCursor = encode(c)
	}
	return out, nil
}
