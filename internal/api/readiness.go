package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/kksty/tianwu/internal/evidence"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// Derived review lifecycle shown with a blocked premise under verified_only.
// It is deliberately not the review queue or a verifier run: it answers only
// whether this fact is waiting for, inside, or unable to receive an independent
// proof, so the commander can wait or ask the operator instead of probing the
// operator-only review surface (which stays 404 for a session).
const (
	reviewStateQueued     = "queued"
	reviewStateRunning    = "running"
	reviewStateCooling    = "cooling"
	reviewStatePaused     = "paused"
	reviewStateFailed     = "failed"
	reviewStateIneligible = "ineligible"
	reviewStateDisabled   = "disabled"
)

type premiseReview struct {
	State string `json:"state"`
}

type premiseReadiness struct {
	ID           string `json:"id"`
	Title        string `json:"title,omitempty"`
	ClaimVersion uint64 `json:"claim_version,omitempty"`
	Status       string `json:"status"`
	Usable       bool   `json:"usable"`
	Reason       string `json:"reason,omitempty"`
	ReviewID     string `json:"review_id,omitempty"`
	VerifierRun  string `json:"verifier_run,omitempty"`
	Integrity    string `json:"integrity"`
	// Review is present only when verified_only blocks this premise and review
	// could still change its usability. Standard policy omits it: there review is
	// not an admission gate, and a queued state would imply one.
	Review *premiseReview `json:"review,omitempty"`
}

type intentReadiness struct {
	IntegrityChecked bool               `json:"integrity_checked"`
	IntentID         string             `json:"intent_id"`
	Policy           string             `json:"policy"`
	Ready            bool               `json:"ready"`
	Premises         []premiseReadiness `json:"premises"`
	// ReviewHint is one operator-readable sentence for the blocking review
	// states above. The session cannot trigger a review; the hint says so and
	// tells it whether to wait or ask the operator.
	ReviewHint string `json:"review_hint,omitempty"`
}

// Readiness explains the same rule dispatch enforces. Integrity is a separate
// dimension: valid bytes cannot verify an assertion's meaning.
func (s *Server) readiness(db store.DB, in model.Intent, policy string, integrity ...bool) intentReadiness {
	checkBytes := len(integrity) == 0 || integrity[0]
	out := intentReadiness{IntentID: in.ID, Policy: policy, Ready: true, Premises: []premiseReadiness{}, IntegrityChecked: checkBytes}
	facts := map[string]model.Fact{}
	proofs := map[string]model.Evidence{}
	for _, f := range db.Facts {
		facts[f.ID] = f
	}
	for _, e := range db.Evidence {
		proofs[e.ID] = e
	}
	var reviewStates []string
	for _, id := range in.FromFactIDs {
		row := premiseReadiness{ID: id, Status: "missing", Integrity: "unknown", Reason: "前提记录不存在"}
		if f, ok := facts[id]; ok {
			row.Title, row.Status, row.ClaimVersion = f.Title, f.Status, f.ClaimVersion
			row.Reason = model.PremiseIssue(f, policy)
			if proof, proven := model.CurrentProof(f, db.Reviews); proven {
				row.ReviewID, row.VerifierRun = proof.ID, proof.VerifierRun
			} else if policy == model.PremiseVerifiedOnly && row.Reason == "" {
				row.Reason = "缺少绑定当前断言的独立核验收据"
			}
			if policy == model.PremiseVerifiedOnly && row.Reason == "" {
				row.Integrity = "not_checked"
				if checkBytes {
					row.Integrity = "intact"
				}
				for _, eid := range f.EvidenceIDs {
					e, exists := proofs[eid]
					if !exists || e.Digest == "" {
						row.Reason = "原件缺失或没有保全摘要：" + eid
						row.Integrity = "missing"
						break
					}
					if !checkBytes {
						continue
					}
					if err := evidence.Verify(s.CaseRoot, e); err != nil {
						row.Reason = fmt.Sprintf("证据 %s：%v", eid, err)
						row.Integrity = "invalid"
						break
					}
				}
			}
		}
		row.Usable = row.Reason == ""
		out.Ready = out.Ready && row.Usable
		f, ok := facts[id]
		switch {
		case ok && row.Status == "unverified" && model.ReviewableKind(f.Kind):
			// Review can still change this observation. Under standard the
			// hint says it does not gate dispatch; under verified_only the same
			// state explains why dispatch is blocked.
			row.Review = s.premiseReview(f)
			reviewStates = append(reviewStates, row.Review.State)
		case !row.Usable:
			// Missing, rejected, unreviewable or already-verified-without-receipt:
			// waiting for a review cannot make this premise usable.
			row.Review = &premiseReview{State: reviewStateIneligible}
			reviewStates = append(reviewStates, row.Review.State)
		}
		out.Premises = append(out.Premises, row)
	}
	if len(reviewStates) > 0 {
		out.ReviewHint = reviewHint(reviewStates)
	}
	return out
}

// reviewHint summarizes the blocking review states for a session. It stays a
// derived classification (not the queue or a verifier run) and always states the
// boundary: the session waits or asks the operator; it cannot trigger a review.
func reviewHint(states []string) string {
	labels := map[string]string{
		reviewStateRunning:    "进行中，等待即可",
		reviewStateQueued:     "排队中，等待即可",
		reviewStateCooling:    "冷却中，稍后自动重试",
		reviewStatePaused:     "已暂停，请操作员继续复核",
		reviewStateFailed:     "复核未通过，需补证据或修正断言后重试",
		reviewStateDisabled:   "未启用或未开启自动复核，请操作员处理",
		reviewStateIneligible: "不可独立复核，不能作为必要前提",
	}
	order := []string{
		reviewStateRunning, reviewStateQueued, reviewStateCooling,
		reviewStatePaused, reviewStateFailed, reviewStateDisabled, reviewStateIneligible,
	}
	counts := map[string]int{}
	for _, state := range states {
		counts[state]++
	}
	parts := make([]string, 0, len(order))
	for _, state := range order {
		if n := counts[state]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d 条%s", n, labels[state]))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "前提复核：" + strings.Join(parts, "；") + "。复核由控制面/操作员触发，会话不能触发复核。"
}

func (s *Server) getIntentReadiness(w http.ResponseWriter, r *http.Request) {
	db := s.Store.Snapshot()
	for _, in := range db.Intents {
		if in.ID == r.PathValue("id") {
			writeJSON(w, 200, s.readiness(db, in, s.premisePolicy(), r.URL.Query().Get("verify_bytes") == "1"))
			return
		}
	}
	writeErr(w, 404, "方向不存在")
}
