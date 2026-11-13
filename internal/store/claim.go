package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/kksty/tianwu/internal/model"
)

// ErrReviewConflict means a verdict was judged against an assertion that has
// since changed. Applying it to the new row would let an old proof speak for
// new content, so the write is refused and the fact stays unverified.
var ErrReviewConflict = errors.New("复核依据的断言版本已变化：旧结论不能落到新版本上")

// claimHash fingerprints the part of a fact a verifier proves. The display
// title, review status, origin, provenance bookkeeping and identity are
// excluded: renaming a row or recording a verdict must not invalidate the same
// proof. Evidence is included because adding evidence changes what the claim
// rests on — that is a new assertion, not the old one with a nicer label.
func claimHash(f model.Fact) string {
	tags, evidence, refs := slices.Clone(f.Tags), slices.Clone(f.EvidenceIDs), slices.Clone(f.Refs)
	slices.Sort(tags)
	slices.Sort(evidence)
	slices.SortFunc(refs, func(a, b model.Ref) int {
		if a.Scheme != b.Scheme {
			return strings.Compare(a.Scheme, b.Scheme)
		}
		return strings.Compare(a.Locator, b.Locator)
	})
	// The struct field order is the hash contract: reordering fields changes
	// every hash and would silently unverify the whole ledger.
	raw, _ := json.Marshal(struct {
		Kind, Entity, Summary, Doc, Confidence, Proof, Context string
		Tags, Evidence                                         []string
		Refs                                                   []model.Ref
		ObservedAt                                             any
	}{f.Kind, f.EntityRef, f.Summary, f.Doc, f.Confidence, f.Proof, f.ExecutionContextRef, tags, evidence, refs, f.ObservedAt})
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

// FindDeclaration resolves the explicit observation a declaration continues.
//
// Identity comes from the writer, never from the display text: a source ref is
// the strongest identity (an audit finding), otherwise the declaration id.
// Two different declarations with the same title are two observations, which is
// exactly what the old (intent, kind, title) rule could not express. A line
// that later gains a source ref may adopt the row recorded for the same
// declaration id: that is one observation whose provenance became known, not a
// second finding.
func (s *Store) FindDeclaration(intentID, sourceRef, declarationID string) (model.Fact, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sourceRef, declarationID = strings.TrimSpace(sourceRef), strings.TrimSpace(declarationID)
	if sourceRef == "" && declarationID == "" {
		return model.Fact{}, false
	}
	var adopt *model.Fact
	for i := range s.db.Facts {
		f := &s.db.Facts[i]
		if f.IntentID != intentID {
			continue
		}
		switch {
		case sourceRef != "" && f.SourceRef == sourceRef:
			return detachFact(*f), true
		case sourceRef != "" && f.SourceRef == "" && declarationID != "" && f.DeclarationID == declarationID:
			if adopt == nil {
				adopt = f
			}
		case sourceRef == "" && f.SourceRef == "" && declarationID != "" && f.DeclarationID == declarationID:
			return detachFact(*f), true
		}
	}
	if adopt != nil {
		return detachFact(*adopt), true
	}
	return model.Fact{}, false
}

// FindDerivedFact resolves the one derived row a completed run refreshes. It
// matches only derived notes: an explicit observation with the same title is a
// different fact, and merging it here would let a derived note overwrite (or be
// hidden by) a declaration the executor made on purpose. This is the whole
// remaining role of title matching, and it is deliberately unable to see
// declared rows.
func (s *Store) FindDerivedFact(intentID, kind, title string) (model.Fact, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range s.db.Facts {
		f := &s.db.Facts[i]
		if f.IntentID != intentID || f.Origin != model.OriginDerived || f.SourceRef != "" {
			continue
		}
		if f.Kind == kind && f.Title == title {
			return detachFact(*f), true
		}
	}
	return model.Fact{}, false
}
