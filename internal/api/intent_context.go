package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/kksty/tianwu/internal/store"
)

// taskContextRequest is the whole selection, not a delta: an update replaces
// references, pins and interest entities together. version is the optimistic
// token the caller read, so two editors cannot silently overwrite each other.
type taskContextRequest struct {
	Version    uint64   `json:"version"`
	References []string `json:"references"`
	Pins       []string `json:"pins"`
	Entities   []string `json:"entities"`
}

// updateTaskContext lets the operator (no run) or the current participant run
// change what an existing direction explicitly reads. It does not touch the
// intent's hard premises (from_fact_ids), the project scope or its state.
func (s *Server) updateTaskContext(w http.ResponseWriter, r *http.Request) {
	var req taskContextRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "上下文请求无效")
		return
	}
	runID := ""
	if id, ok := identityOf(r); ok {
		if id.IntentID != r.PathValue("id") {
			writeJSON(w, http.StatusForbidden, map[string]any{
				"error": "只能更新自己的方向", "code": "context_denied",
			})
			return
		}
		runID = id.RunID
	}
	in, err := s.Store.UpdateTaskContext(r.PathValue("id"), runID, store.TaskContext{
		Version: req.Version, References: req.References, Pins: req.Pins, Entities: req.Entities,
	})
	if err != nil {
		code := http.StatusInternalServerError
		switch {
		case errors.Is(err, store.ErrContextConflict):
			code = http.StatusConflict
		case errors.Is(err, store.ErrContextNotFound):
			code = http.StatusNotFound
		case errors.Is(err, store.ErrFactRefMissing):
			code = http.StatusBadRequest
		}
		writeErr(w, code, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, in)
}
