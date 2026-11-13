package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The executor's view is whatever is in its case directory: the brief it was
// handed, the read-only graph, the files it produced, and what it declared or
// asked. Nothing extra is stored for this page — it reads the same artifacts the
// run itself used, so the view cannot drift from reality.
const (
	contextPromptMax = 512 << 10
	contextGraphMax  = 2 << 20
	contextLinesMax  = 300
)

type contextFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

func (s *Server) runContext(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	run, ok := s.Store.GetRun(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "没有这条运行"})
		return
	}
	in, ok := s.Store.GetIntent(run.IntentID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "运行对应的意图不存在"})
		return
	}
	if strings.ContainsAny(in.ID, `/\`) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "意图 id 不合法"})
		return
	}
	dir := filepath.Join(s.CaseRoot, in.ID)
	tw := filepath.Join(dir, ".tw")

	prompt := readCapped(filepath.Join(tw, "prompt.txt"), contextPromptMax)
	steer := []string{}
	if files, _ := filepath.Glob(filepath.Join(tw, "steer-*.txt")); len(files) > 0 {
		sort.Strings(files)
		for _, p := range files {
			if text := readCapped(p, contextPromptMax); text != "" {
				steer = append(steer, text)
			}
		}
	}

	var graph json.RawMessage
	if b, err := os.ReadFile(filepath.Join(tw, "graph.json")); err == nil && len(b) <= contextGraphMax && json.Valid(b) {
		graph = json.RawMessage(b)
	}
	declared := readJSONLines(filepath.Join(tw, "facts.jsonl"), contextLinesMax, contextGraphMax)
	asks := readJSONLines(filepath.Join(tw, "ask.jsonl"), contextLinesMax, 256<<10)

	writeJSON(w, http.StatusOK, map[string]any{
		"run_id":        run.ID,
		"intent_id":     in.ID,
		"intent_title":  in.Title,
		"intent_status": in.Status,
		"status":        run.Status,
		"stop_reason":   run.StopReason,
		"tokens":        run.Tokens,
		"prompt":        prompt,
		"steer":         steer,
		"graph":         graph,
		"declared":      declared,
		"asks":          asks,
		"files":         listCaseFiles(dir),
	})
}

// readCapped returns a file as text, or "" when it is missing or too large.
// A truncated brief is worse than an honest empty panel: the size is visible in
// files[], so the UI can say why.
func readCapped(path string, max int64) string {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() || st.Size() > max {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

// readJSONLines parses a JSONL file, dropping malformed lines instead of
// failing the whole panel. Facts.jsonl is written line by line by the executor;
// one bad line must not hide the rest.
func readJSONLines(path string, maxLines int, maxBytes int64) []json.RawMessage {
	text := readCapped(path, maxBytes)
	if text == "" {
		return nil
	}
	out := make([]json.RawMessage, 0, 8)
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !json.Valid([]byte(line)) {
			continue
		}
		out = append(out, json.RawMessage(line))
		if len(out) >= maxLines {
			break
		}
	}
	return out
}

// listCaseFiles lists the top level of the case plus the .tw/ bookkeeping files
// an executor can reach, so the page can show what exists even when a file is
// too large to render.
func listCaseFiles(dir string) []contextFile {
	out := []contextFile{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	add := func(prefix, name string, size int64) {
		out = append(out, contextFile{Name: prefix + name, Size: size})
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if info, err := e.Info(); err == nil {
			add("", e.Name(), info.Size())
		}
	}
	if tw, err := os.ReadDir(filepath.Join(dir, ".tw")); err == nil {
		for _, e := range tw {
			if e.IsDir() {
				continue
			}
			if info, err := e.Info(); err == nil {
				add(".tw/", e.Name(), info.Size())
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
