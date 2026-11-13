package api

import (
	"fmt"
	"net/http"

	"github.com/kksty/tianwu/internal/evidence"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

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
}

type intentReadiness struct {
	IntegrityChecked bool               `json:"integrity_checked"`
	IntentID         string             `json:"intent_id"`
	Policy           string             `json:"policy"`
	Ready            bool               `json:"ready"`
	Premises         []premiseReadiness `json:"premises"`
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
		out.Premises = append(out.Premises, row)
	}
	return out
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
