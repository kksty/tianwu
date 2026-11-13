package executor

import "testing"

// The samples below are protojson for cyber-harness's aop.Event with
// UseProtoNames: the payload key equals the oneof field name (docs/11-protocol.md).
// Content is itself a oneof, so a text part is {"text":{"text":"…"}} — the
// nesting is not decoration, and reading it flat silently produced empty
// messages for a long time.
func TestParseEventMessage(t *testing.T) {
	line := `{"id":"e1","session_id":"s1","turn_id":"t1","seq":3,"message":{"role":"assistant","content":[{"text":{"text":"先看 "}},{"text":{"text":"/admin"}}]}}`
	ev := parseEvent(line)
	if ev.Kind != KindMessage || ev.Role != "assistant" {
		t.Fatalf("event = %+v", ev)
	}
	if ev.Text != "先看 /admin" {
		t.Fatalf("text = %q", ev.Text)
	}
	if len(ev.Raw) == 0 {
		t.Fatal("raw line must be kept")
	}
}

// A flat text part is not what the harness emits, but a fixture or a future
// producer may use it, and dropping the whole message because of the shape
// would be the same bug in the other direction.
func TestParseEventMessageFlatText(t *testing.T) {
	ev := parseEvent(`{"seq":3,"message":{"role":"assistant","content":[{"text":"x"}]}}`)
	if ev.Kind != KindMessage || ev.Text != "x" {
		t.Fatalf("event = %+v", ev)
	}
}

func TestParseEventToolCall(t *testing.T) {
	line := `{"seq":4,"tool_call":{"id":"c1","name":"curl","kind":"bash","working_directory":"/case"}}`
	ev := parseEvent(line)
	if ev.Kind != KindToolCall || ev.Tool != "curl" {
		t.Fatalf("event = %+v", ev)
	}
}

func TestParseEventToolResult(t *testing.T) {
	ok := parseEvent(`{"seq":5,"tool_result":{"call_id":"c1","name":"curl","output":[{"text":{"text":"HTTP/1.1 200 OK"}}]}}`)
	if ok.Kind != KindToolResult || !ok.OK || ok.Tool != "curl" {
		t.Fatalf("event = %+v", ok)
	}
	if ok.Text != "HTTP/1.1 200 OK" {
		t.Fatalf("text = %q", ok.Text)
	}
	bad := parseEvent(`{"seq":6,"tool_result":{"call_id":"c2","name":"curl","is_error":true}}`)
	if bad.Kind != KindToolResult || bad.OK {
		t.Fatalf("is_error must not read as ok: %+v", bad)
	}
}

// The execution review pairs a result with its call and measures elapsed time
// from duration_ms. Both fields arrive as protojson (the 64-bit duration is a
// string), so the parser must keep them instead of dropping them like it used
// to; without them every review row is an unpaired, timeless result.
func TestParseEventToolCallResultIdentityAndDuration(t *testing.T) {
	call := parseEvent(`{"seq":4,"tool_call":{"id":"call-1","name":"bash","kind":"bash"}}`)
	if call.Kind != KindToolCall || call.CallID != "call-1" {
		t.Fatalf("tool_call = %+v", call)
	}
	stringDuration := parseEvent(`{"seq":5,"tool_result":{"call_id":"call-1","name":"bash","duration_ms":"250","output":[{"text":{"text":"ok"}}]}}`)
	if stringDuration.Kind != KindToolResult || stringDuration.CallID != "call-1" {
		t.Fatalf("tool_result lost its call id: %+v", stringDuration)
	}
	if stringDuration.DurationMs != 250 {
		t.Fatalf("duration = %d, want the string-encoded 250", stringDuration.DurationMs)
	}
	numericDuration := parseEvent(`{"seq":6,"tool_result":{"call_id":"call-2","duration_ms":25}}`)
	if numericDuration.DurationMs != 25 {
		t.Fatalf("numeric duration = %d, want 25", numericDuration.DurationMs)
	}
	// Absent must stay 0 (not reported), and a failed result must still be
	// recognisable by ok=false rather than a missing duration.
	failed := parseEvent(`{"seq":7,"tool_result":{"name":"bash","is_error":true}}`)
	if failed.OK || failed.DurationMs != 0 || failed.CallID != "" {
		t.Fatalf("failed result = %+v", failed)
	}
}

func TestParseEventStatusCarriesState(t *testing.T) {
	ev := parseEvent(`{"seq":7,"status":{"state":"compacting context (82%)"}}`)
	if ev.Kind != KindStatus || ev.State != "compacting context (82%)" || ev.Text == "" {
		t.Fatalf("status event = %+v", ev)
	}
}

func TestParseEventTurnEnded(t *testing.T) {
	// protojson writes int64 as a string; both shapes must read the same.
	ev := parseEvent(`{"seq":9,"turn_ended":{"stop_reason":"completed","usage":{"input_tokens":"10","output_tokens":"4","total_tokens":"14","model":"m"}}}`)
	if ev.Kind != KindTurnEnd || ev.StopReason != "completed" || !ev.OK || ev.Tokens != 14 {
		t.Fatalf("event = %+v", ev)
	}
	numeric := parseEvent(`{"seq":9,"turn_ended":{"stop_reason":"completed","usage":{"total_tokens":14}}}`)
	if numeric.Tokens != 14 {
		t.Fatalf("numeric total_tokens = %d", numeric.Tokens)
	}
	failed := parseEvent(`{"seq":9,"turn_ended":{"stop_reason":"error","error":{"code":"upstream","message":"502"}}}`)
	if failed.OK || failed.StopReason != "error" {
		t.Fatalf("failed turn = %+v", failed)
	}
}

func TestParseEventUsageIsAStringInProtojson(t *testing.T) {
	ev := parseEvent(`{"seq":9,"usage":{"input_tokens":"5703","output_tokens":"12","total_tokens":"5715"}}`)
	if ev.Kind != KindUsage || ev.Tokens != 5715 {
		t.Fatalf("event = %+v (int64 fields arrive as strings)", ev)
	}
}

func TestParseEventContextTokens(t *testing.T) {
	// The harness puts the context size in usage.detail.context_tokens; the
	// console uses it for the live pressure bar. context_window rides with it so
	// the control plane measures against the harness's resolved window (D80).
	usage := parseEvent(`{"seq":1,"usage":{"total_tokens":"5703","detail":{"context_tokens":"12345","context_window":"1000000"}}}`)
	if usage.Kind != KindUsage || usage.ContextTokens != 12345 {
		t.Fatalf("usage = %+v", usage)
	}
	if usage.ContextWindow != 1000000 {
		t.Fatalf("usage window = %d", usage.ContextWindow)
	}
	ended := parseEvent(`{"seq":2,"turn_ended":{"stop_reason":"completed","context_tokens":"22000","usage":{"total_tokens":"22","detail":{"context_window":"1000000"}}}}`)
	if ended.ContextTokens != 22000 || ended.ContextWindow != 1000000 {
		t.Fatalf("turn_ended = %+v", ended)
	}
	var res Result
	absorb(&res, usage)
	absorb(&res, ended)
	if res.ContextTokens != 22000 {
		t.Fatalf("result context = %d", res.ContextTokens)
	}
	if res.ContextWindow != 1000000 {
		t.Fatalf("result window = %d", res.ContextWindow)
	}
}

func TestParseEventErrorAndUnknown(t *testing.T) {
	ev := parseEvent(`{"seq":2,"error":{"code":"provider_auth","message":"bad key"}}`)
	if ev.Kind != KindError || ev.Text == "" {
		t.Fatalf("event = %+v", ev)
	}
	// A payload we do not model must stay raw instead of being misread.
	unknown := parseEvent(`{"seq":3,"provider_frame":{"x":1}}`)
	if unknown.Kind != KindRaw {
		t.Fatalf("unknown payload = %+v", unknown)
	}
	// Non-JSON noise (a stray print, a crash message) is raw too.
	if noise := parseEvent("panic: something"); noise.Kind != KindRaw {
		t.Fatalf("noise = %+v", noise)
	}
}

func TestAbsorbKeepsRunFacts(t *testing.T) {
	var res Result
	absorb(&res, parseEvent(`{"message":{"role":"assistant","content":[{"text":"做完了"}]}}`))
	absorb(&res, parseEvent(`{"usage":{"total_tokens":100}}`))
	absorb(&res, parseEvent(`{"turn_ended":{"stop_reason":"completed","usage":{"total_tokens":180}}}`))

	if res.Text != "做完了" {
		t.Fatalf("text = %q", res.Text)
	}
	if res.Tokens != 180 {
		t.Fatalf("tokens = %d, want the turn total 180", res.Tokens)
	}
	if res.StopReason != "completed" || res.IsError {
		t.Fatalf("result = %+v", res)
	}
}

// The handoff line is measured against the last context size, not the peak: a
// pre-compaction peak would schedule a second handoff after the context reset.
func TestAbsorbTracksFinalContextAndCompaction(t *testing.T) {
	var res Result
	absorb(&res, Event{Kind: KindUsage, ContextTokens: 90000})
	absorb(&res, Event{Kind: KindStatus, State: "compact_start"})
	absorb(&res, Event{Kind: KindStatus, State: "compact_end"})
	absorb(&res, Event{Kind: KindUsage, ContextTokens: 20000})
	if res.ContextTokens != 90000 {
		t.Fatalf("peak = %d, want 90000", res.ContextTokens)
	}
	if res.FinalContextTokens != 20000 {
		t.Fatalf("final = %d, want the post-compaction 20000", res.FinalContextTokens)
	}
	if !res.Compacted {
		t.Fatal("compact_end did not mark the turn as compacted")
	}
}

// When compaction is the last thing the turn did, its pre-compaction size is
// stale: the next round must read "unknown", not the old 110k.
func TestAbsorbDropsPreCompactionSize(t *testing.T) {
	var res Result
	absorb(&res, Event{Kind: KindUsage, ContextTokens: 110000})
	absorb(&res, Event{Kind: KindStatus, State: "compact_end"})
	if res.FinalContextTokens != 0 {
		t.Fatalf("final = %d, want unknown after compaction", res.FinalContextTokens)
	}
}

// Only compaction statuses count; a budget warning is not a lost context.
func TestAbsorbIgnoresOtherStatuses(t *testing.T) {
	var res Result
	absorb(&res, Event{Kind: KindStatus, State: "token_budget_warning"})
	if res.Compacted {
		t.Fatal("token_budget_warning marked the turn as compacted")
	}
	if res.LastState != "token_budget_warning" {
		t.Fatalf("last state = %q", res.LastState)
	}
	if !IsCompactionState("compacting context (82%)") {
		t.Fatal("an older harness's wording must still be recognised")
	}
}
