package store

// RebuildIndexes is an explicit maintenance operation; it neither changes
// facts nor emits content changes or replays execution actions.
func (s *Store) RebuildIndexes() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.sqlite.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("DELETE FROM fact_search"); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM relations"); err != nil {
		return err
	}
	for _, r := range s.records {
		if err = writeSQLiteRow(tx, Change{Kind: r.kind, ID: r.id, Position: r.position, Version: r.version, Seq: r.seq, Value: r.raw, Operation: "move"}); err != nil {
			return err
		}
	}
	if _, err = tx.Exec("INSERT INTO fact_search(fact_search) VALUES('integrity-check')"); err != nil {
		return err
	}
	return tx.Commit()
}
