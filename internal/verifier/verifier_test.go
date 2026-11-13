package verifier

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

type stubExec struct {
	body    string // written to review.json, empty means "writes nothing"
	fail    bool
	lastReq executor.Request
}

func (s *stubExec) Name() string { return "stub" }

func (s *stubExec) Run(ctx context.Context, req executor.Request, emit func(executor.Event)) (executor.Result, error) {
	s.lastReq = req
	if s.fail {
		return executor.Result{ExitCode: 1, Output: "boom"}, nil
	}
	if s.body != "" {
		if err := os.WriteFile(filepath.Join(req.WorkDir, VerdictPath), []byte(s.body), 0o644); err != nil {
			return executor.Result{ExitCode: 1}, err
		}
	}
	return executor.Result{ExitCode: 0, Events: 3}, nil
}

func setup(t *testing.T) (*store.Store, *model.Fact, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "in_1"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The reviewer is given the cited bytes; a fixture that only names a path
	// would test the digest path with a missing file (a legitimate
	// needs_evidence, not the behavior under test here).
	if err := os.WriteFile(filepath.Join(root, "in_1", "proof.html"), []byte("HTTP/1.1 200 OK\nadmin visible"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(root, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	ev, err := st.AddEvidence(model.Evidence{URI: "in_1/proof.html", Name: "proof.html"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := st.AddFact(model.Fact{
		IntentID: "in_1", Kind: "vuln", Title: "/admin 越权",
		Summary: "普通用户可读订单", Doc: "GET /admin (Cookie uid=2) → 200",
		EvidenceIDs: []string{ev.ID}, Origin: model.OriginDeclared,
	})
	if err != nil {
		t.Fatal(err)
	}
	return st, &f, root
}

func TestEligible(t *testing.T) {
	cases := []struct {
		f    model.Fact
		want bool
	}{
		{model.Fact{Kind: "vuln", Status: "unverified"}, true},
		{model.Fact{Kind: "cred", Status: "unverified"}, true},
		{model.Fact{Kind: "vuln", Status: "verified"}, false},
		{model.Fact{Kind: "host", Status: "unverified"}, false},
		{model.Fact{Kind: "service", Status: "unverified"}, false},
		{model.Fact{Kind: "note", Status: "unverified"}, false},
		{model.Fact{Kind: "loot", Status: "unverified"}, false},
	}
	for _, c := range cases {
		if got := Eligible(c.f); got != c.want {
			t.Errorf("Eligible(%s/%s) = %v, want %v", c.f.Kind, c.f.Status, got, c.want)
		}
	}
}

func TestRunPassMarksVerified(t *testing.T) {
	st, f, root := setup(t)
	ex := &stubExec{body: `{"verdict":"pass","notes":"重跑一致","proof":"curl -H Cookie:uid=2 /admin → 200"}`}
	v := New(st, ex, root)

	rev, err := v.Run(context.Background(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rev.Verdict != "pass" || !strings.Contains(rev.Notes, "证明：") {
		t.Fatalf("review = %+v", rev)
	}
	got, _ := st.FactByID(f.ID)
	if got.Status != "verified" {
		t.Fatalf("fact status = %s", got.Status)
	}
	if ex.lastReq.WorkDir == "" || !strings.Contains(ex.lastReq.Prompt, "/admin 越权") {
		t.Fatalf("prompt/workdir not built: %+v", ex.lastReq)
	}
	if !strings.Contains(ex.lastReq.Prompt, filepath.Join("in_1", "proof.html")) {
		t.Fatalf("evidence path missing from prompt")
	}
}

func TestRunNeedsEvidenceKeepsUnverified(t *testing.T) {
	st, f, root := setup(t)
	ex := &stubExec{body: `{"verdict":"needs_evidence","notes":"重跑 403，结论不成立"}`}
	v := New(st, ex, root)
	if _, err := v.Run(context.Background(), f.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := st.FactByID(f.ID)
	if got.Status != "unverified" {
		t.Fatalf("status = %s", got.Status)
	}
}

func TestRunRejectMarksRejected(t *testing.T) {
	st, f, root := setup(t)
	ex := &stubExec{body: `{"verdict":"reject","notes":"明确否定"}`}
	v := New(st, ex, root)
	if _, err := v.Run(context.Background(), f.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := st.FactByID(f.ID)
	if got.Status != "rejected" {
		t.Fatalf("status = %s", got.Status)
	}
}

func TestRunWithoutVerdictIsNeedsEvidence(t *testing.T) {
	st, f, root := setup(t)
	ex := &stubExec{}
	v := New(st, ex, root)
	rev, err := v.Run(context.Background(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rev.Verdict != "needs_evidence" || !strings.Contains(rev.Notes, "没有给出可用结论") {
		t.Fatalf("review = %+v", rev)
	}
}

func TestRunRejectsIneligible(t *testing.T) {
	st, f, root := setup(t)
	host, _ := st.AddFact(model.Fact{IntentID: "in_1", Kind: "host", Title: "10.0.0.5"})
	v := New(st, &stubExec{}, root)
	if _, err := v.Run(context.Background(), host.ID); err == nil {
		t.Fatal("observational facts must not be verified")
	}
	// Second run on the same fact is refused once it is verified.
	ex := &stubExec{body: `{"verdict":"pass","notes":"ok"}`}
	v2 := New(st, ex, root)
	if _, err := v2.Run(context.Background(), f.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := v2.Run(context.Background(), f.ID); err == nil {
		t.Fatal("already verified fact must not be re-run")
	}
}

// A static finding has no live surface: asking it for a dynamic re-run would
// reject it for the wrong reason, so the brief asks for a replay instead (D35).
func TestStaticFindingGetsReplayBrief(t *testing.T) {
	st, _, root := setup(t)
	ex := &stubExec{body: `{"verdict":"pass","notes":"重放一致"}`}
	v := New(st, ex, root)

	staticFact, err := st.AddFact(model.Fact{
		IntentID: "in_1", Kind: "vuln", Title: "httpd 栈溢出", Proof: "static",
		Doc: `rizin -q -c 'pdf @ sym.handler' fw.bin`, Origin: model.OriginDeclared,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Run(context.Background(), staticFact.ID); err != nil {
		t.Fatal(err)
	}
	p := ex.lastReq.Prompt
	for _, want := range []string{"静态分析", "重放一次", "不要重新攻击"} {
		if !strings.Contains(p, want) {
			t.Fatalf("static brief missing %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "重新跑一次最小只读证明") {
		t.Fatal("static brief still asks for a dynamic re-run")
	}
}

func TestReadVerdictRejectsBadValues(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"not json":    "nope",
		"bad verdict": `{"verdict":"approved"}`,
		"no notes":    `{"verdict":"reject"}`,
	}
	for name, body := range cases {
		p := filepath.Join(dir, "v.json")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := readVerdict(p); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

// Start claims before returning: two concurrent requests must not both get 202
// and then have one silently lose inside Run. The completion callback is what
// wakes the decider, so it must fire after the verdict is stored.
func TestStartClaimsBeforeReturning(t *testing.T) {
	st, f, root := setup(t)
	ex := &stubExec{body: `{"verdict":"pass","notes":"重跑一致"}`}
	v := New(st, ex, root)
	done := make(chan struct{})
	if err := v.Start(context.Background(), f.ID, func() { close(done) }); err != nil {
		t.Fatal(err)
	}
	if err := v.Start(context.Background(), f.ID, nil); err == nil || !strings.Contains(err.Error(), "正在复核") {
		t.Fatalf("second Start = %v, want a busy refusal", err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("review did not finish")
	}
	got, ok := st.FactByID(f.ID)
	if !ok || got.Status != "verified" {
		t.Fatalf("verdict not applied: %+v", got)
	}
}
