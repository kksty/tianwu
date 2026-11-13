package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
)

// scriptedExec replays a fixed event list, so a test can fabricate exactly the
// structured tool_call the warning hook looks at - without a model or a harness.
type scriptedExec struct {
	events []executor.Event
}

func (e *scriptedExec) Name() string { return "scripted" }

func (e *scriptedExec) Run(_ context.Context, _ executor.Request, emit func(executor.Event)) (executor.Result, error) {
	for _, ev := range e.events {
		emit(ev)
	}
	return executor.Result{ExitCode: 0, StopReason: "completed"}, nil
}

func subagentEvent() executor.Event {
	return executor.Event{Kind: executor.KindToolCall, Tool: "subagent", Text: `{"prompt":"分析一下"}`}
}

func newScriptedServer(t *testing.T, ex *scriptedExec) (*Server, model.Intent) {
	t.Helper()
	s, p := newBindingServer(t)
	if err := p.SetNodeBinding([]string{"local"}, "local"); err != nil {
		t.Fatal(err)
	}
	s.LocalNode = "local"
	s.Exec = ex
	in, err := p.Store.CreateIntent(model.Intent{Title: "用测试事件驱动一轮"})
	if err != nil {
		t.Fatal(err)
	}
	return s, in
}

// A structured tool_call naming the harness subagent tool becomes one warning
// on the run row and one warning event in the journal - exactly once, even if
// the model calls the tool several times.
func TestNativeSubagentToolCallRecordsWarningOnce(t *testing.T) {
	ex := &scriptedExec{events: []executor.Event{
		{Kind: executor.KindToolCall, Tool: "read", Text: "README.md"},
		subagentEvent(), subagentEvent(), subagentEvent(),
		{Kind: executor.KindTurnEnd, StopReason: "completed", OK: true},
	}}
	s, in := newScriptedServer(t, ex)
	runID, err := s.StartRun(in.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, s.Project, runID)

	run, ok := s.Store.GetRun(runID)
	if !ok {
		t.Fatal("run missing")
	}
	count := 0
	for _, w := range run.Warnings {
		if w == nativeSubagentWarning {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("native subagent warning count = %d in %v", count, run.Warnings)
	}
	for _, needle := range []string{"subagent", "账本", "不可取消", "不计费", "spawn"} {
		if !strings.Contains(nativeSubagentWarning, needle) {
			t.Fatalf("warning text lacks %q: %s", needle, nativeSubagentWarning)
		}
	}

	lines := readJournalFileLines(t, s.DataDir, runID)
	warnings := 0
	for _, obj := range lines {
		if obj["kind"] != "warning" {
			continue
		}
		warnings++
		text, _ := obj["text"].(string)
		if text != nativeSubagentWarning {
			t.Fatalf("warning event text = %q", text)
		}
	}
	if warnings != 1 {
		t.Fatalf("warning events = %d, want 1: %v", warnings, lines)
	}

	// The console read path sees it too (same journal, no second reader).
	rec := call(t, s, http.MethodGet, "/api/runs/"+runID+"/events", nil)
	if rec.Code != 200 || strings.Count(rec.Body.String(), `"kind":"warning"`) != 1 {
		t.Fatalf("runEvents = %d %s", rec.Code, rec.Body.String())
	}
}

// The boundary is the structured tool name, not text: a bash call whose command
// mentions "ioa send"/"subagent", or a model message containing the word, is not
// evidence of a ledger-external session and must not raise the warning.
func TestNormalToolCallsDoNotWarn(t *testing.T) {
	ex := &scriptedExec{events: []executor.Event{
		{Kind: executor.KindToolCall, Tool: "bash",
			Text: `{"command":"ioa send worker-a \"subagent 是内置工具\""}`},
		{Kind: executor.KindMessage, Role: "assistant", Text: "我会调用 subagent 吗？"},
		{Kind: executor.KindToolCall, Tool: "tool_result", Text: `{"name":"subagent"}`},
		{Kind: executor.KindTurnEnd, StopReason: "completed", OK: true},
	}}
	s, in := newScriptedServer(t, ex)
	runID, err := s.StartRun(in.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, s.Project, runID)

	run, _ := s.Store.GetRun(runID)
	if len(run.Warnings) != 0 {
		t.Fatalf("non-subagent calls produced warnings: %v", run.Warnings)
	}
	for _, obj := range readJournalFileLines(t, s.DataDir, runID) {
		if obj["kind"] == "warning" {
			t.Fatalf("non-subagent calls produced a warning event: %v", obj)
		}
	}
}

// The native-subagent warning is additive: an evidence/handoff warning already
// on the run stays, and both become journal events. Exercised at the helper
// boundary so the coexistence does not depend on a particular fake harness.
func TestNativeSubagentWarningCoexistsWithEvidenceWarnings(t *testing.T) {
	s, _ := newBindingServer(t)
	const existing = "声明的证据文件不存在：missing-1.txt"
	run, err := s.Store.AddRun(model.Run{IntentID: "in_warn", Warnings: []string{existing}})
	if err != nil {
		t.Fatal(err)
	}
	openTestJournal(t, s, run.ID)
	defer s.closeJournal(run.ID)

	s.noteNativeSubagentUsage(run.ID, subagentEvent())
	s.noteNativeSubagentUsage(run.ID, subagentEvent()) // dedupe
	s.flushRunWarnings(run.ID)

	got, _ := s.Store.GetRun(run.ID)
	if len(got.Warnings) != 2 || got.Warnings[0] != existing || got.Warnings[1] != nativeSubagentWarning {
		t.Fatalf("warnings = %v", got.Warnings)
	}
	lines := readJournalFileLines(t, s.DataDir, run.ID)
	warnings := 0
	for _, obj := range lines {
		if obj["kind"] == "warning" {
			warnings++
		}
	}
	if warnings != 2 {
		t.Fatalf("warning events = %d, want 2: %v", warnings, lines)
	}
}

// A commander round has no dispatch settle path; its warnings must still reach
// the journal (task-17 mechanism, flushed in StartCommander).
func TestCommanderNativeSubagentWarningIsJournaled(t *testing.T) {
	s, _, st := newCommanderServer(t)
	s.Exec = &scriptedExec{events: []executor.Event{
		subagentEvent(),
		{Kind: executor.KindTurnEnd, StopReason: "completed", OK: true},
	}}
	if _, _, err := s.StartCommander(context.Background(), "## 本轮", false); err != nil {
		t.Fatal(err)
	}
	runs := st.Runs()
	if len(runs) != 1 {
		t.Fatalf("runs = %+v", runs)
	}
	found := false
	for _, w := range runs[0].Warnings {
		if w == nativeSubagentWarning {
			found = true
		}
	}
	if !found {
		t.Fatalf("commander run warnings = %v", runs[0].Warnings)
	}
	lines := readJournalFileLines(t, s.DataDir, runs[0].ID)
	warnings := 0
	for _, obj := range lines {
		if obj["kind"] == "warning" {
			warnings++
		}
	}
	if warnings != 1 {
		t.Fatalf("commander warning events = %d: %v", warnings, lines)
	}
}

// An old run row with no warnings is untouched: no warning row grows, and
// flushing it is a no-op (the compatibility rule task-17 established).
func TestOldRunWithoutWarningsStaysClean(t *testing.T) {
	s, _ := newBindingServer(t)
	run, err := s.Store.AddRun(model.Run{IntentID: "in_old"})
	if err != nil {
		t.Fatal(err)
	}
	s.flushRunWarnings(run.ID)
	got, _ := s.Store.GetRun(run.ID)
	if got.Warnings != nil {
		t.Fatalf("old run grew warnings: %v", got.Warnings)
	}
	if j := s.journal(run.ID); j != nil {
		t.Fatal("flush should not open a journal")
	}
}
