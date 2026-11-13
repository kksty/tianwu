package store

import "fmt"

func (s *Store) validateMemory() error {
	if s.db.Memory == nil {
		return nil
	}
	m := s.db.Memory
	if m.Epoch == "" {
		return fmt.Errorf("invalid ledger memory: missing epoch")
	}
	last := uint64(0)
	for _, c := range m.History {
		if c.Seq <= last || c.Seq > m.Seq || c.ID == "" || c.Version == 0 {
			return fmt.Errorf("invalid ledger memory: non-monotonic version history")
		}
		last = c.Seq
	}
	if m.ContentHash != contentHash(s.records) {
		return fmt.Errorf("invalid ledger memory: content fingerprint differs from committed rows")
	}
	return nil
}
