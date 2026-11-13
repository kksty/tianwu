package executor

import (
	"strings"

	"github.com/kksty/tianwu/internal/model"
)

// UsageOf maps the provider observations an executor reports into the ledger's
// usage row. Worker dispatch, commander rounds and the auxiliary purposes
// (verifier/report) all record the same shape through this one function, so
// "what did this run cost" has one answer everywhere.
//
// Unknown stays unknown: when the harness reported no usage at all, Known is
// false and the zero counters are the absence of a measurement, not a measured
// zero. Callers must not print "0 tokens" for such a row (model.Usage's
// Known/Missing contract).
func UsageOf(res Result) model.Usage {
	return model.Usage{
		InputTokens:       res.InputTokens,
		OutputTokens:      res.OutputTokens,
		CachedInputTokens: res.CachedInputTokens,
		CacheWriteTokens:  res.CacheWriteTokens,
		TotalTokens:       res.Tokens,
		Requests:          res.UsageRequests,
		Missing:           res.UsageMissing,
		Known:             res.UsageKnown,
	}
}

// SessionModel returns the model the harness resolved for its session, when the
// event is the session-start record that reports it. It is the actual model the
// turn ran on (which may differ from a profile alias); empty means the harness
// did not report one, not that no model ran.
//
// A purpose run knows only what the harness tells it. It cannot know which saved
// profile the host picked for an unspecified binding, so purpose rows record
// Model here and leave Profile/ResolvedProfile/Provider empty rather than
// inventing a binding.
func SessionModel(ev Event) string {
	if ev.Kind != KindSession {
		return ""
	}
	return strings.TrimSpace(ev.Text)
}
