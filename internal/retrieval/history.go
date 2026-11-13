package retrieval

import (
	"fmt"
	"github.com/kksty/tianwu/internal/store"
)

type HistoryPage struct {
	History []store.Change `json:"history"`
	PageInfo
}

func History(s *store.Store, kind, id, token string, limit int) (HistoryPage, error) {
	key := queryKey([]string{"history", kind, id})
	c := cursor{Version: s.Watermark(), Query: key}
	if token != "" {
		var err error
		c, err = decode(token, key)
		if err != nil {
			return HistoryPage{}, err
		}
		live := s.Watermark()
		if c.Version.Epoch != live.Epoch || c.Version.Seq > live.Seq {
			return HistoryPage{}, fmt.Errorf("cursor_expired")
		}
	}
	rows := []store.Change{}
	for _, v := range s.History(kind, id) {
		if v.Seq <= c.Version.Seq || v.Operation == "baseline" {
			rows = append(rows, v)
		}
	}
	out := HistoryPage{History: []store.Change{}, PageInfo: PageInfo{Total: len(rows), Version: c.Version}}
	if c.Offset > len(rows) {
		return out, fmt.Errorf("invalid_cursor")
	}
	end := c.Offset + pageLimit(limit)
	if end > len(rows) {
		end = len(rows)
	}
	out.History = rows[c.Offset:end]
	out.HasMore = end < len(rows)
	if out.HasMore {
		c.Offset = end
		out.NextCursor = encode(c)
	}
	return out, nil
}
