package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/kksty/tianwu/internal/kb"
)

func (s *Server) worldKB(w http.ResponseWriter, r *http.Request) {
	if s.KB == nil {
		writeErr(w, 503, "知识库未配置")
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	entries := s.KB.Search(q.Get("q"), strings.Split(q.Get("tags"), ","), limit)
	writeJSON(w, 200, map[string]any{"entries": entries, "revisions": kb.Revisions(entries)})
}

func (s *Server) nominateWorldKB(w http.ResponseWriter, r *http.Request) {
	id, ok := identityOf(r)
	if !ok {
		writeErr(w, 401, "缺少协作身份")
		return
	}
	s.saveWorldNomination(w, r, fmt.Sprintf("project:%s participant:%s run:%s", id.ProjectID, id.ParticipantID, id.RunID))
}

func (s *Server) saveWorldNomination(w http.ResponseWriter, r *http.Request, origin string) {
	if s.KB == nil {
		writeErr(w, 503, "知识库未配置")
		return
	}
	var c kb.Candidate
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&c); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	c.ID, c.Version = "", 0
	c.Origin = origin
	row, err := s.KB.SaveCandidate(c)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, kb.ErrConflict) {
			status = http.StatusConflict
		}
		writeErr(w, status, err.Error())
		return
	}
	writeJSON(w, 201, row)
}

func (s *Server) nominateProjectWorldKB(w http.ResponseWriter, r *http.Request) {
	projectID := ""
	if s.Project != nil {
		projectID = s.Project.ID
	}
	s.saveWorldNomination(w, r, fmt.Sprintf("project:%s actor:%s", projectID, actorOf(r)))
}
