package verifier

import (
	"context"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
)

func TestChangedClaimCannotReuseEarlierReviewFile(t *testing.T) {
	st, f, root := setup(t)
	ex := &stubExec{body: `{"verdict":"pass","notes":"original claim"}`}
	v := New(st, ex, root)
	if _, err := v.Run(context.Background(), f.ID); err != nil {
		t.Fatal(err)
	}
	firstDir := ex.lastReq.WorkDir
	if _, err := st.UpdateFact(f.ID, func(f *model.Fact) { f.Summary = "changed claim" }); err != nil {
		t.Fatal(err)
	}
	ex.body = ""
	review, err := v.Run(context.Background(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if review.Verdict != "needs_evidence" || firstDir == ex.lastReq.WorkDir {
		t.Fatalf("old receipt reused: %+v", review)
	}
}

type failedReceiptExec struct{ result executor.Result }

func (s failedReceiptExec) Name() string { return "failed-receipt" }
func (s failedReceiptExec) Run(ctx context.Context, req executor.Request, emit func(executor.Event)) (executor.Result, error) {
	w := &stubExec{body: `{"verdict":"pass","notes":"written before failure"}`}
	if _, err := w.Run(ctx, req, emit); err != nil {
		return executor.Result{}, err
	}
	return s.result, nil
}

func TestUnsuccessfulRunCannotSupplyPassReceipt(t *testing.T) {
	for _, result := range []executor.Result{{ExitCode: 1}, {IsError: true}, {StopReason: "error"}} {
		st, f, root := setup(t)
		review, err := New(st, failedReceiptExec{result}, root).Run(context.Background(), f.ID)
		if err != nil {
			t.Fatal(err)
		}
		if review.Verdict == "pass" {
			t.Fatalf("accepted failed run: %+v", result)
		}
	}
}

func TestReviewReceivesAndRecordsPersistentInstructions(t *testing.T) {
	st, f, root := setup(t)
	const constraint = "only inspect local evidence; no outbound requests"
	in, _, err := st.CreateInstruction(model.Instruction{Text: constraint, CreatedBy: "operator", IdempotencyKey: "review-policy"})
	if err != nil {
		t.Fatal(err)
	}
	ex := &stubExec{body: `{"verdict":"needs_evidence","notes":"local evidence insufficient"}`}
	review, err := New(st, ex, root).Run(context.Background(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	run, _ := st.GetRun(review.VerifierRun)
	if !strings.Contains(ex.lastReq.Prompt, constraint) || run.InstructionVersions[in.ID] != in.Version {
		t.Fatal("missing frozen constraints")
	}
}
