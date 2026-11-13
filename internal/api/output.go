package api

import (
	"encoding/json"
	"github.com/kksty/tianwu/internal/executor"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Tool text is read from the native AOP event already archived by the adapter;
// the 400-character console preview is not used as the original evidence.
func (s *Server) runOutput(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	run, ok := s.Store.GetRun(id)
	if !ok {
		writeErr(w, 404, "run not found")
		return
	}
	if identity, agent := identityOf(r); agent && identity.IntentID != run.IntentID {
		writeErr(w, 403, "只能读取自己方向的工具输出；其他方向通过事实证据引用读取")
		return
	}
	call := r.URL.Query().Get("call_id")
	if call == "" {
		writeErr(w, 400, "call_id is required")
		return
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if offset < 0 {
		writeErr(w, 400, "invalid offset")
		return
	}
	if limit <= 0 {
		limit = 4000
	}
	if limit > 16000 {
		limit = 16000
	}
	f, err := os.Open(filepath.Join(s.DataDir, "runs", id+".jsonl"))
	if err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	for {
		var ev executor.Event
		err = decoder.Decode(&ev)
		if err == io.EOF {
			break
		}
		if err != nil {
			writeErr(w, 500, "invalid event journal: "+err.Error())
			return
		}
		if ev.Kind != executor.KindToolResult || ev.CallID != call {
			continue
		}
		var raw struct {
			ToolResult struct {
				Output []struct {
					Text struct {
						Text string `json:"text"`
					} `json:"text"`
				} `json:"output"`
			} `json:"tool_result"`
		}
		if err = json.Unmarshal(ev.Raw, &raw); err != nil {
			writeErr(w, 422, "native event is unavailable")
			return
		}
		var text strings.Builder
		for _, v := range raw.ToolResult.Output {
			text.WriteString(v.Text.Text)
		}
		runes := []rune(s.redactSecrets(text.String()))
		if offset > len(runes) {
			writeErr(w, 400, "offset outside output")
			return
		}
		end := offset + limit
		if end > len(runes) {
			end = len(runes)
		}
		writeJSON(w, 200, map[string]any{"run_id": id, "call_id": call, "text": string(runes[offset:end]), "offset": offset, "next_offset": end, "total": len(runes), "has_more": end < len(runes), "source": "aop.tool_result"})
		return
	}
	writeErr(w, 404, "tool result not found")
}
