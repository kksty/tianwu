package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/verifier"
)

// The review state on readiness is the session-visible half of the verifier:
// it says what the session should do (wait / ask the operator) without exposing
// the review run, its output or the queue. It exists because a blocked
// verified_only direction otherwise invites the commander to invent a review
// workaround.
func TestReadinessReportsBlockedReviewState(t *testing.T) {
	t.Run("queued", func(t *testing.T) {
		s, p, f, _ := proofPolicyFixture(t)
		in, err := p.Store.CreateIntent(model.Intent{Title: "consumer", FromFactIDs: []string{f.ID}})
		if err != nil {
			t.Fatal(err)
		}
		got := s.readiness(p.Store.Snapshot(), in, s.premisePolicy())
		if len(got.Premises) != 1 || got.Premises[0].Review == nil || got.Premises[0].Review.State != reviewStateQueued {
			t.Fatalf("queued premise = %+v", got.Premises)
		}
		if !strings.Contains(got.ReviewHint, "会话不能触发复核") {
			t.Fatalf("queued hint = %q", got.ReviewHint)
		}
	})

	t.Run("running", func(t *testing.T) {
		s, p, f, _ := proofPolicyFixture(t)
		ex := &reviewProbeExec{
			blockFirst: true,
			entered:    make(chan struct{}, 1),
			release:    make(chan struct{}),
		}
		s.Verifier = verifier.New(p.Store, ex, s.CaseRoot)
		s.Verifier.Policy = s.premisePolicy
		if err := s.Verifier.Start(context.Background(), f.ID, nil); err != nil {
			t.Fatal(err)
		}
		waitFor(t, 5*time.Second, func() bool {
			select {
			case <-ex.entered:
				return true
			default:
				return false
			}
		})
		t.Cleanup(func() {
			close(ex.release)
			waitFor(t, 5*time.Second, func() bool { return !s.Verifier.Busy(f.ID) })
		})
		in, err := p.Store.CreateIntent(model.Intent{Title: "consumer", FromFactIDs: []string{f.ID}})
		if err != nil {
			t.Fatal(err)
		}
		got := s.readiness(p.Store.Snapshot(), in, s.premisePolicy())
		if len(got.Premises) != 1 || got.Premises[0].Review == nil || got.Premises[0].Review.State != reviewStateRunning {
			t.Fatalf("running premise = %+v", got.Premises)
		}
	})

	t.Run("cooling", func(t *testing.T) {
		s, p, f, _ := proofPolicyFixture(t)
		s.noteReviewFailure(f, time.Now())
		in, err := p.Store.CreateIntent(model.Intent{Title: "consumer", FromFactIDs: []string{f.ID}})
		if err != nil {
			t.Fatal(err)
		}
		got := s.readiness(p.Store.Snapshot(), in, s.premisePolicy())
		if len(got.Premises) != 1 || got.Premises[0].Review == nil || got.Premises[0].Review.State != reviewStateCooling {
			t.Fatalf("cooling premise = %+v", got.Premises)
		}
	})

	t.Run("paused", func(t *testing.T) {
		s, p, f, _ := proofPolicyFixture(t)
		s.SetReviewsPaused(true)
		t.Cleanup(func() { s.SetReviewsPaused(false) })
		in, err := p.Store.CreateIntent(model.Intent{Title: "consumer", FromFactIDs: []string{f.ID}})
		if err != nil {
			t.Fatal(err)
		}
		got := s.readiness(p.Store.Snapshot(), in, s.premisePolicy())
		if len(got.Premises) != 1 || got.Premises[0].Review == nil || got.Premises[0].Review.State != reviewStatePaused {
			t.Fatalf("paused premise = %+v", got.Premises)
		}
		if !strings.Contains(got.ReviewHint, "请操作员继续复核") {
			t.Fatalf("paused hint = %q", got.ReviewHint)
		}
	})

	t.Run("disabled", func(t *testing.T) {
		s, p, f, _ := proofPolicyFixture(t)
		s.Verifier = nil
		in, err := p.Store.CreateIntent(model.Intent{Title: "consumer", FromFactIDs: []string{f.ID}})
		if err != nil {
			t.Fatal(err)
		}
		got := s.readiness(p.Store.Snapshot(), in, s.premisePolicy())
		if len(got.Premises) != 1 || got.Premises[0].Review == nil || got.Premises[0].Review.State != reviewStateDisabled {
			t.Fatalf("disabled premise = %+v", got.Premises)
		}
		if !strings.Contains(got.ReviewHint, "请操作员处理") {
			t.Fatalf("disabled hint = %q", got.ReviewHint)
		}
	})

	t.Run("failed", func(t *testing.T) {
		s, p, source, _ := proofPolicyFixture(t)
		fact, err := p.Store.AddFact(model.Fact{
			IntentID: source.IntentID, Kind: "vuln", Title: "failed premise",
			Origin: model.OriginDeclared, EvidenceIDs: source.EvidenceIDs,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.Store.ReviewFact(model.Review{FactID: fact.ID, Verdict: "needs_evidence", Notes: "无法复现"}); err != nil {
			t.Fatal(err)
		}
		in, err := p.Store.CreateIntent(model.Intent{Title: "consumer", FromFactIDs: []string{fact.ID}})
		if err != nil {
			t.Fatal(err)
		}
		got := s.readiness(p.Store.Snapshot(), in, s.premisePolicy())
		if len(got.Premises) != 1 || got.Premises[0].Review == nil || got.Premises[0].Review.State != reviewStateFailed {
			t.Fatalf("failed premise = %+v", got.Premises)
		}
		if !strings.Contains(got.ReviewHint, "复核未通过") {
			t.Fatalf("failed hint = %q", got.ReviewHint)
		}
	})

	t.Run("ineligible", func(t *testing.T) {
		s, p, _, _ := proofPolicyFixture(t)
		note, err := p.Store.AddFact(model.Fact{Kind: "note", Title: "historical suggestion"})
		if err != nil {
			t.Fatal(err)
		}
		got := s.readiness(p.Store.Snapshot(), model.Intent{FromFactIDs: []string{note.ID}}, model.PremiseVerifiedOnly)
		if len(got.Premises) != 1 || got.Premises[0].Review == nil || got.Premises[0].Review.State != reviewStateIneligible {
			t.Fatalf("ineligible premise = %+v", got.Premises)
		}
	})

}
