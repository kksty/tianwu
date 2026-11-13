package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/kksty/tianwu/internal/kb"
	"github.com/kksty/tianwu/internal/store"
)

func (s *Server) getPlaybook(w http.ResponseWriter, r *http.Request) {
	p, ok := s.Store.PlaybookByID(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "战场知识不存在")
		return
	}
	writeJSON(w, 200, p)
}

func (s *Server) deletePlaybook(w http.ResponseWriter, r *http.Request) {
	var in struct {
		UpdatedAt time.Time `json:"updated_at"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&in); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := s.Store.DeletePlaybook(r.PathValue("id"), in.UpdatedAt); err != nil {
		status := 500
		if errors.Is(err, os.ErrNotExist) {
			status = 404
		}
		if errors.Is(err, store.ErrPlaybookChanged) {
			status = 409
		}
		writeErr(w, status, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": true})
}

func (s *Server) nominatePlaybookKB(w http.ResponseWriter, r *http.Request) {
	if s.KB == nil {
		writeErr(w, 503, "知识库未配置")
		return
	}
	var in struct {
		kb.Candidate
		SourceUpdatedAt time.Time `json:"source_updated_at"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&in); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	p, ok := s.Store.PlaybookByID(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "来源战场知识已删除")
		return
	}
	if !p.UpdatedAt.Equal(in.SourceUpdatedAt) {
		writeErr(w, 409, "来源战场知识已更新，请刷新页面核对")
		return
	}
	projectID := ""
	if s.Project != nil {
		projectID = s.Project.ID
	}
	c := in.Candidate
	c.ID, c.Version = "", 0
	c.Origin = fmt.Sprintf("operator project:%s playbook:%s version:%s", projectID, p.ID, p.UpdatedAt.UTC().Format(time.RFC3339Nano))
	row, err := s.KB.SaveCandidate(c)
	if err != nil {
		status := 400
		if errors.Is(err, kb.ErrConflict) {
			status = 409
		}
		writeErr(w, status, err.Error())
		return
	}
	writeJSON(w, 201, row)
}
