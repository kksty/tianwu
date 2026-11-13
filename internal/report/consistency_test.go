package report

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/board"
	"github.com/kksty/tianwu/internal/evidence"
	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// hookExec is a harness stand-in whose behavior and mutations are supplied by the
// test, so a test can move the live ledger exactly while the writer is working.
type hookExec struct {
	run func(ctx context.Context, req executor.Request, emit func(executor.Event)) (executor.Result, error)
}

func (e *hookExec) Name() string { return "hook" }

func (e *hookExec) Run(ctx context.Context, req executor.Request, emit func(executor.Event)) (executor.Result, error) {
	if e.run == nil {
		return executor.Result{ExitCode: 0, StopReason: "completed"}, nil
	}
	return e.run(ctx, req, emit)
}

// reportRunFixture is the smallest ledger a complete report can be written from:
// one verified conclusion with one real evidence file inside the case.
func reportRunFixture(t *testing.T) (st *store.Store, caseRoot string, fact model.Fact, ev model.Evidence) {
	t.Helper()
	root := t.TempDir()
	caseRoot = filepath.Join(root, "cases")
	if err := os.MkdirAll(filepath.Join(caseRoot, "in_1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(caseRoot, "in_1", "proof.txt"), []byte("HTTP/1.1 200 OK\nid=2 can read all orders"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(root, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	ev, err = st.AddEvidence(model.Evidence{IntentID: "in_1", URI: "in_1/proof.txt", Name: "proof.txt"})
	if err != nil {
		t.Fatal(err)
	}
	fact, err = st.AddFact(model.Fact{
		IntentID: "in_1", Kind: "vuln", Title: "/admin 越权", Status: "verified",
		EvidenceIDs: []string{ev.ID}, Origin: model.OriginDeclared,
	})
	if err != nil {
		t.Fatal(err)
	}
	return st, caseRoot, fact, ev
}

// writeCompleteReport writes the three artifacts Validate expects, referencing
// the evidence copy the generator has already made.
func writeCompleteReport(t *testing.T, workDir, factID, evidenceRel string) {
	t.Helper()
	writeFile := func(rel, body string) {
		t.Helper()
		abs := filepath.Join(workDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	findings, err := json.Marshal([]Finding{{
		Fact: factID, Verdict: VerdictConfirmed, Title: "/admin 越权",
		Location: "http://target/admin", Preconditions: "普通用户会话",
		Impact: "读取全部订单", Trace: "GET /admin → 200",
		Evidence: []string{evidenceRel},
	}})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(IndexFile, "# 报告\n\n- 结论：/admin 越权\n")
	writeFile(Findings, string(findings))
	writeFile(FindingsDir+"/"+factID+".md", "# /admin 越权\n\n证据："+evidenceRel+"\n")
}

func readSnapshotFile(t *testing.T, dir string) snapshot {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, Snapshot))
	if err != nil {
		t.Fatal(err)
	}
	var s snapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("snapshot is not json: %v", err)
	}
	return s
}

// The report's snapshot facts, source digest, instruction versions and validation
// ledger must all describe the single freeze the run started from. Before this,
// Generate took a Snapshot() and froze the instructions later, so a write landing
// between the two produced a report that disagreed with itself.
func TestReportSnapshotDigestAndValidationShareOneFrozenMoment(t *testing.T) {
	st, caseRoot, fact, ev := reportRunFixture(t)
	in, _, err := st.CreateInstruction(model.Instruction{
		Text: "只检查本地证据，不发外部请求", CreatedBy: "operator", IdempotencyKey: "report-policy",
	})
	if err != nil {
		t.Fatal(err)
	}
	// The digest a single freeze of the current ledger must produce. The report
	// run row the generator later adds is not report content (runs are outside
	// the content digest), so this is the expected value.
	pre := st.Snapshot()
	wantDigest := board.ContentDigest(board.BuildFromDB(pre, 20))

	evidenceRel := EvidenceDir + "/" + ev.ID + "-proof.txt"
	ex := &hookExec{run: func(_ context.Context, req executor.Request, emit func(executor.Event)) (executor.Result, error) {
		writeCompleteReport(t, req.WorkDir, fact.ID, evidenceRel)
		// The live ledger moves while the writer works: a new reportable fact and
		// a new instruction version land after the freeze.
		if _, err := st.AddFact(model.Fact{
			IntentID: "in_1", Kind: "vuln", Title: "冻结之后才出现的结论", Status: "verified",
		}); err != nil {
			return executor.Result{ExitCode: 1}, err
		}
		if _, err := st.UpdateInstruction(in.ID, in.Version, "改后的约束", model.InstructionActive); err != nil {
			return executor.Result{ExitCode: 1}, err
		}
		emit(executor.Event{Kind: executor.KindSession, Text: "actual-model"})
		return executor.Result{
			ExitCode: 0, StopReason: "completed", Events: 4,
			InputTokens: 11, OutputTokens: 7, CachedInputTokens: 3, Tokens: 21,
			UsageRequests: 1, UsageKnown: true,
		}, nil
	}}
	g := &Generator{Store: st, Exec: ex, CaseRoot: caseRoot, Scope: "10.0.0.0/24"}
	meta, err := g.Generate(context.Background())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	// The snapshot is the frozen input: one fact, the frozen instruction version.
	s := readSnapshotFile(t, g.Dir(meta.ID))
	if len(s.Facts) != 1 || s.Facts[0].ID != fact.ID {
		t.Fatalf("snapshot facts = %+v, want only the frozen conclusion", s.Facts)
	}
	if len(s.Instructions) != 1 || s.Instructions[0].ID != in.ID {
		t.Fatalf("snapshot instructions = %+v, want the frozen instruction", s.Instructions)
	}
	if s.Instructions[0].Version != in.Version || s.Instructions[0].Text != "只检查本地证据，不发外部请求" {
		t.Fatalf("snapshot instruction = %+v, want v%d with the frozen text", s.Instructions[0], in.Version)
	}

	// The run row froze the same instruction version.
	run, ok := st.GetRun(meta.RunID)
	if !ok {
		t.Fatalf("report run %s not found", meta.RunID)
	}
	if run.InstructionVersions[in.ID] != in.Version {
		t.Fatalf("run instruction versions = %v, want %s v%d", run.InstructionVersions, in.ID, in.Version)
	}

	// The source digest belongs to the frozen moment, not the mutated ledger.
	if meta.SourceDigest != wantDigest {
		t.Fatalf("source digest = %q, want the frozen-moment digest %q", meta.SourceDigest, wantDigest)
	}
	if live := board.ContentDigest(board.Build(st, 20)); meta.SourceDigest == live {
		t.Fatalf("source digest followed the live ledger after the freeze (got %q)", live)
	}

	// Validation used the frozen DB too: the post-freeze conclusion is not
	// required to have a findings row, and every rule still passes.
	if meta.Status != StatusReady {
		t.Fatalf("report status = %q, want ready; checks = %+v", meta.Status, meta.Checks)
	}
	for _, c := range meta.Checks {
		if !c.OK {
			t.Errorf("check %s failed against the frozen ledger: %s", c.Rule, c.Detail)
		}
	}
	if _, err := os.ReadFile(filepath.Join(g.Dir(meta.ID), ChecksFile)); err != nil {
		t.Fatalf("checks.json not written: %v", err)
	}
}

// The report run is a run like any other: the provider's usage and the model the
// harness actually resolved belong on its row. Unknown usage must stay unknown,
// not become a measured zero.
func TestReportRunRecordsUsageAndObservedModel(t *testing.T) {
	t.Run("known usage and resolved model", func(t *testing.T) {
		st, caseRoot, fact, ev := reportRunFixture(t)
		ex := &hookExec{run: func(_ context.Context, req executor.Request, emit func(executor.Event)) (executor.Result, error) {
			writeCompleteReport(t, req.WorkDir, fact.ID, EvidenceDir+"/"+ev.ID+"-proof.txt")
			emit(executor.Event{Kind: executor.KindSession, Text: "deepseek-flash"})
			return executor.Result{
				ExitCode: 0, StopReason: "completed",
				InputTokens: 40, OutputTokens: 9, CachedInputTokens: 12, CacheWriteTokens: 2,
				Tokens: 51, UsageRequests: 3, UsageMissing: 1, UsageKnown: true,
			}, nil
		}}
		meta, err := (&Generator{Store: st, Exec: ex, CaseRoot: caseRoot}).Generate(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		run, ok := st.GetRun(meta.RunID)
		if !ok {
			t.Fatalf("run %s not found", meta.RunID)
		}
		// The report must not read as stale against the ledger it was just
		// written from: the frozen digest and the live content watermark agree.
		if live := board.ContentDigest(board.Build(st, 20)); live != meta.SourceDigest {
			t.Fatalf("fresh report digest = %q, live ledger digest = %q (report would list as stale)", meta.SourceDigest, live)
		}
		if run.Tokens != 51 || !run.Usage.Known || run.Usage.InputTokens != 40 ||
			run.Usage.OutputTokens != 9 || run.Usage.CachedInputTokens != 12 ||
			run.Usage.CacheWriteTokens != 2 || run.Usage.Requests != 3 || run.Usage.Missing != 1 ||
			run.Usage.TotalTokens != 51 {
			t.Fatalf("report run usage = %+v (tokens %d), want the Result's observations", run.Usage, run.Tokens)
		}
		if run.Model != "deepseek-flash" {
			t.Fatalf("run model = %q, want the model the harness reported", run.Model)
		}
		// The generator cannot know the saved profile behind an unspecified
		// binding; empty means "not known", never a guessed id.
		if run.Profile != "" || run.ResolvedProfile != "" || run.Provider != "" {
			t.Fatalf("invented model binding: profile=%q resolved=%q provider=%q",
				run.Profile, run.ResolvedProfile, run.Provider)
		}
	})

	t.Run("unknown usage stays unknown", func(t *testing.T) {
		st, caseRoot, fact, ev := reportRunFixture(t)
		ex := &hookExec{run: func(_ context.Context, req executor.Request, _ func(executor.Event)) (executor.Result, error) {
			writeCompleteReport(t, req.WorkDir, fact.ID, EvidenceDir+"/"+ev.ID+"-proof.txt")
			return executor.Result{ExitCode: 0, StopReason: "completed"}, nil
		}}
		meta, err := (&Generator{Store: st, Exec: ex, CaseRoot: caseRoot}).Generate(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		run, _ := st.GetRun(meta.RunID)
		if run.Usage.Known || run.Usage != (model.Usage{}) {
			t.Fatalf("unknown usage was recorded as measured: %+v", run.Usage)
		}
		if run.Model != "" {
			t.Fatalf("model invented without a session report: %q", run.Model)
		}
	})
}

// A report whose cited preserved bytes no longer hash to the admitted digest must
// refuse before a model run: copying them would turn tampering into the
// deliverable. The failed generation leaves no advertised draft and names the
// evidence in the run's exit reason.
func TestGenerateRefusesTamperedPreservedEvidence(t *testing.T) {
	root := t.TempDir()
	caseRoot := filepath.Join(root, "cases")
	if err := os.MkdirAll(filepath.Join(caseRoot, "in_1"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(caseRoot, "in_1", "admin.html")
	if err := os.WriteFile(src, []byte("HTTP/1.1 200 OK\nadmin visible"), 0o644); err != nil {
		t.Fatal(err)
	}
	preserved, err := evidence.Preserve(caseRoot, src)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(root, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	ev, err := st.AddEvidence(model.Evidence{
		IntentID: "in_1", URI: preserved.URI, Digest: preserved.Digest,
		Name: "admin.html", Size: preserved.Size,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddFact(model.Fact{
		IntentID: "in_1", Kind: "vuln", Title: "越权", Status: "verified",
		EvidenceIDs: []string{ev.ID}, Origin: model.OriginDeclared,
	}); err != nil {
		t.Fatal(err)
	}
	// The preserved object is altered after admission.
	if err := os.WriteFile(filepath.Join(caseRoot, filepath.FromSlash(preserved.URI)), []byte("tampered proof"), 0o644); err != nil {
		t.Fatal(err)
	}
	executed := false
	g := &Generator{Store: st, Exec: &hookExec{run: func(context.Context, executor.Request, func(executor.Event)) (executor.Result, error) {
		executed = true
		return executor.Result{ExitCode: 0}, nil
	}}, CaseRoot: caseRoot}
	_, err = g.Generate(context.Background())
	if !errors.Is(err, evidence.ErrDigestMismatch) {
		t.Fatalf("Generate over tampered evidence = %v, want ErrDigestMismatch", err)
	}
	if !strings.Contains(err.Error(), ev.ID) || !strings.Contains(err.Error(), preserved.URI) {
		t.Fatalf("refusal must name the evidence id/address: %v", err)
	}
	if executed {
		t.Fatal("the model was asked to write a report from tampered proof")
	}
	if got := List(caseRoot); len(got) != 0 {
		t.Fatalf("a failed report was advertised: %+v", got)
	}
	// The early failure is visible as a failed run, not as "still writing".
	var run model.Run
	for _, r := range st.Runs() {
		if r.Purpose == "report" {
			run = r
		}
	}
	if run.ID == "" || run.Status != model.RunFailed {
		t.Fatalf("report run = %+v, want a settled failure", run)
	}
	if run.ExitReason == "" || !strings.Contains(run.ExitReason, ev.ID) {
		t.Fatalf("run exit reason = %q, want the digest refusal", run.ExitReason)
	}
}

// A failed or canceled writing pass is not a report: no directory, no report.json,
// no stale meta for List() to advertise.
func TestFailedOrCanceledReportLeavesNoAdvertisedDraft(t *testing.T) {
	t.Run("executor failure", func(t *testing.T) {
		st, caseRoot, _, _ := reportRunFixture(t)
		ex := &hookExec{run: func(context.Context, executor.Request, func(executor.Event)) (executor.Result, error) {
			return executor.Result{ExitCode: 1, Output: "homeostat failure", StopReason: "error"}, nil
		}}
		_, err := (&Generator{Store: st, Exec: ex, CaseRoot: caseRoot}).Generate(context.Background())
		if err == nil || !strings.Contains(err.Error(), "homeostat failure") {
			t.Fatalf("Generate = %v, want the executor failure", err)
		}
		if got := List(caseRoot); len(got) != 0 {
			t.Fatalf("failed report advertised: %+v", got)
		}
		for _, r := range st.Runs() {
			if r.Purpose != "report" {
				continue
			}
			if r.Status != model.RunFailed || r.ExitReason == "" {
				t.Fatalf("failed report run = %+v", r)
			}
		}
	})

	t.Run("control-plane cancel", func(t *testing.T) {
		st, caseRoot, _, _ := reportRunFixture(t)
		ex := &hookExec{run: func(ctx context.Context, _ executor.Request, _ func(executor.Event)) (executor.Result, error) {
			<-ctx.Done()
			return executor.Result{ExitCode: -1, StopReason: "canceled"}, ctx.Err()
		}}
		g := &Generator{
			Store: st, Exec: ex, CaseRoot: caseRoot,
			Track: func(_ string, cancel context.CancelFunc) func() string {
				cancel()
				return func() string { return model.CancelCausePause }
			},
		}
		_, err := g.Generate(context.Background())
		if err == nil || !strings.Contains(err.Error(), "取消") {
			t.Fatalf("Generate = %v, want a cancellation error", err)
		}
		if got := List(caseRoot); len(got) != 0 {
			t.Fatalf("canceled report advertised: %+v", got)
		}
		for _, r := range st.Runs() {
			if r.Purpose != "report" {
				continue
			}
			if r.Status != model.RunCanceled || r.CancelCause != model.CancelCausePause {
				t.Fatalf("canceled report run = %+v", r)
			}
		}
	})
}
