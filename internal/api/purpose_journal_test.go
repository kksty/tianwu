package api

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/report"
	"github.com/kksty/tianwu/internal/verifier"
)

// A review is started by the verifier, not dispatch. Before D138 it passed a
// no-op event callback: the row counted events at settle but never wrote
// runs/<id>.jsonl, so the console's event stream answered "no log for run …"
// and the execution review was empty on the run an operator most wants to audit
// ("did it really re-run the proof?"). The journal must exist and be the same
// one the console reads.
func TestVerifierRunWritesEventJournal(t *testing.T) {
	s, st, f, cases := newPurposeServer(t)
	s.Exec = &recordExec{}
	vf := verifier.New(st, s.Exec, cases)
	vf.Track = s.TrackReviewRun
	vf.Log = s.OpenPurposeRunLog
	s.Verifier = vf

	if err := vf.Start(s.Ctx, f.ID, nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	var run model.Run
	waitFor(t, 5*time.Second, func() bool {
		for _, r := range st.Runs() {
			if r.Purpose == "verifier" {
				run = r
				return r.Status != model.RunRunning
			}
		}
		return false
	})
	if run.Status != model.RunDone {
		t.Fatalf("review run status = %q, want %q", run.Status, model.RunDone)
	}
	if want := filepath.Join(s.DataDir, "runs", run.ID+".jsonl"); run.LogPath != want {
		t.Fatalf("LogPath = %q, want %q", run.LogPath, want)
	}

	lines := readJournalFileLines(t, s.DataDir, run.ID)
	var kinds []string
	for _, obj := range lines {
		if kind, _ := obj["kind"].(string); kind != "" {
			kinds = append(kinds, kind)
		}
	}
	if !containsString(kinds, "session_started") || !containsString(kinds, "turn_ended") {
		t.Fatalf("review journal = %v, want the executor stream", kinds)
	}

	rec := call(t, s, http.MethodGet, "/api/runs/"+run.ID+"/events?limit=20", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"kind":"session_started"`) {
		t.Fatalf("runEvents: %d %s", rec.Code, rec.Body.String())
	}
}

// The report writing pass is a run too, and the most expensive step of
// delivery. It gets the same journal so its tool calls, duration and usage are
// visible instead of "no log for run" (D138).
func TestReportRunWritesEventJournal(t *testing.T) {
	s, st, _, cases := newPurposeServer(t)
	s.Exec = &recordExec{}
	s.Reports = &report.Generator{
		Store: st, Exec: s.Exec, CaseRoot: cases,
		Track: s.TrackPurposeRun, Log: s.OpenPurposeRunLog,
	}

	meta, err := s.Reports.Generate(s.Ctx)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if meta.RunID == "" {
		t.Fatal("report meta has no run id")
	}
	lines := readJournalFileLines(t, s.DataDir, meta.RunID)
	if len(lines) == 0 {
		t.Fatal("report run wrote no journal")
	}

	rec := call(t, s, http.MethodGet, "/api/runs/"+meta.RunID+"/events?limit=20", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"kind":"session_started"`) {
		t.Fatalf("runEvents: %d %s", rec.Code, rec.Body.String())
	}
}

// A run without a journal (e.g. a purpose run started before D138) is reported
// as "this run has no event log", not as the raw English "no log for run r_…"
// that used to reach the console.
func TestRunEventsExplainsMissingJournal(t *testing.T) {
	s, _, _, _ := newPurposeServer(t)
	rec := call(t, s, http.MethodGet, "/api/runs/r_missing/events", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "no log for run") {
		t.Fatalf("raw english message leaked: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "没有事件日志") {
		t.Fatalf("missing explanation: %s", rec.Body.String())
	}
}

func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
