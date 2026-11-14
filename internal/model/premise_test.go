package model

import (
	"testing"
	"time"
)

// Review slices reach this helper in two different orders: the persisted ledger
// keeps append order, store.Reviews returns newest-first. "Newest verdict for the
// current assertion" must be decided by the verdict's time, never by slice
// position, or readiness and the automatic queue disagree about the same fact.
func TestLatestReviewForClaimIsOrderIndependent(t *testing.T) {
	f := Fact{ID: "f_1", ClaimVersion: 3, ClaimHash: "hash-3"}
	older := Review{ID: "rv_old", FactID: "f_1", ClaimVersion: 3, ClaimHash: "hash-3", Verdict: "pass", CreatedAt: time.Unix(100, 0)}
	newer := Review{ID: "rv_new", FactID: "f_1", ClaimVersion: 3, ClaimHash: "hash-3", Verdict: "needs_evidence", CreatedAt: time.Unix(200, 0)}

	cases := map[string][]Review{
		"append order": {older, newer},
		"newest first": {newer, older},
	}
	for name, reviews := range cases {
		t.Run(name, func(t *testing.T) {
			got, ok := LatestReviewForClaim(f, reviews)
			if !ok || got.ID != "rv_new" {
				t.Fatalf("latest review = %+v (ok=%v), want rv_new", got, ok)
			}
		})
	}
}

// Two verdicts written in the same instant cannot be ordered by time; the later
// append (later slice element) is the one that actually overwrote the state.
func TestLatestReviewForClaimTieTakesLaterElement(t *testing.T) {
	at := time.Unix(100, 0)
	f := Fact{ID: "f_2", ClaimVersion: 1, ClaimHash: "hash-1"}
	first := Review{ID: "rv_1", FactID: "f_2", ClaimVersion: 1, ClaimHash: "hash-1", Verdict: "pass", CreatedAt: at}
	second := Review{ID: "rv_2", FactID: "f_2", ClaimVersion: 1, ClaimHash: "hash-1", Verdict: "needs_evidence", CreatedAt: at}

	got, ok := LatestReviewForClaim(f, []Review{first, second})
	if !ok || got.ID != "rv_2" {
		t.Fatalf("same-instant verdict = %+v (ok=%v), want the later element rv_2", got, ok)
	}
}

// A verdict for an older assertion is history: changing the claim must not let
// the old proof speak for the new content.
func TestLatestReviewForClaimIgnoresOlderAssertions(t *testing.T) {
	f := Fact{ID: "f_3", ClaimVersion: 2, ClaimHash: "hash-2"}
	stale := Review{ID: "rv_stale", FactID: "f_3", ClaimVersion: 1, ClaimHash: "hash-1", Verdict: "pass", CreatedAt: time.Unix(300, 0)}
	current := Review{ID: "rv_current", FactID: "f_3", ClaimVersion: 2, ClaimHash: "hash-2", Verdict: "needs_evidence", CreatedAt: time.Unix(100, 0)}

	got, ok := LatestReviewForClaim(f, []Review{stale, current})
	if !ok || got.ID != "rv_current" {
		t.Fatalf("latest review = %+v (ok=%v), want the current assertion's rv_current", got, ok)
	}
}
