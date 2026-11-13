package store

import (
	"encoding/binary"
	"fmt"
	"math"
	"strings"
)

// Vectors are a rebuildable cache, never authoritative observations or a
// source of content-change wakes. The model key includes the embedding endpoint.
func (s *Store) VectorWatermark() (uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var seq uint64
	err := s.sqlite.db.QueryRow("SELECT coalesce(max(seq),0) FROM vectors").Scan(&seq)
	return seq, err
}
func (s *Store) ReadVectors(model string, hashes []string, upper uint64) (map[string][]float32, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[string][]float32{}
	if len(hashes) == 0 {
		return out, nil
	}
	if len(hashes) > 512 {
		return nil, fmt.Errorf("vector lookup batch too large")
	}
	args := []any{model, upper}
	placeholders := []string{}
	for _, hash := range hashes {
		args = append(args, hash)
		placeholders = append(placeholders, "?")
	}
	rows, err := s.sqlite.db.Query("SELECT hash,vector FROM vectors WHERE model=? AND seq<=? AND hash IN ("+strings.Join(placeholders, ",")+")", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var hash string
		var raw []byte
		if err = rows.Scan(&hash, &raw); err != nil {
			return nil, err
		}
		if len(raw)%4 != 0 {
			return nil, fmt.Errorf("invalid cached embedding")
		}
		vector := make([]float32, len(raw)/4)
		for i := range vector {
			vector[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
		}
		out[hash] = vector
	}
	return out, rows.Err()
}
func (s *Store) CacheVectors(model string, vectors map[string][]float32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(vectors) > 32 {
		return fmt.Errorf("embedding write batch too large")
	}
	tx, err := s.sqlite.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	dim := 0
	for hash, v := range vectors {
		if len(v) == 0 || len(v) > 65536 {
			return fmt.Errorf("invalid embedding dimensions")
		}
		if dim != 0 && dim != len(v) {
			return fmt.Errorf("inconsistent embedding dimensions")
		}
		dim = len(v)
		raw := make([]byte, 4*len(v))
		for i, f := range v {
			if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
				return fmt.Errorf("non-finite embedding")
			}
			binary.LittleEndian.PutUint32(raw[i*4:], math.Float32bits(f))
		}
		if _, err = tx.Exec("INSERT INTO vectors(model,hash,vector) VALUES(?,?,?) ON CONFLICT(model,hash) DO NOTHING", model, hash, raw); err != nil {
			return err
		}
	}
	return tx.Commit()
}
