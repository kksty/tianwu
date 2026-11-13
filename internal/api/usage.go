package api

// Token summaries. Every run contributes once: the subtree total is the
// union of the root intent and all descendants by run id, never a sum of
// per-level sums. Missing usage remains explicitly unknown.

import (
	"net/http"

	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/model"
)

type usageSummary struct {
	Runs              int `json:"runs"`
	InputTokens       int `json:"input_tokens"`
	OutputTokens      int `json:"output_tokens"`
	CachedInputTokens int `json:"cached_input_tokens"`
	CacheWriteTokens  int `json:"cache_write_tokens"`
	TotalTokens       int `json:"total_tokens"`
	Requests          int `json:"requests"`
	MissingUsage      int `json:"missing_usage"`
	UnknownRuns       int `json:"unknown_usage_runs"`
}

func summarizeUsage(runs []model.Run) usageSummary {
	out := usageSummary{}
	seen := map[string]bool{}
	for _, r := range runs {
		if seen[r.ID] {
			continue
		}
		seen[r.ID] = true
		out.Runs++
		if r.Usage.Known {
			out.InputTokens += r.Usage.InputTokens
			out.OutputTokens += r.Usage.OutputTokens
			out.CachedInputTokens += r.Usage.CachedInputTokens
			out.CacheWriteTokens += r.Usage.CacheWriteTokens
			out.TotalTokens += r.Usage.TotalTokens
			out.Requests += r.Usage.Requests
			out.MissingUsage += r.Usage.Missing
		} else {
			out.UnknownRuns++
			out.TotalTokens += r.Tokens
		}
	}
	return out
}

func (s *Server) intentUsage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	in, ok := s.Store.GetIntent(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "意图不存在")
		return
	}
	if c := s.caller(r); c.agent && c.participant != collab.ParticipantID(id) {
		writeErr(w, http.StatusForbidden, "只能查看自己的用量")
		return
	}
	subtreeIDs := map[string]bool{id: true}
	for _, d := range s.Store.Descendants(id) {
		subtreeIDs[d.ID] = true
	}
	var direct, subtree []model.Run
	for _, run := range s.Store.Runs() {
		if !subtreeIDs[run.IntentID] {
			continue
		}
		subtree = append(subtree, run)
		if run.IntentID == id {
			direct = append(direct, run)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"intent_id": in.ID,
		"direct":    summarizeUsage(direct),
		"subtree":   summarizeUsage(subtree),
	})
}
