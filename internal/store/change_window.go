package store

import (
	"encoding/json"
	"fmt"
)

func (s *Store) Watermark() ReadVersion {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v := ReadVersion{Epoch: s.readEpoch}
	if s.db.Memory != nil {
		v = ReadVersion{s.db.Memory.Epoch, s.db.Memory.Seq}
	}
	return v
}

func (s *Store) ChangeWindow(v ReadVersion, after uint64, limit int) ([]Change, int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []Change{}
	if s.db.Memory == nil {

		return out, 0, nil
	}
	if v.Epoch != s.db.Memory.Epoch || v.Seq > s.db.Memory.Seq || after > v.Seq {
		return nil, 0, fmt.Errorf("cursor_expired")
	}
	pending := 0
	if s.sqlite != nil {
		if err := s.sqlite.db.QueryRow("SELECT count(*) FROM versions WHERE seq>? AND seq<=?", after, v.Seq).Scan(&pending); err != nil {
			return nil, 0, err
		}
		rows, err := s.sqlite.db.Query("SELECT payload FROM versions WHERE seq>? AND seq<=? ORDER BY seq LIMIT ?", after, v.Seq, limit)
		if err != nil {
			return nil, 0, err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			if err = rows.Scan(&raw); err != nil {
				return nil, 0, err
			}
			var c Change
			if err = json.Unmarshal(raw, &c); err != nil {
				return nil, 0, err
			}
			c.Value = nil
			out = append(out, c)
		}
		return out, pending, rows.Err()
	}
	for _, c := range s.db.Memory.History {
		if c.Seq > after && c.Seq <= v.Seq {
			pending++
			if len(out) < limit {
				c.Value = nil
				c.Fields = append([]string(nil), c.Fields...)
				out = append(out, c)
			}
		}
	}
	return out, pending, nil
}
