package api

// A new dispatch archives the mission text it was actually given under the
// case's .tw/briefs. Resumes do not rewrite it (there is nothing new to
// archive), retries keep one file per run, the file never becomes evidence, and
// known credentials are redacted before it lands on disk (task-25).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
)

type briefExec struct {
	mu      sync.Mutex
	prompts []string
	fail    bool
}

func (e *briefExec) Name() string { return "brief" }

func (e *briefExec) Run(_ context.Context, req executor.Request, emit func(executor.Event)) (executor.Result, error) {
	e.mu.Lock()
	e.prompts = append(e.prompts, req.Prompt)
	e.mu.Unlock()
	emit(executor.Event{Kind: executor.KindSession, Text: "actual-model"})
	if e.fail {
		return executor.Result{ExitCode: 1, Output: "boom"}, errors.New("boom")
	}
	return executor.Result{ExitCode: 0, StopReason: "completed"}, nil
}

func (e *briefExec) promptAt(i int) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if i >= len(e.prompts) {
		return ""
	}
	return e.prompts[i]
}

func TestFreshDispatchArchivesBrief(t *testing.T) {
	s, p := newBindingServer(t)
	if err := p.SetNodeBinding([]string{"local"}, "local"); err != nil {
		t.Fatal(err)
	}
	ex := &briefExec{}
	s.Exec = ex
	in, err := p.Store.CreateIntent(model.Intent{Title: "验证 /admin", Description: "只读"})
	if err != nil {
		t.Fatal(err)
	}
	runID, err := s.StartRun(in.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, p, runID)
	run, ok := p.Store.GetRun(runID)
	if !ok {
		t.Fatal("run missing")
	}
	if run.BriefPath == "" || !strings.Contains(run.BriefPath, ".tw/briefs/"+runID+".md") {
		t.Fatalf("brief path = %q", run.BriefPath)
	}
	abs := filepath.Join(p.CaseDir, filepath.FromSlash(run.BriefPath))
	data, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("archived brief missing: %v", err)
	}
	if string(data) != ex.promptAt(0) {
		t.Fatalf("archived brief is not the dispatched prompt:\nfile=%q\nprompt=%q", data, ex.promptAt(0))
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(abs)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("brief mode = %o, want 600", info.Mode().Perm())
		}
	}

	// /api/runs exposes the field.
	runs := call(t, s, http.MethodGet, "/api/runs", nil)
	if !strings.Contains(runs.Body.String(), `"brief_path"`) || !strings.Contains(runs.Body.String(), runID) {
		t.Fatalf("/api/runs does not expose brief_path: %s", runs.Body.String())
	}
	// The archive is operator material, not evidence: the ingestor skips .tw.
	for _, e := range p.Store.Snapshot().Evidence {
		if strings.Contains(e.URI, ".tw/") {
			t.Fatalf("ingest harvested a .tw file as evidence: %+v", e)
		}
	}
}

// A resume (steer / wake / handoff) carries a continuation prompt; archiving it
// would only clone the session's own context, so it writes nothing. A retry is
// a new dispatch and keeps its own file next to the old one.
func TestResumeDoesNotArchiveAndRetriesKeepSeparateFiles(t *testing.T) {
	s, p := newBindingServer(t)
	if err := p.SetNodeBinding([]string{"local"}, "local"); err != nil {
		t.Fatal(err)
	}
	ex := &briefExec{fail: true}
	s.Exec = ex
	in, err := p.Store.CreateIntent(model.Intent{Title: "方向"})
	if err != nil {
		t.Fatal(err)
	}
	run1ID, err := s.StartRun(in.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, p, run1ID)
	run1, _ := p.Store.GetRun(run1ID)
	if run1.BriefPath == "" {
		t.Fatal("the first dispatch did not archive its brief")
	}

	// Resume path: prompt != "" -> keepPrompt=true -> no archive.
	resumeID, err := s.startRunWith(in.ID, "## 续话\n继续原来的方向", run1ID, true, true)
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, p, resumeID)
	resumeRun, _ := p.Store.GetRun(resumeID)
	if resumeRun.BriefPath != "" {
		t.Fatalf("a resume archived a brief: %q", resumeRun.BriefPath)
	}

	// Fresh retry: a second dispatch archives a second file.
	run2ID, err := s.StartRun(in.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, p, run2ID)
	run2, _ := p.Store.GetRun(run2ID)
	if run2.BriefPath == "" || run2.BriefPath == run1.BriefPath {
		t.Fatalf("retry brief path = %q (first %q)", run2.BriefPath, run1.BriefPath)
	}
	for _, rel := range []string{run1.BriefPath, run2.BriefPath} {
		if _, err := os.Stat(filepath.Join(p.CaseDir, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("archived brief %q missing: %v", rel, err)
		}
	}
	if ex.promptAt(0) == ex.promptAt(1) {
		t.Fatal("the two fresh dispatches had the same prompt; the fixture does not exercise multiple files")
	}
}

// The archived copy goes through the same secret boundary as the journal: a
// credential in the prompt must not become durable in the brief.
func TestArchivedBriefRedactsKnownSecrets(t *testing.T) {
	const secret = "sk-super-secret-123456"
	s := &Server{CaseRoot: t.TempDir()}
	s.Secrets = func() []string { return []string{secret} }
	workDir := filepath.Join(s.CaseRoot, "in_1")
	rel, err := s.archiveBrief(workDir, "r_secret", "前 "+secret+" 后")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(s.CaseRoot, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) {
		t.Fatalf("archived brief kept a known secret: %s", data)
	}
	if !strings.Contains(string(data), "***") {
		t.Fatalf("archived brief was not redacted: %s", data)
	}
}

// Old run rows have no brief_path; new ones can be written and read back.
func TestRunBriefPathJSONCompatibility(t *testing.T) {
	var old model.Run
	if err := json.Unmarshal([]byte(`{"id":"r_old","status":"done"}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.BriefPath != "" {
		t.Fatalf("old run grew a brief path: %q", old.BriefPath)
	}
	raw, err := json.Marshal(model.Run{ID: "r_plain"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "brief_path") {
		t.Fatalf("omitempty did not drop an empty path: %s", raw)
	}
	raw, err = json.Marshal(model.Run{ID: "r_new", BriefPath: "in_1/.tw/briefs/r_new.md"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"brief_path":"in_1/.tw/briefs/r_new.md"`) {
		t.Fatalf("brief_path missing: %s", raw)
	}
}
