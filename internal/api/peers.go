package api

// Operator-facing peer policy: who is allowed to message whom in this project.
//
// The policy lives in the project manifest (internal/project), not in the
// message table: authorization is a property of the engagement that survives a
// restart, and dropping the project drops the rules with it. The default is
// open, so projects created before this endpoint keep working; switching to
// allowlist is an explicit operator act.

import (
	"encoding/json"
	"net/http"

	"github.com/kksty/tianwu/internal/project"
)

type peerPolicyResponse struct {
	PeerPolicy project.PeerPolicy `json:"peer_policy"`
	// Mode is the effective mode (open even when the manifest has no policy),
	// so a console does not have to re-derive the default.
	Mode string `json:"mode"`
}

func (s *Server) peerPolicyResponse() peerPolicyResponse {
	pol := project.PeerPolicy{Mode: project.PeerPolicyOpen}
	if s.Project != nil {
		pol = s.Project.PeerPolicyView()
	}
	return peerPolicyResponse{PeerPolicy: pol, Mode: pol.Mode}
}

// getPeerPolicy is a read-only view for the operator UI; it never exposes
// anything about individual messages, only the rule set.
func (s *Server) getPeerPolicy(w http.ResponseWriter, r *http.Request) {
	if s.Project == nil {
		writeErr(w, http.StatusServiceUnavailable, "项目未初始化：协作接口不可用")
		return
	}
	writeJSON(w, http.StatusOK, s.peerPolicyResponse())
}

// updatePeerPolicy replaces the rule set. An empty/garbage mode is refused
// instead of being treated as open: a typo must not silently widen or close the
// project's conversation.
func (s *Server) updatePeerPolicy(w http.ResponseWriter, r *http.Request) {
	if s.Project == nil {
		writeErr(w, http.StatusServiceUnavailable, "项目未初始化：协作接口不可用")
		return
	}
	var req project.PeerPolicy
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是 JSON")
		return
	}
	pol, err := s.Project.SetPeerPolicy(req)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, peerPolicyResponse{PeerPolicy: pol, Mode: pol.Mode})
}

// peerAllowed is the one place the message path asks whether a send is
// authorized. A bare test server (no project) has no policy, so it keeps the
// legacy open behaviour.
func (s *Server) peerAllowed(from, to string) bool {
	if s.Project == nil {
		return true
	}
	return s.Project.PeerAllowed(from, to)
}
