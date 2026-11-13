package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
	"github.com/kksty/tianwu/internal/verifier"
)

// batchExec is a verifier run without a model: it optionally blocks (so the
// test can observe the batch while it is live) and optionally writes a pass
// verdict so the ledger shows the review actually happened.
type batchExec struct {
	block chan struct{}
	body  string
}

func (e *batchExec) Name() string { return "batch-stub" }

func (e *batchExec) Run(ctx context.Context, req executor.Request, emit func(executor.Event)) (executor.Result, error) {
	if e.block != nil {
		select {
		case <-e.block:
		case <-ctx.Done():
			return executor.Result{ExitCode: 1}, ctx.Err()
		}
	}
	if e.body != "" {
		if err := os.WriteFile(filepath.Join(req.WorkDir, verifier.VerdictPath), []byte(e.body), 0o644); err != nil {
			return executor.Result{ExitCode: 1}, err
		}
	}
	return executor.Result{ExitCode: 0}, nil
}

func newBatchServer(t *testing.T, ex executor.Executor) (*Server, *store.Store, *http.ServeMux) {
	t.Helper()
	dir := t.TempDir()
	cases := filepath.Join(dir, "cases")
	if err := os.MkdirAll(cases, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, ex, dir, cases)
	s.Timeout = 0
	s.Verifier = verifier.New(st, ex, cases)
	mux := http.NewServeMux()
	s.Register(mux)
	return s, st, mux
}

func addVerifyFact(t *testing.T, st *store.Store, kind, status string) model.Fact {
	t.Helper()
	ev, err := st.AddEvidence(model.Evidence{IntentID: "in_1", URI: "in_1/proof.txt", Name: "proof.txt"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := st.AddFact(model.Fact{
		IntentID: "in_1", Kind: kind, Title: kind + "/" + status,
		Status: status, EvidenceIDs: []string{ev.ID}, Origin: model.OriginDeclared,
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func doPost(t *testing.T, mux *http.ServeMux, path string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// One click queues every reviewable fact and nothing else: verified findings,
// rejected findings and observations are not re-run. The reviews happen, so
// the ledger shows the verdicts afterwards (D100).
func TestVerifyPendingCoversOnlyUnreviewedConclusions(t *testing.T) {
	ex := &batchExec{body: `{"verdict":"pass","notes":"重跑一致","proof":"p"}`}
	s, st, mux := newBatchServer(t, ex)
	vuln := addVerifyFact(t, st, "vuln", "unverified")
	cred := addVerifyFact(t, st, "cred", "unverified")
	addVerifyFact(t, st, "vuln", "verified")
	addVerifyFact(t, st, "vuln", "rejected")
	addVerifyFact(t, st, "host", "unverified")

	code, out := doPost(t, mux, "/api/facts/verify-pending")
	if code != http.StatusAccepted {
		t.Fatalf("batch = %d %v, want 202", code, out)
	}
	if out["queued"] != float64(2) || out["skipped"] != float64(0) {
		t.Fatalf("batch counts = %v, want queued 2 skipped 0", out)
	}
	waitFor(t, 5*time.Second, func() bool {
		a, _ := st.FactByID(vuln.ID)
		b, _ := st.FactByID(cred.ID)
		return a.Status == "verified" && b.Status == "verified"
	})
	waitFor(t, 5*time.Second, func() bool {
		s.verifyBatchMu.Lock()
		defer s.verifyBatchMu.Unlock()
		return !s.verifyBatch
	})
	if reviews := st.Reviews(""); len(reviews) != 2 {
		t.Fatalf("reviews = %d, want 2 (only the unverified findings)", len(reviews))
	}
}

// A second click while a batch is live must not double the token bill: the
// endpoint refuses instead of queueing a parallel copy.
func TestVerifyPendingRefusesASecondBatch(t *testing.T) {
	block := make(chan struct{})
	ex := &batchExec{block: block}
	s, st, mux := newBatchServer(t, ex)
	f := addVerifyFact(t, st, "vuln", "unverified")

	code, out := doPost(t, mux, "/api/facts/verify-pending")
	if code != http.StatusAccepted {
		t.Fatalf("first batch = %d %v, want 202", code, out)
	}
	waitFor(t, 5*time.Second, func() bool { return s.Verifier.Busy(f.ID) })

	code, out = doPost(t, mux, "/api/facts/verify-pending")
	if code != http.StatusConflict {
		t.Fatalf("second batch = %d %v, want 409", code, out)
	}
	close(block)
	waitFor(t, 5*time.Second, func() bool {
		s.verifyBatchMu.Lock()
		defer s.verifyBatchMu.Unlock()
		return !s.verifyBatch
	})
}

// Without a verifier the button must say so instead of pretending to queue.
func TestVerifyPendingWithoutAReviewer(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: st}
	mux := http.NewServeMux()
	s.Register(mux)
	code, out := doPost(t, mux, "/api/facts/verify-pending")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("batch without verifier = %d %v, want 503", code, out)
	}
}

// The report dialog needs one cheap read: how many findings the report would
// carry, and how many of them are still unverified. Rejected facts never
// become findings, observations never did (D100).
func TestReportPrecheckCountsFindingsByReviewState(t *testing.T) {
	ex := &batchExec{}
	_, st, mux := newBatchServer(t, ex)
	addVerifyFact(t, st, "vuln", "unverified")
	addVerifyFact(t, st, "cred", "unverified")
	addVerifyFact(t, st, "vuln", "verified")
	addVerifyFact(t, st, "vuln", "rejected")
	addVerifyFact(t, st, "host", "unverified")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/reports/precheck", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("precheck = %d %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["reportable"] != float64(3) || out["unverified"] != float64(2) || out["rejected"] != float64(1) {
		t.Fatalf("precheck = %v, want reportable 3 unverified 2 rejected 1", out)
	}
	if out["running"] != false {
		t.Fatalf("precheck running = %v, want false", out["running"])
	}
}
