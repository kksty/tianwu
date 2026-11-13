package verifier

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/evidence"
	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// accountingExec is a review harness stand-in that writes a verdict, reports a
// session model and returns a chosen Result, so the run row's accounting can be
// asserted without a harness.
type accountingExec struct {
	body   string
	model  string
	result executor.Result
	ran    bool
}

func (e *accountingExec) Name() string { return "accounting" }

func (e *accountingExec) Run(ctx context.Context, req executor.Request, emit func(executor.Event)) (executor.Result, error) {
	e.ran = true
	if e.body != "" {
		if err := os.WriteFile(filepath.Join(req.WorkDir, VerdictPath), []byte(e.body), 0o644); err != nil {
			return executor.Result{ExitCode: 1}, err
		}
	}
	if e.model != "" {
		emit(executor.Event{Kind: executor.KindSession, Text: e.model})
	}
	return e.result, nil
}

// A review is a run too: its usage and the model the harness actually used belong
// on its row, exactly like a worker run. Unknown usage stays unknown.
func TestReviewRunRecordsUsageAndObservedModel(t *testing.T) {
	t.Run("known usage and resolved model", func(t *testing.T) {
		st, f, root := setup(t)
		ex := &accountingExec{
			body:  `{"verdict":"pass","notes":"重跑一致"}`,
			model: "deepseek-flash",
			result: executor.Result{
				ExitCode: 0, StopReason: "completed",
				InputTokens: 30, OutputTokens: 8, CachedInputTokens: 5, CacheWriteTokens: 1,
				Tokens: 38, UsageRequests: 2, UsageMissing: 0, UsageKnown: true,
			},
		}
		review, err := New(st, ex, root).Run(context.Background(), f.ID)
		if err != nil {
			t.Fatal(err)
		}
		run, ok := st.GetRun(review.VerifierRun)
		if !ok {
			t.Fatalf("review run %s not found", review.VerifierRun)
		}
		if run.Status != model.RunDone {
			t.Fatalf("run status = %q", run.Status)
		}
		if run.Tokens != 38 || !run.Usage.Known || run.Usage.InputTokens != 30 ||
			run.Usage.OutputTokens != 8 || run.Usage.CachedInputTokens != 5 ||
			run.Usage.CacheWriteTokens != 1 || run.Usage.Requests != 2 ||
			run.Usage.TotalTokens != 38 {
			t.Fatalf("review run usage = %+v (tokens %d), want the Result's observations", run.Usage, run.Tokens)
		}
		if run.Model != "deepseek-flash" {
			t.Fatalf("run model = %q, want the harness-reported model", run.Model)
		}
		// The verifier has no saved profile binding of its own, so it must not
		// pretend to know one.
		if run.Profile != "" || run.ResolvedProfile != "" || run.Provider != "" {
			t.Fatalf("invented model binding: profile=%q resolved=%q provider=%q",
				run.Profile, run.ResolvedProfile, run.Provider)
		}
	})

	t.Run("unknown usage stays unknown", func(t *testing.T) {
		st, f, root := setup(t)
		ex := &accountingExec{body: `{"verdict":"pass","notes":"重跑一致"}`}
		review, err := New(st, ex, root).Run(context.Background(), f.ID)
		if err != nil {
			t.Fatal(err)
		}
		run, _ := st.GetRun(review.VerifierRun)
		if run.Usage.Known || run.Usage != (model.Usage{}) {
			t.Fatalf("unknown usage was recorded as measured: %+v", run.Usage)
		}
		if run.Model != "" {
			t.Fatalf("model invented without a session report: %q", run.Model)
		}
	})
}

// A cited proof whose preserved bytes no longer hash to what the ledger admitted
// must not be handed to the reviewer as if it were the proof: the review would be
// a verdict on altered bytes. Refuse before the prompt, settle the run failed with
// an explicit reason, and record needs_evidence so the fact cannot look proven.
func TestReviewRefusesDigestTamperedEvidence(t *testing.T) {
	root := t.TempDir()
	cases := filepath.Join(root, "cases")
	if err := os.MkdirAll(filepath.Join(cases, "in_1"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(cases, "in_1", "admin.html")
	if err := os.WriteFile(src, []byte("HTTP/1.1 200 OK\nadmin visible"), 0o644); err != nil {
		t.Fatal(err)
	}
	preserved, err := evidence.Preserve(cases, src)
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
	f, err := st.AddFact(model.Fact{
		IntentID: "in_1", Kind: "vuln", Title: "越权", Status: "unverified",
		EvidenceIDs: []string{ev.ID}, Origin: model.OriginDeclared,
	})
	if err != nil {
		t.Fatal(err)
	}
	// The immutable object itself is altered after admission.
	if err := os.WriteFile(filepath.Join(cases, filepath.FromSlash(preserved.URI)), []byte("tampered proof"), 0o644); err != nil {
		t.Fatal(err)
	}

	ex := &accountingExec{body: `{"verdict":"pass","notes":"should never run"}`}
	review, err := New(st, ex, cases).Run(context.Background(), f.ID)
	if err != nil {
		t.Fatalf("a refused review should still record a needs_evidence verdict: %v", err)
	}
	if review.Verdict == "pass" {
		t.Fatalf("tampered proof was accepted as a pass: %+v", review)
	}
	if review.Verdict != "needs_evidence" {
		t.Fatalf("verdict = %q, want needs_evidence", review.Verdict)
	}
	if !strings.Contains(review.Notes, ev.ID) || !strings.Contains(review.Notes, "摘要") {
		t.Fatalf("review notes must name the bad evidence and the reason: %q", review.Notes)
	}
	// The model was never asked to re-prove the claim from the altered bytes.
	if ex.ran {
		t.Fatal("the reviewer was given tampered evidence")
	}
	run, ok := st.GetRun(review.VerifierRun)
	if !ok {
		t.Fatalf("review run %s not found", review.VerifierRun)
	}
	if run.Status != model.RunFailed {
		t.Fatalf("run status = %q, want failed", run.Status)
	}
	if run.ExitReason == "" || !strings.Contains(run.ExitReason, ev.ID) {
		t.Fatalf("run exit reason = %q, want the digest refusal", run.ExitReason)
	}
	if got, _ := st.FactByID(f.ID); got.Status != "unverified" {
		t.Fatalf("a tampered proof changed the fact status to %q", got.Status)
	}
}
