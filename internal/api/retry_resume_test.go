package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// failedRetryServer builds a project whose one direction already failed: the
// ledger rows are written directly, so the test does not need a model or a host.
// withMarker writes the AOP session pointer that makes "resume" possible.
func failedRetryServer(t *testing.T, withMarker bool) (*Server, *blockingExec, model.Intent, model.Run) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	ex := &blockingExec{}
	s := New(st, ex, dir, filepath.Join(dir, "cases"))
	s.Timeout = 0
	in, err := st.CreateIntent(model.Intent{Title: "查 /admin 越权", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := st.AddRun(model.Run{IntentID: in.ID})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().Add(-2 * time.Minute)
	ended := time.Now().Add(-time.Minute)
	run, err = st.UpdateRun(run.ID, func(r *model.Run) {
		r.Status = model.RunFailed
		r.StopReason = "error"
		r.LastState = "context 37%"
		r.Generation = 1
		r.StartedAt = started
		r.EndedAt = &ended
		r.LastTurnStartedAt = &started
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Status = model.StatusFailed
		i.RunID = run.ID
		i.Attempts = 1
	}); err != nil {
		t.Fatal(err)
	}
	if withMarker {
		writeSayMarker(t, filepath.Join(s.CaseRoot, in.ID))
	}
	t.Cleanup(func() {
		s.CancelAll()
		s.WaitIdle(5 * time.Second)
		time.Sleep(150 * time.Millisecond)
	})
	return s, ex, in, run
}

// deliveredMessage stores one accepted (delivered) message addressed to the
// direction, with an explicit run/provenance for the redelivery rules.
func deliveredMessage(t *testing.T, s *Server, intentID, runID, path string, at time.Time) model.Message {
	t.Helper()
	msg, created, err := s.Store.AddMessage(model.Message{
		FromParticipantID: "pt_operator", ToParticipantID: collab.ParticipantID(intentID),
		Kind: model.MessageUpdate, Text: "看看这个入口",
	})
	if err != nil || !created {
		t.Fatalf("add message: %v created=%v", err, created)
	}
	got, err := s.Store.UpdateMessage(msg.ID, func(m *model.Message) {
		m.Status = model.MessageAccepted
		m.DeliveredRunID = runID
		m.DeliveryPath = path
		m.CreatedAt = at
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// A resume retry must continue the previous harness session: same trajectory,
// a new generation, only a structured continuation prompt (not the full brief).
func TestRetryWithResumeContinuesPreviousSession(t *testing.T) {
	s, ex, in, prev := failedRetryServer(t, true)

	runID, err := s.RetryIntent(in.ID, "补上被夹住的验证", true)
	if err != nil {
		t.Fatalf("resume retry: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool { return len(ex.requests()) == 1 })
	req := ex.requests()[0]
	if !req.Resume {
		t.Fatal("executor request did not continue the session")
	}
	if !strings.Contains(req.Prompt, "上一轮") || !strings.Contains(req.Prompt, "stop_reason") {
		t.Fatalf("resume prompt lacks structured failure data:\n%s", req.Prompt)
	}
	if strings.Contains(req.Prompt, "上次失败") || strings.Contains(req.Prompt, "## 交接") {
		t.Fatalf("resume sent a fresh brief:\n%s", req.Prompt)
	}

	run, ok := s.Store.GetRun(runID)
	if !ok {
		t.Fatal("resumed run missing")
	}
	if run.ContinuedFrom != prev.ID {
		t.Fatalf("continued_from = %q, want %q", run.ContinuedFrom, prev.ID)
	}
	// ClaimIntent increments the claim count; the resumed turn is a new
	// generation on the same intent.
	if run.Generation != 2 {
		t.Fatalf("generation = %d, want 2", run.Generation)
	}
	got, _ := s.Store.GetIntent(in.ID)
	if len(got.Notes) == 0 || got.Notes[len(got.Notes)-1].Kind != model.IntentNoteRetry {
		t.Fatalf("retry note missing: %+v", got.Notes)
	}
}

// Without a trajectory there is nothing to keep: the explicit answer is
// resume_unavailable, not a silent fresh session.
func TestRetryWithResumeRefusesWithoutSession(t *testing.T) {
	s, _, in, _ := failedRetryServer(t, false)

	_, err := s.RetryIntent(in.ID, "", true)
	var re *retryError
	if !errors.As(err, &re) || re.code != "resume_unavailable" {
		t.Fatalf("err = %v, want resume_unavailable", err)
	}
	got, _ := s.Store.GetIntent(in.ID)
	if got.Status != model.StatusFailed || got.Hold {
		t.Fatalf("refused retry changed the direction: %+v", got)
	}
	if runs := s.Store.Runs(); len(runs) != 1 {
		t.Fatalf("refused retry started a run: %d runs", len(runs))
	}
}

// A fresh-session retry has no conversation memory: every accepted-but-unconfirmed
// message of the direction goes back on the queue, confirmed ones do not.
func TestRetryFreshSessionRequeuesUnconfirmed(t *testing.T) {
	s, ex, in, prev := failedRetryServer(t, true)
	now := time.Now()
	older := deliveredMessage(t, s, in.ID, "r_older", "prompt", now.Add(-time.Hour))
	latest := deliveredMessage(t, s, in.ID, prev.ID, "ioa", now)
	acked := deliveredMessage(t, s, in.ID, prev.ID, "ioa", now)
	if _, err := s.Store.UpdateMessage(acked.ID, func(m *model.Message) {
		m.Status = model.MessageConfirmed
		m.AckedAt = &now
	}); err != nil {
		t.Fatal(err)
	}

	runID, redelivered, err := s.retryIntentRun(in.ID, "换条路", false)
	if err != nil {
		t.Fatalf("fresh retry: %v", err)
	}
	if redelivered != 2 {
		t.Fatalf("redelivered = %d, want 2", redelivered)
	}
	waitFor(t, 5*time.Second, func() bool { return len(ex.requests()) == 1 })
	if req := ex.requests()[0]; req.Resume {
		t.Fatal("fresh retry resumed the old session")
	}
	run, _ := s.Store.GetRun(runID)
	if run.ContinuedFrom != "" {
		t.Fatalf("fresh run continued_from = %q, want empty", run.ContinuedFrom)
	}
	// The requeued messages ride the new run's prompt immediately: accepted
	// again under the fresh run, still marked as redeliveries so the model can
	// dedupe by message id.
	for _, id := range []string{older.ID, latest.ID} {
		m, _ := s.Store.MessageByID(id)
		if m.Status != model.MessageAccepted || m.Redeliveries != 1 ||
			m.DeliveredRunID != runID || m.DeliveryPath != "prompt" {
			t.Fatalf("message %s = %+v", id, m)
		}
	}
	if m, _ := s.Store.MessageByID(acked.ID); m.Status != model.MessageConfirmed {
		t.Fatalf("confirmed message was requeued: %+v", m)
	}
}

// finish() is the settle point for redelivery: a failed run puts the messages
// delivered after its last turn_started back on the queue. Confirmed rows and
// messages that arrived before the last turn began stay as they are.
func TestFinishFailedRequeuesPossiblyUnread(t *testing.T) {
	s, _, in, prev := failedRetryServer(t, false)
	now := time.Now()
	beforeTurn := deliveredMessage(t, s, in.ID, prev.ID, "prompt", now.Add(-time.Hour))
	turnStarted := time.Now()
	if _, err := s.Store.UpdateRun(prev.ID, func(r *model.Run) { r.LastTurnStartedAt = &turnStarted }); err != nil {
		t.Fatal(err)
	}
	// It was created earlier, but remained queued until after this turn began.
	afterTurn := deliveredMessage(t, s, in.ID, prev.ID, "ioa", now.Add(-time.Hour))
	acked := deliveredMessage(t, s, in.ID, prev.ID, "ioa", now)
	if _, err := s.Store.UpdateMessage(acked.ID, func(m *model.Message) {
		m.Status = model.MessageConfirmed
		m.AckedAt = &now
	}); err != nil {
		t.Fatal(err)
	}

	s.finish(prev.ID, in.ID, model.RunFailed, "provider 失败", executor.Result{StopReason: "error"}, 0)

	if m, _ := s.Store.MessageByID(afterTurn.ID); m.Status != model.MessageQueued || m.Redeliveries != 1 {
		t.Fatalf("message delivered after the last turn = %+v", m)
	}
	if m, _ := s.Store.MessageByID(beforeTurn.ID); m.Status != model.MessageAccepted {
		t.Fatalf("message from before the last turn was requeued: %+v", m)
	}
	if m, _ := s.Store.MessageByID(acked.ID); m.Status != model.MessageConfirmed {
		t.Fatalf("confirmed message was requeued: %+v", m)
	}
}

// A done run must not requeue anything: the turn completed, so what it carried
// was processed as far as the control plane can tell.
func TestFinishDoneDoesNotRequeue(t *testing.T) {
	s, _, in, prev := failedRetryServer(t, false)
	now := time.Now()
	m := deliveredMessage(t, s, in.ID, prev.ID, "ioa", now)

	s.finish(prev.ID, in.ID, model.RunDone, "", executor.Result{StopReason: "completed"}, 0)
	if got, _ := s.Store.MessageByID(m.ID); got.Status != model.MessageAccepted {
		t.Fatalf("done run requeued a message: %+v", got)
	}
}

// The retry dialog's counts: what a resumed turn re-injects vs what a fresh
// session must carry.
func TestRetryPreviewReportsResumeAndCounts(t *testing.T) {
	s, _, in, _ := failedRetryServer(t, true)
	now := time.Now()
	// One message already requeued by the failed run (resume will carry it)
	// and one still accepted (only a fresh session needs it).
	requeued := deliveredMessage(t, s, in.ID, "r_prev", "ioa", now)
	if _, err := s.Store.UpdateMessage(requeued.ID, func(m *model.Message) {
		m.Status = model.MessageQueued
		m.Redeliveries = 1
	}); err != nil {
		t.Fatal(err)
	}
	deliveredMessage(t, s, in.ID, "r_prev", "prompt", now)

	req := httptest.NewRequest(http.MethodGet, "/api/intents/"+in.ID+"/retry-preview", nil)
	req.SetPathValue("id", in.ID)
	rec := httptest.NewRecorder()
	s.retryPreview(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		CanResume     bool   `json:"can_resume"`
		ResumeError   string `json:"resume_error"`
		ResumeSession string `json:"resume_session"`
		ResumeCount   int    `json:"resume_redeliver"`
		FreshCount    int    `json:"new_session_redeliver"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.CanResume || out.ResumeError != "" || out.ResumeSession != "sess-1" {
		t.Fatalf("preview resume = %+v", out)
	}
	if out.ResumeCount != 1 || out.FreshCount != 2 {
		t.Fatalf("preview counts = %+v", out)
	}
}

// The preview must explain why a resume is unavailable instead of leaving the
// console to guess from a 409.
func TestRetryPreviewExplainsMissingSession(t *testing.T) {
	s, _, in, _ := failedRetryServer(t, false)
	req := httptest.NewRequest(http.MethodGet, "/api/intents/"+in.ID+"/retry-preview", nil)
	req.SetPathValue("id", in.ID)
	rec := httptest.NewRecorder()
	s.retryPreview(rec, req)
	var out struct {
		CanResume   bool   `json:"can_resume"`
		ResumeError string `json:"resume_error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.CanResume || !strings.Contains(out.ResumeError, "会话") {
		t.Fatalf("preview = %+v", out)
	}
}

// The one automatic step: a resume that discovers the host no longer has the
// session falls back to a fresh session, visibly (warning on the old run).
func TestResumeGoneFallsBackToFreshSession(t *testing.T) {
	s, ex, in, prev := failedRetryServer(t, true)

	s.fallbackToFreshSession(prev.ID, in.ID)
	waitFor(t, 5*time.Second, func() bool { return len(ex.requests()) == 1 })
	if req := ex.requests()[0]; req.Resume {
		t.Fatal("fallback started a resume, not a fresh session")
	}
	var fresh model.Run
	for _, r := range s.Store.Runs() {
		if r.ID != prev.ID {
			fresh = r
		}
	}
	if fresh.ID == "" || fresh.ContinuedFrom != "" || fresh.Generation != 2 {
		t.Fatalf("fallback run = %+v", fresh)
	}
	old, _ := s.Store.GetRun(prev.ID)
	if len(old.Warnings) == 0 || !strings.Contains(old.Warnings[0], "原会话") {
		t.Fatalf("fallback was silent: %+v", old.Warnings)
	}
	if got, _ := s.Store.GetIntent(in.ID); got.Status != model.StatusRunning && got.Status != model.StatusClaimed {
		t.Fatalf("intent after fallback = %+v", got)
	}
}
