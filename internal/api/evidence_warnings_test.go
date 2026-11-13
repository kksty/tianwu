package api

// End-to-end: a run that declares an unopenable evidence path leaves structured
// warnings on the run row, in its event log, on the board, and in the next
// dispatch's brief (task-17).

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
)

type handoffWarnExec struct {
	mu        sync.Mutex
	prompts   []string
	failFirst bool
}

func (e *handoffWarnExec) Name() string { return "handoff-warn" }

func (e *handoffWarnExec) Run(_ context.Context, req executor.Request, emit func(executor.Event)) (executor.Result, error) {
	e.mu.Lock()
	e.prompts = append(e.prompts, req.Prompt)
	n := len(e.prompts)
	e.mu.Unlock()
	dir := filepath.Join(req.WorkDir, ".tw")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return executor.Result{ExitCode: 1}, err
	}
	line := `{"kind":"note","title":"缺失证据","summary":"s","evidence":["missing-` +
		strconv.Itoa(n) + `.txt"]}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "facts.jsonl"), []byte(line), 0o644); err != nil {
		return executor.Result{ExitCode: 1}, err
	}
	emit(executor.Event{Kind: executor.KindSession, Text: "actual-model"})
	if e.failFirst && n == 1 {
		return executor.Result{ExitCode: 1, Output: "boom"}, errors.New("boom")
	}
	return executor.Result{ExitCode: 0, StopReason: "completed"}, nil
}

func (e *handoffWarnExec) promptAt(i int) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if i >= len(e.prompts) {
		return ""
	}
	return e.prompts[i]
}

func TestHandoffWarningsReachRunEventsBoardAndNextPrompt(t *testing.T) {
	s, p := newBindingServer(t)
	if err := p.SetNodeBinding([]string{"local"}, "local"); err != nil {
		t.Fatal(err)
	}
	ex := &handoffWarnExec{failFirst: true}
	s.Exec = ex

	in, err := p.Store.CreateIntent(model.Intent{Title: "验证 /admin"})
	if err != nil {
		t.Fatal(err)
	}
	runID, err := s.StartRun(in.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, p, runID)

	run, ok := p.Store.GetRun(runID)
	if !ok || len(run.Warnings) == 0 {
		t.Fatalf("run warnings = %+v", run)
	}
	if !strings.Contains(strings.Join(run.Warnings, "\n"), "missing-1.txt") {
		t.Fatalf("warning does not name the path: %v", run.Warnings)
	}

	// The run event stream carries a warning event, so an operator reading the
	// log sees the same problem.
	events := call(t, s, http.MethodGet, "/api/runs/"+runID+"/events", nil)
	if events.Code != http.StatusOK || !strings.Contains(events.Body.String(), `"kind":"warning"`) ||
		!strings.Contains(events.Body.String(), "missing-1.txt") {
		t.Fatalf("run events = %d %s", events.Code, events.Body.String())
	}

	// /api/runs returns the run row with warnings.
	runs := call(t, s, http.MethodGet, "/api/runs", nil)
	if !strings.Contains(runs.Body.String(), `"warnings"`) || !strings.Contains(runs.Body.String(), "missing-1.txt") {
		t.Fatalf("/api/runs = %s", runs.Body.String())
	}

	// The board shows the warning summary without opening the run list.
	boardResp := call(t, s, http.MethodGet, "/api/board", nil)
	if !strings.Contains(boardResp.Body.String(), "handoff_warnings") ||
		!strings.Contains(boardResp.Body.String(), "missing-1.txt") {
		t.Fatalf("board = %s", boardResp.Body.String())
	}

	// The retried direction's brief carries the previous run's warnings.
	runID2, err := s.StartRun(in.ID)
	if err != nil {
		t.Fatalf("retry after a failed run: %v", err)
	}
	waitRun(t, p, runID2)
	second := ex.promptAt(1)
	if !strings.Contains(second, "## 上一轮交接问题") {
		t.Fatalf("next brief has no warning section:\n%s", second)
	}
	if !strings.Contains(second, "missing-1.txt") {
		t.Fatalf("next brief does not name the bad path:\n%s", second)
	}
	// The first dispatch had nothing to correct.
	if first := ex.promptAt(0); strings.Contains(first, "## 上一轮交接问题") {
		t.Fatalf("the first dispatch carried a warning section:\n%s", first)
	}
}
