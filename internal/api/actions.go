package api

import (
	"encoding/json"
	"net/http"

	"github.com/kksty/tianwu/internal/ledger"
	"github.com/kksty/tianwu/internal/model"
)

// completeGoal is the commander's evidence-gated close. The operator's
// POST /api/goals/{id}/met pins by hand; this one enforces the same rule the
// decider always had: cite facts, at least one with evidence, and a vuln/cred
// citation needs a verified fact (D55, D78). It also requires the one-line
// reason (note), because the ledger and the console have to be able to answer
// "why is this met" long after the commander's session is gone (D96).
func (s *Server) completeGoal(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		From []string `json:"from"`
		Note string   `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid json: "+err.Error())
		return
	}
	if s.premisePolicy() == model.PremiseVerifiedOnly {
		ready := s.readiness(s.Store.Snapshot(), model.Intent{FromFactIDs: req.From}, s.premisePolicy())
		if !ready.Ready {
			writeErr(w, 400, "目标依据缺少当前证明或原件校验失败，请先核验")
			return
		}
	}
	g, err := ledger.CompleteGoal(s.Store, id, req.From, req.Note, s.premisePolicy())
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	// The decision log is where the round's reasons are read back; recording
	// the statement there said "what was closed" but never "why" (D96).
	s.recordAction("complete", g.ID, g.Note)
	writeJSON(w, 200, g)
}

// nominatePlaybook is the commander's write path into the project's knowledge
// table: candidate only, provenance required, never overwrites a confirmed row.
// The rules live in ledger so the decider's action and this endpoint cannot
// drift apart.
func (s *Server) nominatePlaybook(w http.ResponseWriter, r *http.Request) {
	var req model.Playbook
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid json: "+err.Error())
		return
	}
	pb, changed, err := ledger.NominatePlaybook(s.Store, req)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s.recordAction("playbook", pb.ID, pb.Title)
	writeJSON(w, 200, map[string]any{"playbook": pb, "changed": changed})
}
