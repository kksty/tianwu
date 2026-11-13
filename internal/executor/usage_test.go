package executor

import (
	"testing"

	"github.com/kksty/tianwu/internal/model"
)

// The ledger's usage row is the same shape for every purpose. The mapping must
// keep the breakdown and the completeness signal intact: a result the harness
// measured is not confused with one it never reported.
func TestUsageOfMapsProviderObservations(t *testing.T) {
	res := Result{
		InputTokens: 40, OutputTokens: 9, CachedInputTokens: 12, CacheWriteTokens: 2,
		Tokens: 51, UsageRequests: 3, UsageMissing: 1, UsageKnown: true,
	}
	want := model.Usage{
		InputTokens: 40, OutputTokens: 9, CachedInputTokens: 12, CacheWriteTokens: 2,
		TotalTokens: 51, Requests: 3, Missing: 1, Known: true,
	}
	if got := UsageOf(res); got != want {
		t.Fatalf("UsageOf = %+v, want %+v", got, want)
	}
}

func TestUsageOfKeepsUnknownUnknown(t *testing.T) {
	// Nothing was measured: Known=false and no fabricated zero counters.
	if got := UsageOf(Result{}); got.Known || got != (model.Usage{}) {
		t.Fatalf("empty result = %+v, want an unknown usage row", got)
	}
}

// The model name comes only from the harness's session report; any other event
// text is not a model binding.
func TestSessionModelOnlyReadsSessionStarted(t *testing.T) {
	if got := SessionModel(Event{Kind: KindSession, Text: "deepseek-flash"}); got != "deepseek-flash" {
		t.Fatalf("SessionModel = %q", got)
	}
	if got := SessionModel(Event{Kind: KindMessage, Text: "deepseek-flash"}); got != "" {
		t.Fatalf("a message event must not be read as a model: %q", got)
	}
	if got := SessionModel(Event{Kind: KindSession, Text: "  "}); got != "" {
		t.Fatalf("blank session text must stay unknown: %q", got)
	}
}
