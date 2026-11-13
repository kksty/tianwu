package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/resource"
)

// The resource registry is a read-only projection over owners that already
// exist: browser/material entries in the project's `.access/` tree, operator
// waits and model sessions in the ledger, and external actions in the ledger.
// It must not become a second owner: acquire/release/recover still go through
// POST /api/access/{id}/lease, operator receipts through the intent's operator
// endpoint, and action results through the action CAS endpoint. Reads here never
// acquire, release, replay or resolve anything.

// resourceRuntime supplies the two pieces of browser state that are not in the
// persisted lease: a native command in flight and an unconfirmed control marker.
// Both are guarded by accessMu, which the handlers below hold while projecting.
type resourceRuntime struct{ s *Server }

func (r resourceRuntime) BrowserCommandActive(accessID string) bool {
	return r.s.browserActive[accessID]
}

func (r resourceRuntime) BrowserControlPending(accessID string) (bool, error) {
	control, err := r.s.browserControlState(accessID)
	return control.Pending, err
}

// resourceProjection builds the projection for this project. Callers hold
// accessMu when the runtime adapter can be consulted.
func (s *Server) resourceProjection() *resource.Service {
	return &resource.Service{
		Cases:   s.CaseRoot,
		Ledger:  s.Store,
		Runtime: resourceRuntime{s},
	}
}

// registerResourceRoutes mounts GET /api/resources and GET /api/resources/{id}.
// The Lead wires it into the operator/commander project mux and the participant
// mux (internal/api/api.go Register and internal/api/agent.go registerAgent);
// identity decides visibility exactly as it does for the access endpoints.
func (s *Server) registerResourceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/resources", s.listResources)
	mux.HandleFunc("GET /api/resources/{id}", s.getResource)
}

// listResources returns the project's execution-site resources.
//
// A participant may only read its own direction: an omitted intent is forced to
// the caller's intent and an explicit different one is refused, so it cannot
// enumerate other directions. Operator/commander requests carry no participant
// identity and see the project.
func (s *Server) listResources(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	intentID := strings.TrimSpace(query.Get("intent"))
	kind := strings.TrimSpace(query.Get("kind"))
	if kind != "" && !model.IsResourceKind(kind) {
		writeErr(w, http.StatusBadRequest, "未知的资源类型："+kind)
		return
	}
	if id, agent := identityOf(r); agent {
		if intentID == "" {
			intentID = id.IntentID
		}
		if intentID != id.IntentID {
			writeErr(w, http.StatusForbidden, "只能读取自己方向的资源")
			return
		}
	}
	s.accessMu.Lock()
	rows, err := s.resourceProjection().List(intentID, kind)
	s.accessMu.Unlock()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取资源投影失败："+err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"resources": rows,
		"total":     len(rows),
		"intent_id": intentID,
		"kind":      kind,
	})
}

// getResource returns one projection by its persisted identity. A participant
// asking for another direction's resource gets the same 404 as a missing one:
// the response must not confirm that another direction exists.
func (s *Server) getResource(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, _, err := resource.SplitID(id); err != nil {
		writeErr(w, http.StatusBadRequest, "资源 id 无效")
		return
	}
	s.accessMu.Lock()
	row, ok, err := s.resourceProjection().Get(id)
	s.accessMu.Unlock()
	switch {
	case errors.Is(err, resource.ErrInvalidID):
		writeErr(w, http.StatusBadRequest, "资源 id 无效")
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, "读取资源投影失败："+err.Error())
		return
	}
	if ident, agent := identityOf(r); agent {
		if !ok || row.IntentID == "" || row.IntentID != ident.IntentID {
			writeErr(w, http.StatusNotFound, "资源不存在")
			return
		}
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "资源不存在")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"resource": row})
}
