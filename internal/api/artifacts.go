package api

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/kksty/tianwu/internal/artifact"
	"github.com/kksty/tianwu/internal/executor"
)

func (s *Server) listArtifacts(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	s.artifactMu.Lock()
	defer s.artifactMu.Unlock()
	rows, err := artifact.List(s.CaseRoot, r.URL.Query().Get("run"), r.URL.Query().Get("q"), limit)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"artifacts": rows})
}

func (s *Server) artifactData(w http.ResponseWriter, r *http.Request) {
	s.artifactMu.Lock()
	defer s.artifactMu.Unlock()
	record, raw, err := artifact.Read(s.CaseRoot, r.URL.Query().Get("ref"))
	if err != nil {
		writeErr(w, 404, "原生结果不可用："+err.Error())
		return
	}
	// Native data is a download, never an active document under console origin.
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="native-result.bin"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Tianwu-Source-Type", record.MediaType)
	_, _ = w.Write(raw)
}

func (s *Server) captureArtifact(ev *executor.Event, runID, intentID string) error {
	if ev.Native == nil {
		return nil
	}
	s.artifactMu.Lock()
	defer s.artifactMu.Unlock()
	r, err := artifact.Save(s.CaseRoot, runID, intentID, ev.NativeEventID, ev.Native)
	if err != nil {
		return fmt.Errorf("保存原生结果失败：%w", err)
	}
	ev.ArtifactRef = r.Ref
	ev.Native = nil
	return nil
}
