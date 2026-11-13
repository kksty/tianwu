package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

func (s *Server) registerActions(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/actions", s.listActions)
	mux.HandleFunc("POST /api/actions", s.beginAction)
	mux.HandleFunc("POST /api/actions/{id}/result", s.resolveAction)
}

func (s *Server) listActions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"actions": s.Store.Actions(r.URL.Query().Get("intent_id"))})
}

func (s *Server) actionCaller(w http.ResponseWriter, r *http.Request, intentID string) (string, bool) {
	in, ok := s.Store.GetIntent(intentID)
	if !ok {
		writeErr(w, 404, "意图不存在")
		return "", false
	}
	c := s.caller(r)
	if c.agent && (c.participant != collab.ParticipantID(intentID) || c.runID != in.RunID || in.Status != model.StatusRunning) {
		writeErr(w, 403, "只有本方向当前执行者可以登记或核对动作结果")
		return "", false
	}
	if c.agent {
		return c.runID, true
	}
	return "", true
}

func (s *Server) beginAction(w http.ResponseWriter, r *http.Request) {
	if s.paused.Load() || s.deleting.Load() {
		writeErr(w, 409, "项目已暂停或正在删除：不能登记新的外部动作")
		return
	}
	var req struct {
		IntentID  string `json:"intent_id"`
		Key       string `json:"idempotency_key"`
		Name      string `json:"name"`
		Reference string `json:"reference"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		writeErr(w, 400, "请求体不是 JSON")
		return
	}
	runID, ok := s.actionCaller(w, r, req.IntentID)
	if !ok {
		return
	}
	if runID == "" {
		in, _ := s.Store.GetIntent(req.IntentID)
		runID = in.RunID
	}
	a, created, err := s.Store.BeginAction(req.IntentID, runID, req.Key, req.Name, req.Reference)
	if err != nil {
		status := 400
		if errors.Is(err, store.ErrIdempotencyConflict) {
			status = 409
		}
		writeErr(w, status, err.Error())
		return
	}
	status := 200
	if created {
		status = 201
	}
	writeJSON(w, status, map[string]any{"action": a, "created": created})
}

func (s *Server) resolveAction(w http.ResponseWriter, r *http.Request) {
	a, ok := s.Store.GetAction(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "外部动作不存在")
		return
	}
	runID, ok := s.actionCaller(w, r, a.IntentID)
	if !ok {
		return
	}
	var req struct {
		Version   int    `json:"version"`
		State     string `json:"state"`
		Result    string `json:"result"`
		ResultRef string `json:"result_ref"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		writeErr(w, 400, "请求体不是 JSON")
		return
	}
	out, err := s.Store.ResolveAction(a.ID, req.Version, req.State, req.Result, req.ResultRef, runID)
	if err != nil {
		status := 400
		if errors.Is(err, store.ErrActionConflict) {
			status = 409
		}
		writeErr(w, status, err.Error())
		return
	}
	writeJSON(w, 200, out)
	// No commander wake: only explicitly registered waits become ready.
	go s.pump()
}
