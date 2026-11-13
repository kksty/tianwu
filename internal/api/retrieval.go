package api

import (
	"encoding/json"
	"errors"
	"github.com/kksty/tianwu/internal/retrieval"
	"net/http"
	"strconv"
	"strings"
)

func factQuery(r *http.Request) retrieval.FactQuery {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	return retrieval.FactQuery{Mode: q.Get("mode"), Q: q.Get("q"), Kind: q.Get("kind"), Intent: q.Get("intent"), Status: q.Get("status"), Tag: q.Get("tag"), Cursor: q.Get("cursor"), Limit: limit, Ascending: q.Get("order") == "oldest"}
}
func retrievalError(w http.ResponseWriter, e error) {
	var incomplete *retrieval.IndexIncomplete
	if errors.As(e, &incomplete) {
		writeJSON(w, 409, map[string]any{"error": e.Error(), "code": "semantic_index_incomplete", "indexed": incomplete.Indexed, "total": incomplete.Total, "index_url": "/api/retrieval/index"})
		return
	}
	code := http.StatusBadRequest
	if strings.Contains(e.Error(), "expired") || strings.Contains(e.Error(), "conflict") {
		code = http.StatusConflict
	}
	if strings.Contains(e.Error(), "unconfigured") {
		code = http.StatusNotImplemented
	}
	writeErr(w, code, e.Error())
}
func (s *Server) changes(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	since, _ := strconv.ParseUint(r.URL.Query().Get("since"), 10, 64)
	out, e := retrieval.Changes(s.Store, r.URL.Query().Get("cursor"), limit, since)
	if e != nil {
		retrievalError(w, e)
		return
	}
	writeJSON(w, 200, out)
}
func (s *Server) graphQuery(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	edgeLimit, _ := strconv.Atoi(q.Get("edge_limit"))
	boundaryLimit, _ := strconv.Atoi(q.Get("boundary_limit"))
	maxBytes, _ := strconv.Atoi(q.Get("max_bytes"))
	depth := 2
	if q.Has("depth") {
		depth, _ = strconv.Atoi(q.Get("depth"))
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	roots := []string{}
	if q.Get("root") != "" {
		roots = strings.Split(q.Get("root"), ",")
	}
	out, e := retrieval.Graph(s.Store, retrieval.GraphQuery{Roots: roots, Direction: q.Get("direction"), Depth: depth, Order: q.Get("order"), Projection: q.Get("projection"), Cursor: q.Get("cursor"), Limit: limit, EdgeLimit: edgeLimit, BoundaryLimit: boundaryLimit, MaxBytes: maxBytes})
	if e != nil {
		retrievalError(w, e)
		return
	}
	if _, agent := identityOf(r); agent {
		out.Goals = nil
		for i := range out.Intents {
			in := &out.Intents[i]
			in.Doc = ""
			in.Notes = nil
			in.Interjections = nil
			in.Handoff = nil
			in.Eval = ""
		}
	}
	writeJSON(w, 200, out)
}
func contextReader(r *http.Request) string {
	if id, ok := identityOf(r); ok {
		return "intent:" + id.IntentID
	}
	return "commander"
}
func (s *Server) workingContext(w http.ResponseWriter, r *http.Request) {
	out, e := retrieval.WorkingContext(s.Store, contextReader(r))
	if e != nil {
		retrievalError(w, e)
		return
	}
	writeJSON(w, 200, out)
}
func (s *Server) acknowledgeContext(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"delivery_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid json: "+err.Error())
		return
	}
	if e := s.Store.AcknowledgeDelivery(contextReader(r), req.ID); e != nil {
		retrievalError(w, e)
		return
	}
	writeJSON(w, 200, map[string]any{"acknowledged": req.ID})
}
func (s *Server) factHistory(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	out, err := retrieval.History(s.Store, "fact", r.PathValue("id"), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		retrievalError(w, err)
		return
	}
	writeJSON(w, 200, out)
}
func (s *Server) graphExport(w http.ResponseWriter, r *http.Request) {
	db, v := s.Store.ReadView()
	w.Header().Set("Content-Disposition", "attachment; filename=graph.json")
	writeJSON(w, 200, map[string]any{"snapshot_version": v, "facts": db.Facts, "intents": db.Intents, "goals": db.Goals, "evidence": db.Evidence, "reviews": db.Reviews})
}

func (s *Server) experienceCandidates(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	out, err := retrieval.Experiences(s.Store, retrieval.ExperienceQuery{Q: q.Get("q"), Intent: q.Get("intent"), Cursor: q.Get("cursor"), Limit: limit})
	if err != nil {
		retrievalError(w, err)
		return
	}
	writeJSON(w, 200, out)
}
