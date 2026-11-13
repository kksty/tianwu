package aopexec

import (
	"testing"

	"github.com/kksty/tianwu/internal/executor"
)

func TestCanceledTurnRetainsAllObservedCallsAndFinalTotalsDoNotDoubleCount(t *testing.T) {
	var res executor.Result
	for range 3 {
		absorb(&res, executor.Event{Kind: executor.KindUsage, Tokens: 120, InputTokens: 100, OutputTokens: 20, CachedInputTokens: 40, CacheWriteTokens: 5, UsageRequests: 1})
	}
	absorb(&res, executor.Event{Kind: executor.KindUsage, UsageRequests: 1, UsageMissing: 1})
	if res.Tokens != 360 || res.InputTokens != 300 || res.OutputTokens != 60 || res.CachedInputTokens != 120 || res.CacheWriteTokens != 15 || res.UsageRequests != 4 || res.UsageMissing != 1 || !res.UsageKnown {
		t.Fatalf("interrupted turn lost usage: %+v", res)
	}
	absorb(&res, executor.Event{Kind: executor.KindTurnEnd, StopReason: "canceled"})
	if res.Tokens != 360 || res.UsageRequests != 4 || !res.UsageKnown {
		t.Fatalf("absent final usage erased observations: %+v", res)
	}
	absorb(&res, executor.Event{Kind: executor.KindTurnEnd, OK: true, Tokens: 360, InputTokens: 300, OutputTokens: 60, CachedInputTokens: 120, CacheWriteTokens: 15, UsageRequests: 4, UsageMissing: 1})
	if res.Tokens != 360 || res.InputTokens != 300 || res.UsageRequests != 4 {
		t.Fatalf("final usage double counted: %+v", res)
	}
}

func TestPerCallUsageWithoutCountersIsStillRecorded(t *testing.T) {
	var res executor.Result
	for range 2 {
		absorb(&res, executor.Event{Kind: executor.KindUsage, Tokens: 10, InputTokens: 8, OutputTokens: 2})
	}
	if res.Tokens != 20 || !res.UsageKnown || res.UsageRequests != 0 {
		t.Fatalf("legacy observed usage: %+v", res)
	}
}
