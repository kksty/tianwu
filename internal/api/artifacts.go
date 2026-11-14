package api

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/kksty/tianwu/internal/artifact"
	"github.com/kksty/tianwu/internal/executor"
)

// supportProductHidden reports whether this request must not see a native
// product (a scanner result or an HTTP capture) attributed to runID. Report and
// verification runs are operator-side post-processing: the products captured
// from their tools are the review/report output, which
// docs/01-architecture.md §8 keeps out of both the commander's and the
// executor's API view. The decision comes from the run's persisted purpose,
// never from a file name or a tool name. An unknown runID is hidden for a
// harness session as well: a product whose owner cannot be shown to be an
// engagement run is not part of the battlefield (fail closed).
func (s *Server) supportProductHidden(r *http.Request, runID string) bool {
	if !harnessSession(r) {
		return false
	}
	if s.Store == nil {
		return true
	}
	run, ok := s.Store.GetRun(runID)
	if !ok {
		return true
	}
	return supportPurpose(run.Purpose)
}

func (s *Server) listArtifacts(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	s.artifactMu.Lock()
	rows, err := artifact.List(s.CaseRoot, r.URL.Query().Get("run"), r.URL.Query().Get("q"), limit)
	s.artifactMu.Unlock()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if harnessSession(r) {
		visible := make([]artifact.Record, 0, len(rows))
		for _, row := range rows {
			if s.supportProductHidden(r, row.RunID) {
				continue
			}
			visible = append(visible, row)
		}
		rows = visible
	}
	writeJSON(w, 200, map[string]any{"artifacts": rows})
}

func (s *Server) artifactData(w http.ResponseWriter, r *http.Request) {
	s.artifactMu.Lock()
	record, raw, err := artifact.Read(s.CaseRoot, r.URL.Query().Get("ref"))
	s.artifactMu.Unlock()
	if err != nil {
		writeErr(w, 404, "原生结果不可用："+err.Error())
		return
	}
	if s.supportProductHidden(r, record.RunID) {
		// The same 404 a missing record gets: a harness session must not be able
		// to confirm that a support run produced this native result.
		writeErr(w, 404, "原生结果不可用：没有这条记录")
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
