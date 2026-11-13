package store

import "fmt"

// FactVersionsAt returns the record version of every fact that exists at
// snapshot v, keyed by fact id.
//
// The retrieval read path deliberately drops DB.Memory: a reader must never
// walk the mutable journal (ReadView/ReadAt both return Memory=nil). A
// projection that shows a record version therefore cannot read it off the DB it
// paginates — it has to ask the ledger for the versions belonging to the very
// same ReadVersion. Without this, a page could pair a frozen body with a later
// version (or pass a fake zero off as "version 0"), and the fact id would no
// longer identify the exact record the page was cut from.
//
// The version is taken from the last fact change at or before v.Seq. A delete
// removes the id; a move changes only ordering, not content, so it never bumps
// the record version. Baseline entries count: they are the first recorded
// version of a row that predates the journal.
func (s *Store) FactVersionsAt(v ReadVersion) (map[string]uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	epoch := s.readEpoch
	seq := uint64(0)
	if s.db.Memory != nil {
		epoch, seq = s.db.Memory.Epoch, s.db.Memory.Seq
	}
	// Same rejection as ReadAt: a cursor from another ledger identity, or one
	// that claims a watermark this ledger has not reached, is not a snapshot.
	if v.Epoch != epoch || v.Seq > seq {
		return nil, fmt.Errorf("cursor_expired: ledger identity or watermark changed")
	}
	out := map[string]uint64{}
	if s.db.Memory == nil {
		return out, nil
	}
	for _, c := range s.db.Memory.History {
		if c.Seq > v.Seq {
			break
		}
		if c.Kind != "fact" || c.Operation == "move" {
			continue
		}
		if c.Operation == "delete" {
			delete(out, c.ID)
			continue
		}
		out[c.ID] = c.Version
	}
	return out, nil
}
