package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/kksty/tianwu/internal/retrieval"
	"github.com/kksty/tianwu/internal/store"
)

func (s *Server) archivePlaybook(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Archived  *bool     `json:"archived"`
		UpdatedAt time.Time `json:"updated_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if req.Archived == nil {
		writeErr(w, 400, "archived is required")
		return
	}
	out, err := s.Store.ArchivePlaybook(r.PathValue("id"), req.UpdatedAt, *req.Archived)
	if err != nil {
		code := 400
		if errors.Is(err, store.ErrPlaybookChanged) {
			code = 409
		}
		if errors.Is(err, os.ErrNotExist) {
			code = 404
		}
		writeErr(w, code, err.Error())
		return
	}
	writeJSON(w, 200, out)
}
func (s *Server) playbookHistory(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	out, err := retrieval.History(s.Store, "playbook", r.PathValue("id"), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		retrievalError(w, err)
		return
	}
	writeJSON(w, 200, out)
}
