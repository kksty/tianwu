package api

// D152 (revised): Tianwu no longer predicts the model window. The harness owns
// feasibility/compaction; when it reports a context limit, dispatch retries once
// with a degraded brief (hard constraints and handoff contract kept, optional
// summaries replaced by ids/pointers), and never loops.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

func TestContextLimitFailureDetection(t *testing.T) {
	cases := []struct {
		name    string
		res     executor.Result
		runErr  error
		reason  string
		journal string
		want    bool
	}{
		{
			name:   "harness preflight refusal",
			res:    executor.Result{ExitCode: 1, IsError: true, StopReason: "error", Output: "context_window_exhausted: context_window=8000, estimated_input_tokens=12000, safety_reserve=4096"},
			reason: "API error",
			want:   true,
		},
		{
			name: "provider wording",
			res:  executor.Result{ExitCode: 0, StopReason: "error", Output: "This model's maximum context length is 8192 tokens"},
			want: true,
		},
		{
			name:    "journal error event only",
			res:     executor.Result{ExitCode: 1},
			journal: "prompt is too long: 210000 tokens > 200000 maximum",
			want:    true,
		},
		{
			name:   "successful turn is never a context failure",
			res:    executor.Result{ExitCode: 0, StopReason: "completed"},
			reason: "maximum context length mentioned only in a summary",
			want:   false,
		},
		{
			name:   "unrelated failure",
			res:    executor.Result{ExitCode: 1, Output: "connection refused"},
			runErr: errors.New("boom"),
			want:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := contextLimitFailure(tc.res, tc.runErr, tc.reason, tc.journal); got != tc.want {
				t.Fatalf("contextLimitFailure = %v, want %v", got, tc.want)
			}
		})
	}
}

// contextLimitExec fails the first call with the harness's structured context
// error (or every call, for the "no loop" case) and records the prompts.
type contextLimitExec struct {
	mu         sync.Mutex
	prompts    []string
	alwaysFail bool
}

func (e *contextLimitExec) Name() string { return "context-limit" }

func (e *contextLimitExec) Run(_ context.Context, req executor.Request, _ func(executor.Event)) (executor.Result, error) {
	e.mu.Lock()
	e.prompts = append(e.prompts, req.Prompt)
	n := len(e.prompts)
	e.mu.Unlock()
	if e.alwaysFail || n == 1 {
		return executor.Result{
			ExitCode: 1, IsError: true, StopReason: "error",
			Output: "context_window_exhausted: context_window=8000, estimated_input_tokens=12000, safety_reserve=4096",
		}, nil
	}
	return executor.Result{ExitCode: 0, StopReason: "completed"}, nil
}

func (e *contextLimitExec) captured() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.prompts...)
}

func newContextLimitServer(t *testing.T, ex executor.Executor) (*Server, *store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, ex, dir, filepath.Join(dir, "cases"))
	return s, st, dir
}

func TestDispatchRetriesOnceWithSlimBriefAfterHarnessContextLimit(t *testing.T) {
	ex := &contextLimitExec{}
	s, st, _ := newContextLimitServer(t, ex)
	// Optional material (source-fact summaries) that the degraded brief must drop
	// while keeping the hard sections and the premise pointer.
	var from []string
	for i := 0; i < 3; i++ {
		ev := addEvidenceFile(t, st, s.CaseRoot, fmt.Sprintf("in_src/proof-%d.txt", i), "proof", "text/plain", []byte("proof"))
		f, err := st.AddFact(model.Fact{
			Kind: "vuln", Title: "源观察",
			EvidenceIDs: []string{ev.ID},
			Summary:     "可选摘要标记" + strings.Repeat("摘要", 60),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.ReviewFact(model.Review{FactID: f.ID, Verdict: "pass", VerifierRun: "run-proof"}); err != nil {
			t.Fatal(err)
		}
		from = append(from, f.ID)
	}
	in, err := st.CreateIntent(model.Intent{Title: "上下文受限方向", Description: "d", FromFactIDs: from})
	if err != nil {
		t.Fatal(err)
	}
	caseDir := filepath.Join(s.CaseRoot, in.ID)
	if err := os.MkdirAll(filepath.Join(caseDir, ".tw"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(caseDir, ".tw", "worker-note.md"),
		[]byte("笔记标记："+strings.Repeat("历史判断", 100)), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := s.StartRun(in.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		got, ok := st.GetIntent(in.ID)
		return ok && got.Status == model.StatusDone
	})
	prompts := ex.captured()
	if len(prompts) != 2 {
		t.Fatalf("executor ran %d times, want exactly 2 (initial + one slim retry)", len(prompts))
	}
	first, second := prompts[0], prompts[1]
	if strings.Contains(first, "本轮可选摘要有界省略") {
		t.Fatalf("the first attempt was already degraded:\n%s", first)
	}
	if !strings.Contains(first, "可选摘要标记") {
		t.Fatalf("the full brief did not carry the optional summaries the retry drops:\n%s", first)
	}
	for _, want := range []string{"本轮可选摘要有界省略", "## 意图", "## 交接（必做）", ".tw/context.json", "声明字段（唯一来源"} {
		if !strings.Contains(second, want) {
			t.Errorf("slim retry brief missing %q:\n%s", want, second)
		}
	}
	if strings.Contains(second, "可选摘要标记") || strings.Contains(second, "笔记标记") {
		t.Errorf("slim retry kept optional material:\n%s", second)
	}
	// The failed first run records why the second attempt was smaller.
	runs := st.Runs()
	if len(runs) != 2 {
		t.Fatalf("runs = %d, want 2", len(runs))
	}
	firstRun := runs[0]
	if firstRun.Status != model.RunFailed {
		t.Fatalf("first run status = %q, want failed", firstRun.Status)
	}
	found := false
	for _, w := range firstRun.Warnings {
		if strings.Contains(w, "上下文超限") && strings.Contains(w, "精简任务书") {
			found = true
		}
	}
	if !found {
		t.Fatalf("first run does not explain the slim retry: %+v", firstRun.Warnings)
	}
	if last := runs[len(runs)-1]; last.Status != model.RunDone {
		t.Fatalf("retry run status = %q, want done", last.Status)
	}
}

func TestContextSlimRetryHappensOnlyOnce(t *testing.T) {
	ex := &contextLimitExec{alwaysFail: true}
	s, st, _ := newContextLimitServer(t, ex)
	in, err := st.CreateIntent(model.Intent{Title: "一直超限", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartRun(in.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		got, ok := st.GetIntent(in.ID)
		return ok && got.Status == model.StatusFailed && len(ex.captured()) == 2
	})
	time.Sleep(500 * time.Millisecond)
	if prompts := ex.captured(); len(prompts) != 2 {
		t.Fatalf("executor ran %d times; a second context failure must not loop", len(prompts))
	}
	// The degraded failure is left to the operator/commander, with the harness
	// error visible as the run reason.
	runs := st.Runs()
	last := runs[len(runs)-1]
	if last.Status != model.RunFailed {
		t.Fatalf("last run status = %q, want failed", last.Status)
	}
	if !strings.Contains(strings.ToLower(last.ExitReason), "context_window_exhausted") &&
		!strings.Contains(last.ExitReason, "上下文") {
		t.Fatalf("last run reason does not carry the harness context error: %q", last.ExitReason)
	}
}
