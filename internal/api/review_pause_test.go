package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/model"
)

// Reviews are an operator-triggered re-proof, not a dispatched run: pausing the
// engagement stops the commander and executor work, but must not cancel or
// refuse the review the operator just asked for (D137). The review completes
// and writes its verdict while the project stays paused.
func TestProjectPauseKeepsOperatorReviewRunning(t *testing.T) {
	block := make(chan struct{})
	ex := &batchExec{block: block, body: `{"verdict":"pass","notes":"重跑一致","proof":"p"}`}
	s, st, mux := newBatchServer(t, ex)
	s.Verifier.Track = s.TrackReviewRun
	f := addVerifyFact(t, st, "vuln", "unverified")

	code, out := doPost(t, mux, "/api/facts/"+f.ID+"/verify")
	if code != http.StatusAccepted {
		t.Fatalf("verify = %d %v, want 202", code, out)
	}
	waitFor(t, 5*time.Second, func() bool { return s.Verifier.Busy(f.ID) })

	code, out = doPost(t, mux, "/api/project/pause")
	if code != http.StatusOK {
		t.Fatalf("project pause = %d %v, want 200", code, out)
	}
	if !s.paused.Load() {
		t.Fatal("project pause did not close the dispatch gate")
	}
	// Give the pause snapshot a moment to reach the registries; the review must
	// still be live afterwards.
	time.Sleep(100 * time.Millisecond)
	if !s.Verifier.Busy(f.ID) {
		t.Fatal("project pause canceled the operator's review")
	}

	close(block)
	waitFor(t, 5*time.Second, func() bool {
		got, _ := st.FactByID(f.ID)
		return got.Status == "verified"
	})
	if reviews := st.Reviews(""); len(reviews) != 1 {
		t.Fatalf("reviews = %d, want the verdict written during the pause", len(reviews))
	}
}

// The review switch is the other half of the decoupling: it cancels live
// reviews and refuses new ones, but it must not pause the project, the
// commander or executor dispatch. Resume clears it without waking anyone.
func TestReviewPauseCancelsOnlyReviews(t *testing.T) {
	block := make(chan struct{})
	ex := &batchExec{block: block}
	s, st, mux := newBatchServer(t, ex)
	s.Verifier.Track = s.TrackReviewRun
	f := addVerifyFact(t, st, "vuln", "unverified")

	if code, out := doPost(t, mux, "/api/facts/"+f.ID+"/verify"); code != http.StatusAccepted {
		t.Fatalf("verify = %d %v, want 202", code, out)
	}
	waitFor(t, 5*time.Second, func() bool { return s.Verifier.Busy(f.ID) })
	waitReviewTracked(t, s)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/verifier", nil))
	var status map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &status)
	if status["paused"] != false || status["enabled"] != true {
		t.Fatalf("verifier status = %v, want paused=false enabled=true", status)
	}

	code, out := doPost(t, mux, "/api/verifier/pause")
	if code != http.StatusOK {
		t.Fatalf("review pause = %d %v, want 200", code, out)
	}
	if s.paused.Load() {
		t.Fatal("review pause also paused the project")
	}
	if !s.ReviewsPaused() {
		t.Fatal("review pause did not set the review switch")
	}
	waitFor(t, 5*time.Second, func() bool { return !s.Verifier.Busy(f.ID) })
	run := lastVerifierRun(t, s)
	if run.Status != model.RunCanceled || run.CancelCause != model.CancelCauseReviewPause {
		t.Fatalf("review run = status %q cause %q, want canceled/%s", run.Status, run.CancelCause, model.CancelCauseReviewPause)
	}
	if got, _ := st.FactByID(f.ID); got.Status != "unverified" {
		t.Fatalf("canceled review changed the fact to %q", got.Status)
	}

	// New reviews are refused while the switch is on; the batch path too.
	if code, out := doPost(t, mux, "/api/facts/"+f.ID+"/verify"); code != http.StatusConflict {
		t.Fatalf("verify while paused = %d %v, want 409", code, out)
	}
	if code, out := doPost(t, mux, "/api/facts/verify-pending"); code != http.StatusConflict {
		t.Fatalf("batch while paused = %d %v, want 409", code, out)
	}

	if code, out := doPost(t, mux, "/api/verifier/resume"); code != http.StatusOK || out["paused"] != false {
		t.Fatalf("review resume = %d %v, want paused=false", code, out)
	}
	if s.paused.Load() {
		t.Fatal("review resume touched the project pause")
	}
	// The switch is off again: the same fact can be reviewed (the executor now
	// answers immediately with a verdict instead of blocking).
	ex.block = nil
	ex.body = `{"verdict":"pass","notes":"重跑一致","proof":"p"}`
	if code, out := doPost(t, mux, "/api/facts/"+f.ID+"/verify"); code != http.StatusAccepted {
		t.Fatalf("verify after resume = %d %v, want 202", code, out)
	}
	waitFor(t, 5*time.Second, func() bool {
		got, _ := st.FactByID(f.ID)
		return got.Status == "verified"
	})
}

// waitReviewTracked closes the small gap between the verifier claiming a fact
// (Busy true) and registering its cancel func. A real operator click happens
// well after this point; the control plane closes the same gap for delete and
// shutdown by sealing admission before taking the cancel snapshot.
func waitReviewTracked(t *testing.T, s *Server) {
	t.Helper()
	waitFor(t, 5*time.Second, func() bool {
		return s.Lifecycle.HasReviewRun()
	})
}

// A paused project can still run the operator's batch review; the batch loop
// used to stop on the project pause, which left the button queuing nothing.
func TestVerifyPendingRunsWhileProjectPaused(t *testing.T) {
	ex := &batchExec{body: `{"verdict":"pass","notes":"重跑一致","proof":"p"}`}
	s, st, mux := newBatchServer(t, ex)
	s.Verifier.Track = s.TrackReviewRun
	f := addVerifyFact(t, st, "vuln", "unverified")

	if code, out := doPost(t, mux, "/api/project/pause"); code != http.StatusOK {
		t.Fatalf("project pause = %d %v", code, out)
	}
	code, out := doPost(t, mux, "/api/facts/verify-pending")
	if code != http.StatusAccepted {
		t.Fatalf("batch while project paused = %d %v, want 202", code, out)
	}
	waitFor(t, 5*time.Second, func() bool {
		got, _ := st.FactByID(f.ID)
		return got.Status == "verified"
	})
}

// Shutdown/delete are not the project pause: they must stop reviews too, so the
// process (or the directory being removed) is not left with a live verifier.
func TestCancelAllStopsReviews(t *testing.T) {
	block := make(chan struct{})
	ex := &batchExec{block: block}
	s, st, mux := newBatchServer(t, ex)
	s.Verifier.Track = s.TrackReviewRun
	f := addVerifyFact(t, st, "vuln", "unverified")
	if code, out := doPost(t, mux, "/api/facts/"+f.ID+"/verify"); code != http.StatusAccepted {
		t.Fatalf("verify = %d %v", code, out)
	}
	waitFor(t, 5*time.Second, func() bool { return s.Verifier.Busy(f.ID) })
	waitReviewTracked(t, s)

	if n := s.CancelAll(); n != 1 {
		t.Fatalf("CancelAll canceled %d runs, want 1 (the review)", n)
	}
	waitFor(t, 5*time.Second, func() bool { return !s.Verifier.Busy(f.ID) })
	run := lastVerifierRun(t, s)
	if run.Status != model.RunCanceled || run.CancelCause != model.CancelCauseRestart {
		t.Fatalf("review run = status %q cause %q, want canceled/restart", run.Status, run.CancelCause)
	}
}

func lastVerifierRun(t *testing.T, s *Server) model.Run {
	t.Helper()
	var latest model.Run
	for _, r := range s.Store.Runs() {
		if r.Purpose == "verifier" {
			latest = r
		}
	}
	if latest.ID == "" {
		t.Fatal("no verifier run recorded")
	}
	return latest
}
