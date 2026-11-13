package verifier

import (
	"context"
	"errors"
	"testing"

	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// The verdict records which assertion version it proved. A reviewer that ran
// against "summary A" and finishes after "summary B" landed must be refused:
// otherwise an old proof would silently bless new content.
func TestReviewCannotBindToAnAssertionChangedMidReview(t *testing.T) {
	st, fact, root := setup(t)
	// The reviewer starts here; the claim moves while the proof runs (the model
	// itself is not the writer in production, but the store API is the same).
	reviewed := *fact
	if _, err := st.UpdateFact(reviewed.ID, func(f *model.Fact) { f.Summary = "普通用户可读全部订单" }); err != nil {
		t.Fatal(err)
	}
	v := New(st, &stubExec{body: `{"verdict":"pass","notes":"复现了"}`}, root)
	_, err := v.Store.ReviewFact(model.Review{
		FactID: reviewed.ID, Verdict: "pass", ClaimVersion: reviewed.ClaimVersion, ClaimHash: reviewed.ClaimHash,
	})
	if !errors.Is(err, store.ErrReviewConflict) {
		t.Fatalf("stale verdict error = %v, want ErrReviewConflict", err)
	}
	if got, _ := st.FactByID(reviewed.ID); got.Status != "unverified" {
		t.Fatalf("stale verdict applied: %q", got.Status)
	}
	if reviews := st.Reviews(reviewed.ID); len(reviews) != 0 {
		t.Fatalf("stale verdict was recorded: %+v", reviews)
	}
}

func TestReviewPassRecordsTheCurrentClaimVersion(t *testing.T) {
	st, fact, root := setup(t)
	v := New(st, &stubExec{body: `{"verdict":"pass","notes":"复现了"}`}, root)
	review, err := v.Run(context.Background(), fact.ID)
	if err != nil {
		t.Fatal(err)
	}
	if review.ClaimVersion != fact.ClaimVersion || review.ClaimHash != fact.ClaimHash {
		t.Fatalf("review bound to v%d %q, want v%d %q", review.ClaimVersion, review.ClaimHash, fact.ClaimVersion, fact.ClaimHash)
	}
	if got, _ := st.FactByID(fact.ID); got.Status != "verified" {
		t.Fatalf("verified status = %q", got.Status)
	}
	// A display rename does not invalidate the recorded proof.
	if _, err := st.UpdateFact(fact.ID, func(f *model.Fact) { f.Title = "改个名字" }); err != nil {
		t.Fatal(err)
	}
	reviews := st.Reviews(fact.ID)
	if len(reviews) != 1 || reviews[0].ClaimVersion != 1 {
		t.Fatalf("reviews = %+v", reviews)
	}
}

var _ = executor.Result{}
