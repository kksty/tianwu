package store

import (
	"errors"
	"fmt"

	"github.com/kksty/tianwu/internal/model"
)

// ErrFactRefMissing means a request named a fact that this project's ledger
// does not have. References and pins are explicit writer input, unlike a
// premise that is validated at dispatch: an unknown id here is a typo in the
// request, not a condition to discover later.
var ErrFactRefMissing = errors.New("引用的事实不存在")

// checkFactRefsLocked verifies explicit fact references. Caller holds the
// store lock. Missing ids are reported by name so the caller can fix the
// request instead of guessing which one was rejected.
func (s *Store) checkFactRefsLocked(groups ...[]string) error {
	for _, ids := range groups {
		for _, id := range ids {
			if _, ok := s.records[recordKey("fact", id)]; !ok {
				return fmt.Errorf("%w：%s", ErrFactRefMissing, id)
			}
		}
	}
	return nil
}

// ContextRefsExist reports whether every named fact exists. It is the API-side
// pre-check for request bodies that carry context_fact_ids before a creation
// path runs.
func (s *Store) ContextRefsExist(ids []string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.checkFactRefsLocked(ids) == nil
}

// applyInitialContext validates and stores the context selection supplied at
// creation time. Caller holds the store lock and is responsible for flushing.
func (s *Store) applyInitialContextLocked(in *model.Intent) error {
	in.ContextFactIDs = normalizeContextIDs(in.ContextFactIDs)
	in.PinnedFactIDs = normalizeContextIDs(in.PinnedFactIDs)
	in.InterestEntityRefs = normalizeContextIDs(in.InterestEntityRefs)
	return s.checkFactRefsLocked(in.ContextFactIDs, in.PinnedFactIDs)
}
