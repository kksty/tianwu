package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// newReviewServer wires only what the review handler needs: a ledger with one
// vuln fact and the route registered on a real mux.
func newReviewServer(t *testing.T, kind string) (*Server, *httptest.Server, model.Fact) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	f, err := st.AddFact(model.Fact{IntentID: "in_1", Kind: kind, Title: "t", Status: "unverified"})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: st}
	mux := http.NewServeMux()
	s.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return s, srv, f
}

func postReview(t *testing.T, srv *httptest.Server, factID, body string) (int, map[string]any) {
	t.Helper()
	res, err := http.Post(srv.URL+"/api/facts/"+factID+"/review", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func TestManualRejectMarksFactRejected(t *testing.T) {
	s, srv, f := newReviewServer(t, "vuln")
	code, _ := postReview(t, srv, f.ID, `{"verdict":"reject","notes":"重跑 403，结论不成立"}`)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	got, _ := s.Store.FactByID(f.ID)
	if got.Status != "rejected" {
		t.Fatalf("fact status = %q, want rejected", got.Status)
	}
	reviews := s.Store.Reviews(f.ID)
	if len(reviews) != 1 || reviews[0].Verdict != "reject" || reviews[0].Notes == "" {
		t.Fatalf("review not recorded: %+v", reviews)
	}
	if reviews[0].VerifierRun != "" {
		t.Fatal("a human verdict must not claim a verifier run")
	}
}

func TestManualReviewCannotConfirm(t *testing.T) {
	_, srv, f := newReviewServer(t, "vuln")
	code, _ := postReview(t, srv, f.ID, `{"verdict":"pass","notes":"我觉得可以"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: only a verifier rerun may confirm", code)
	}
}

func TestManualReviewRequiresReason(t *testing.T) {
	_, srv, f := newReviewServer(t, "vuln")
	code, _ := postReview(t, srv, f.ID, `{"verdict":"reject","notes":"   "}`)
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: a verdict without a reason loses work", code)
	}
}

func TestManualRejectOnlyForReportableKinds(t *testing.T) {
	_, srv, f := newReviewServer(t, "note")
	code, _ := postReview(t, srv, f.ID, `{"verdict":"reject","notes":"x"}`)
	if code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: notes never reach a report", code)
	}
}

func TestUndoRejectReturnsToUnverified(t *testing.T) {
	s, srv, f := newReviewServer(t, "vuln")
	postReview(t, srv, f.ID, `{"verdict":"reject","notes":"判错了"}`)
	if code, _ := postReview(t, srv, f.ID, `{"verdict":"needs_evidence","notes":"撤销驳回"}`); code != http.StatusOK {
		t.Fatalf("undo status = %d, want 200", code)
	}
	got, _ := s.Store.FactByID(f.ID)
	if got.Status != "unverified" {
		t.Fatalf("fact status = %q, want unverified", got.Status)
	}
}
