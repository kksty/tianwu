package store

// Cache decoded immutable content snapshots, not execution state. It avoids
// reparsing long evidence bodies on every page while bounding retained memory.
type cachedRead struct {
	db    DB
	bytes int
}

const readCacheBudget = 128 << 20

func (s *Store) cachedView(seq uint64) (DB, bool) {
	s.readCacheMu.Lock()
	defer s.readCacheMu.Unlock()
	v, ok := s.readCache[seq]
	if !ok {
		return DB{}, false
	}
	return v.db.clone(), true
}

func (s *Store) cacheView(seq uint64, db DB, bytes int) {
	if bytes > readCacheBudget {
		return
	}
	s.readCacheMu.Lock()
	defer s.readCacheMu.Unlock()
	if s.readCache == nil {
		s.readCache = map[uint64]cachedRead{}
	}
	if _, ok := s.readCache[seq]; ok {
		return
	}
	for len(s.readCacheOrder) > 0 && (len(s.readCacheOrder) >= 4 || s.readCacheBytes+bytes > readCacheBudget) {
		old := s.readCacheOrder[0]
		s.readCacheOrder = s.readCacheOrder[1:]
		s.readCacheBytes -= s.readCache[old].bytes
		delete(s.readCache, old)
	}
	s.readCache[seq] = cachedRead{db, bytes}
	s.readCacheOrder = append(s.readCacheOrder, seq)
	s.readCacheBytes += bytes
}
