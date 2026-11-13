package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/kksty/tianwu/internal/model"
	"net/http"
	"strings"

	"github.com/kksty/tianwu/internal/project"
	"github.com/kksty/tianwu/internal/store"
)

// ErrPremiseUnverified is the strict-policy refusal: a vuln/cred premise has
// not passed a review at its current claim version. It is a 400 with
// code=premise_unverified, not a 409: the request is well-formed, the ledger
// simply does not accept that assertion as an actionable predecessor yet.
var ErrPremiseUnverified = errors.New("严格前提模式下，vuln/cred 前提必须已复核通过")

// premisePolicy is the effective admission mode of the current project.
func (s *Server) premisePolicy() string {
	if s.Project == nil {
		return project.PremisePolicyStandard
	}
	return s.Project.PremisePolicyView()
}

// checkPremiseRefs validates the from_fact_ids a caller is about to bind to a
// new direction. Existence is mandatory in every mode (the child/spawn paths
// already enforced it; the top-level create path did not). Strict mode
// additionally requires a vuln/cred premise to be verified at its current
// claim version. The error preserves which rule failed so the HTTP mapping can
// give the caller a machine code.
func (s *Server) checkPremiseRefs(ids []string) error {
	policy := s.premisePolicy()
	strict := policy == project.PremisePolicyStrict
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		f, ok := s.Store.FactByID(id)
		if !ok {
			return fmt.Errorf("%w：%s", store.ErrFactRefMissing, id)
		}
		if policy == model.PremiseVerifiedOnly {
			if f.Status == "rejected" || !model.ReviewableKind(f.Kind) {
				return fmt.Errorf("%w：%s（%s）", ErrPremiseUnverified, id, model.PremiseIssue(f, policy))
			}
			// Persist the intended relation while its proof is pending. Actual
			// dispatch remains gated, and the queue can now see what needs review.
			continue
		}
		if !strict {
			continue
		}
		switch f.Kind {
		case "vuln", "cred":
			if f.Status != "verified" {
				// Do not suggest moving the fact into context_fact_ids to pass
				// admission: that silently converts a required premise into a
				// reading reference while the downstream action still depends on
				// it (audit F12.2). A real premise waits for its current claim
				// version to be reviewed; an unverified lead can drive a separate
				// verification direction instead.
				return fmt.Errorf("%w：%s（%s，当前 %s，断言 v%d）；需先让该断言当前版本复核通过，或明确该任务不依赖它并说明无法完成的部分；不得通过改字段/改标签绕过准入",
					ErrPremiseUnverified, id, f.Title, f.Status, f.ClaimVersion)
			}
		}
	}
	return nil
}

// writePremiseError maps the premise admission errors to 400 plus a machine
// code. The message names the fact and the policy, so a commander or operator
// can act without guessing.
func writePremiseError(w http.ResponseWriter, err error) {
	code := "premise_invalid"
	switch {
	case errors.Is(err, store.ErrFactRefMissing):
		code = "fact_ref_missing"
	case errors.Is(err, ErrPremiseUnverified):
		code = "premise_unverified"
	}
	writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "code": code})
}

// premisePolicyResponse is the operator's view of the project admission mode.
type premisePolicyResponse struct {
	PremisePolicy string `json:"premise_policy"`
	Effective     string `json:"effective"`
}

func (s *Server) getPremisePolicy(w http.ResponseWriter, r *http.Request) {
	pol := s.premisePolicy()
	writeJSON(w, http.StatusOK, premisePolicyResponse{PremisePolicy: pol, Effective: pol})
}

// updatePremisePolicy accepts {"premise_policy":"standard|strict"}. Only the
// operator holds this route: the projector's participant and commander surfaces
// do not register it, and the agent gate hides unknown project routes.
func (s *Server) updatePremisePolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PremisePolicy string `json:"premise_policy"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求体不是合法 JSON", "code": "premise_policy_invalid"})
		return
	}
	if s.Project == nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "项目未绑定：不能在实例级设置前提政策", "code": "premise_policy_unavailable"})
		return
	}
	pol, err := s.Project.SetPremisePolicy(req.PremisePolicy)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "code": "premise_policy_invalid"})
		return
	}
	s.SchedulePendingReviews()
	if s.Decider != nil {
		s.Decider.Wake("operator")
	}
	writeJSON(w, http.StatusOK, premisePolicyResponse{PremisePolicy: pol, Effective: pol})
}
