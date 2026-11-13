package api

// Context accounting without window prediction (D152, revised).
//
// The harness owns window resolution, the pre-request feasibility check, context
// compaction and overflow recovery: it reports the resolved window and the
// context size it measured (usage.detail.context_window / context_tokens,
// compact_end), refuses a request that cannot fit, and compacts once on an
// overflow before failing. Tianwu does not predict any of that.
//
// What remains here is only reuse of the harness's own numbers:
//   - continuationHistoryTokens: the post-compaction context size the harness
//     reported for the run being continued (or its peak when no final size was
//     reported);
//   - commanderHistoryTokens: the same number for the last commander round.
//
// If a prompt still exceeds the window, the harness reports a structured
// context-limit error; dispatch then retries ONCE with a rebuilt brief that
// omits optional summaries (stable ids/pointers stay). See context_limit.go.

import (
	"strings"

	"github.com/kksty/tianwu/internal/model"
)

// continuationHistoryTokens is what a continued session already holds, as
// reported by the harness at the end of the previous turn. The harness may
// compact mid-turn, so FinalContextTokens (post-compaction) wins over the peak
// ContextTokens. 0 means the harness never reported one (unknown, not empty).
func (s *Server) continuationHistoryTokens(intentID, runID string) int {
	if intentID == "" || runID == "" {
		return 0
	}
	if run, ok := s.Store.GetRun(runID); ok {
		if run.FinalContextTokens > 0 {
			return run.FinalContextTokens
		}
		if run.ContextTokens > 0 {
			return run.ContextTokens
		}
	}
	return 0
}

// commanderHistoryTokens is the context the commander session already holds: the
// last size the harness reported for a commander round. A compacted round's
// final size wins over its peak (same rule as worker continuations).
func (s *Server) commanderHistoryTokens(currentRunID string) int {
	runs := s.Store.Runs()
	for i := len(runs) - 1; i >= 0; i-- {
		r := runs[i]
		if r.ID == currentRunID || r.Purpose != "commander" {
			continue
		}
		if r.FinalContextTokens > 0 {
			return r.FinalContextTokens
		}
		if r.ContextTokens > 0 {
			return r.ContextTokens
		}
	}
	return 0
}

// runeLen is the prompt-length unit the per-section caps use.
func runeLen(s string) int { return len([]rune(s)) }

// noteContextSlimRetry records, once per run, why this dispatch used the
// degraded brief. Warnings are diagnostics: they are journaled at settle and
// never change a run's status.
func (s *Server) noteContextSlimRetry(runID, text string) {
	text = strings.TrimSpace(text)
	if runID == "" || text == "" {
		return
	}
	_, _ = s.Store.UpdateRun(runID, func(r *model.Run) {
		for _, w := range r.Warnings {
			if w == text {
				return
			}
		}
		if len(r.Warnings) >= maxRunWarningRows {
			return
		}
		r.Warnings = append(r.Warnings, text)
	})
}
