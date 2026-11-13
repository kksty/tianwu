package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/decider"
	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/project"
	"github.com/kksty/tianwu/internal/store"
)

// ---- model_stall detector (task-31, final-e2e I1) ----

func emptyAssistantEvent() executor.Event {
	return executor.Event{Kind: executor.KindMessage, Role: "assistant"}
}

func llmRequestEvent() executor.Event {
	return executor.Event{Kind: executor.KindStatus, State: "llm_request"}
}

// feedStallCycles emits n "empty assistant reply -> llm_request" cycles and
// reports whether any one of them tripped the detector.
func feedStallCycles(d *modelStallDetector, n int) bool {
	tripped := false
	for i := 0; i < n; i++ {
		d.observe(emptyAssistantEvent())
		if d.observe(llmRequestEvent()) {
			tripped = true
		}
	}
	return tripped
}

func TestModelStallDetectorTripsOnThirdCycle(t *testing.T) {
	d := newModelStallDetector(3)
	if feedStallCycles(d, 2) {
		t.Fatal("two cycles must not trip the detector")
	}
	d.observe(emptyAssistantEvent())
	if !d.observe(llmRequestEvent()) {
		t.Fatal("the third consecutive cycle must trip")
	}
	// It fires once per run: a replayed event stream must not cancel twice.
	d.observe(emptyAssistantEvent())
	if d.observe(llmRequestEvent()) {
		t.Fatal("detector re-triggered")
	}
}

func TestModelStallDetectorResetsOnProgress(t *testing.T) {
	// A tool call is progress.
	d := newModelStallDetector(3)
	feedStallCycles(d, 2)
	d.observe(executor.Event{Kind: executor.KindToolCall, Tool: "shell"})
	if feedStallCycles(d, 2) {
		t.Fatal("a tool call did not reset the streak")
	}
	// A turn end is progress.
	d.observe(executor.Event{Kind: executor.KindTurnEnd, StopReason: "completed", OK: true})
	if feedStallCycles(d, 2) {
		t.Fatal("a turn end did not reset the streak")
	}
	// A real (non-empty) assistant answer is progress.
	d2 := newModelStallDetector(3)
	feedStallCycles(d2, 2)
	d2.observe(executor.Event{Kind: executor.KindMessage, Role: "assistant", Text: "working on it"})
	if feedStallCycles(d2, 2) {
		t.Fatal("a non-empty reply did not reset the streak")
	}
	// llm_request without a preceding empty reply is normal traffic.
	d3 := newModelStallDetector(3)
	for i := 0; i < 5; i++ {
		if d3.observe(llmRequestEvent()) {
			t.Fatal("llm_request alone tripped the detector")
		}
	}
}

func TestModelStallDetectorZeroDisables(t *testing.T) {
	if newModelStallDetector(0) != nil {
		t.Fatal("limit 0 must disable the detector")
	}
	if (&Server{}).newRunStallDetector() != nil {
		t.Fatal("a zero Server.ModelStallLimit must disable detection")
	}
}

// The threshold is configurable without a new CLI flag: TW_MODEL_STALL_LIMIT=0
// turns the detector off, a positive value changes the cycle count.
func TestModelStallLimitConfigurable(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(modelStallEnv, "0")
	if s := New(st, &stallExec{}, dir, filepath.Join(dir, "cases")); s.ModelStallLimit != 0 {
		t.Fatalf("limit = %d, want 0 (disabled)", s.ModelStallLimit)
	}
	t.Setenv(modelStallEnv, "5")
	if s := New(st, &stallExec{}, dir, filepath.Join(dir, "cases")); s.ModelStallLimit != 5 {
		t.Fatalf("limit = %d, want 5", s.ModelStallLimit)
	}
	t.Setenv(modelStallEnv, "not-a-number")
	if s := New(st, &stallExec{}, dir, filepath.Join(dir, "cases")); s.ModelStallLimit != defaultModelStallLimit {
		t.Fatalf("invalid env should fall back to %d, got %d", defaultModelStallLimit, s.ModelStallLimit)
	}
}

// stallExec makes a run look like the final-E2E weak model: it emits empty
// assistant replies followed by llm_request, never a tool call, and only ends
// when the context is cancelled (or after cycles when patternOnlyFor is "").
type stallExec struct {
	mu             sync.Mutex
	cycles         int
	patternOnlyFor string // intent id; "" means every run stalls
	runs           int
}

func (e *stallExec) Name() string { return "stall" }

func (e *stallExec) Run(ctx context.Context, req executor.Request, emit func(executor.Event)) (executor.Result, error) {
	e.mu.Lock()
	e.runs++
	stalls := e.patternOnlyFor == "" || req.IntentID == e.patternOnlyFor
	e.mu.Unlock()
	if stalls {
		for i := 0; i < e.cycles; i++ {
			emit(emptyAssistantEvent())
			emit(llmRequestEvent())
			select {
			case <-ctx.Done():
				return executor.Result{ExitCode: -1, StopReason: "canceled"}, ctx.Err()
			default:
			}
		}
	}
	emit(executor.Event{Kind: executor.KindTurnEnd, StopReason: "completed", OK: true})
	return executor.Result{ExitCode: 0, StopReason: "completed"}, nil
}

func stallServer(t *testing.T, ex executor.Executor) (*Server, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, ex, dir, filepath.Join(dir, "cases"))
	t.Cleanup(func() {
		s.CancelAll()
		s.WaitIdle(5 * time.Second)
		time.Sleep(150 * time.Millisecond) // let the run log handle close on Windows
	})
	return s, st
}

// journalHasWarning waits until the run journal contains a structured warning
// line mentioning needle. The journal closes after finish, so poll the file.
func journalHasWarning(t *testing.T, dir, runID, needle string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	path := filepath.Join(dir, "runs", runID+".jsonl")
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				if line == "" || !strings.Contains(line, `"kind":"warning"`) || !strings.Contains(line, needle) {
					continue
				}
				return map[string]any{"line": line}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no warning event with %q in %s", needle, path)
	return nil
}

func TestModelStallCancelsRunHoldsIntentAndWarns(t *testing.T) {
	ex := &stallExec{cycles: 5}
	s, st := stallServer(t, ex)
	if s.ModelStallLimit != defaultModelStallLimit {
		t.Fatalf("default limit = %d", s.ModelStallLimit)
	}
	in, err := st.CreateIntent(model.Intent{Title: "弱模型空转"})
	if err != nil {
		t.Fatal(err)
	}
	runID, err := s.StartRun(in.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		r, ok := st.GetRun(runID)
		return ok && r.Status == model.RunCanceled
	})
	run, _ := st.GetRun(runID)
	if run.CancelCause != model.CancelCauseModelStall {
		t.Fatalf("cancel cause = %q, want %q", run.CancelCause, model.CancelCauseModelStall)
	}
	warnings := strings.Join(run.Warnings, "\n")
	if !strings.Contains(warnings, "model_stall") {
		t.Fatalf("run warnings = %v", run.Warnings)
	}
	// An intent run has a direction to hold, and the warning says so.
	if !strings.Contains(warnings, "方向已置 failed+Hold") {
		t.Fatalf("intent warning lost the Hold advice: %v", run.Warnings)
	}
	// The liveness stop holds the direction: failed+Hold, so the pump cannot
	// silently restart the same stalled work.
	waitFor(t, 5*time.Second, func() bool {
		got, ok := st.GetIntent(in.ID)
		return ok && got.Status == model.StatusFailed && got.Hold
	})
	journalHasWarning(t, s.DataDir, runID, "model_stall")

	// No automatic re-dispatch, even after a scheduler tick.
	before := len(st.Runs())
	s.pump()
	time.Sleep(150 * time.Millisecond)
	if got := len(st.Runs()); got != before {
		t.Fatalf("pump re-dispatched a model_stall direction: %d -> %d runs", before, got)
	}
	// Retry is the explicit way back: it clears Hold and starts one run.
	if _, err := s.RetryIntent(in.ID, "换档重试", false); err != nil {
		t.Fatalf("retry after model_stall: %v", err)
	}
	got, _ := st.GetIntent(in.ID)
	if got.Hold {
		t.Fatal("retry did not clear Hold after model_stall")
	}
}

// Detectors are per run: a stalled intent must not take a healthy one with it.
func TestModelStallIsPerRun(t *testing.T) {
	ex := &stallExec{cycles: 5}
	s, st := stallServer(t, ex)
	stalled, err := st.CreateIntent(model.Intent{Title: "空转的方向"})
	if err != nil {
		t.Fatal(err)
	}
	ex.mu.Lock()
	ex.patternOnlyFor = stalled.ID
	ex.mu.Unlock()
	stalledRun, err := s.StartRun(stalled.ID)
	if err != nil {
		t.Fatal(err)
	}
	healthy, err := st.CreateIntent(model.Intent{Title: "正常的方向"})
	if err != nil {
		t.Fatal(err)
	}
	healthyRun, err := s.StartRun(healthy.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		r, ok := st.GetRun(stalledRun)
		return ok && r.Status == model.RunCanceled
	})
	waitFor(t, 10*time.Second, func() bool {
		r, ok := st.GetRun(healthyRun)
		in, exists := st.GetIntent(healthy.ID)
		// Run completion is persisted before intent settlement. Wait for both
		// rather than sampling the gap between those two transactions.
		return ok && r.Status == model.RunDone && exists && in.Status != model.StatusRunning
	})
	got, _ := st.GetIntent(healthy.ID)
	if got.Hold || got.Status != model.StatusDone {
		t.Fatalf("healthy direction was affected: %+v", got)
	}
}

// The commander round is an agent session too: a spinning commander model is
// stopped by the same detector and the round records model_stall. The round
// must also return ErrCommanderStalled, not nil: a canceled-by-liveness round
// is not a normal completion, and the decider/console needs the reason (D107).
func TestModelStallCoversCommanderRound(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	ex := &commanderStallExec{cycles: 5}
	s := New(st, ex, dir, filepath.Join(dir, "cases"))
	s.Project = &project.Project{Dir: dir, Manifest: project.Manifest{ID: "default"}}
	s.APIBase = "http://127.0.0.1:9000"
	_, _, err = s.StartCommander(context.Background(), "## 本轮\nboard", false)
	if !errors.Is(err, decider.ErrCommanderStalled) {
		t.Fatalf("StartCommander err = %v, want ErrCommanderStalled", err)
	}
	if !strings.Contains(err.Error(), "strong_profile") {
		t.Fatalf("stall error lost the operator action: %v", err)
	}
	runs := st.Runs()
	if len(runs) != 1 {
		t.Fatalf("runs = %d", len(runs))
	}
	run := runs[0]
	if run.Status != model.RunCanceled || run.CancelCause != model.CancelCauseModelStall {
		t.Fatalf("commander round = %s / %s", run.Status, run.CancelCause)
	}
	if !strings.Contains(strings.Join(run.Warnings, "\n"), "model_stall") {
		t.Fatalf("commander warnings = %v", run.Warnings)
	}
	// A purpose run has no direction to hold: the generic "方向已置 failed+Hold"
	// text would be false there (D108).
	commanderWarnings := strings.Join(run.Warnings, "\n")
	if strings.Contains(commanderWarnings, "方向") || strings.Contains(commanderWarnings, "failed+Hold") {
		t.Fatalf("purpose-run warning claims a direction was held: %v", run.Warnings)
	}
	if !strings.Contains(commanderWarnings, "purpose run") {
		t.Fatalf("purpose-run warning is not the purpose variant: %v", run.Warnings)
	}
	if !strings.Contains(run.ExitReason, "model_stall") {
		t.Fatalf("commander reason = %q", run.ExitReason)
	}
	journalHasWarning(t, s.DataDir, run.ID, "model_stall")
}

// commanderStallExec emits the stall pattern for one round, then yields to the
// detector's cancellation like a real harness turn would.
type commanderStallExec struct {
	cycles int
}

func (e *commanderStallExec) Name() string { return "commander-stall" }

func (e *commanderStallExec) Run(ctx context.Context, _ executor.Request, emit func(executor.Event)) (executor.Result, error) {
	for i := 0; i < e.cycles; i++ {
		emit(emptyAssistantEvent())
		emit(llmRequestEvent())
		select {
		case <-ctx.Done():
			return executor.Result{ExitCode: -1, StopReason: "canceled"}, ctx.Err()
		default:
		}
	}
	emit(executor.Event{Kind: executor.KindTurnEnd, StopReason: "completed", OK: true})
	return executor.Result{ExitCode: 0, StopReason: "completed"}, nil
}
