package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/kksty/tianwu/internal/traffic"
	"google.golang.org/protobuf/encoding/protojson"
)

func (s *Server) listHTTPFlows(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	offset, _ := strconv.Atoi(q.Get("offset"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	s.artifactMu.Lock()
	rows, total, err := traffic.ListCaptures(s.CaseRoot, q.Get("q"), q.Get("run"), offset, limit)
	s.artifactMu.Unlock()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"flows": rows, "total": total})
}

func (s *Server) httpFlow(w http.ResponseWriter, r *http.Request) {
	s.artifactMu.Lock()
	meta, flow, err := traffic.ReadCapture(s.CaseRoot, r.URL.Query().Get("ref"))
	s.artifactMu.Unlock()
	if err != nil {
		writeErr(w, 404, "流量原件不可用："+err.Error())
		return
	}
	b, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(flow)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="http-flow.json"`)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(b)
		return
	}
	writeJSON(w, 200, map[string]any{"record": meta, "flow": json.RawMessage(b)})
}
