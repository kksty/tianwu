package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
)

// readJournalFile returns each line of a run journal as a decoded object, and
// fails on any line that is not a whole JSON object. That failure mode is the
// point of the single-writer journal: interleaved writes would produce half
// lines and this assertion would catch them.
func readJournalFileLines(t *testing.T, dir, runID string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "runs", runID+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for i, ln := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(ln), &obj); err != nil {
			t.Fatalf("line %d is not a JSON object (%v): %q\ndata:\n%s", i, err, ln, data)
		}
		out = append(out, obj)
	}
	return out
}

func openTestJournal(t *testing.T, s *Server, runID string) {
	t.Helper()
	dir := filepath.Join(s.DataDir, "runs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := s.openJournal(runID, filepath.Join(dir, runID+".jsonl")); err != nil {
		t.Fatal(err)
	}
}

// 50 goroutines write harness events and synthetic message events through the
// same journal. The lock must make every line complete: this is the regression
// that protects all run observability.
func TestJournalSerializesConcurrentWriters(t *testing.T) {
	s, _ := newBindingServer(t)
	const runID = "r_concurrent"
	openTestJournal(t, s, runID)
	defer s.closeJournal(runID)

	const writers = 50
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			if err := s.appendRunEvent(runID, executor.Event{
				Kind: executor.KindToolCall, Tool: "bash", Text: fmt.Sprintf("event-%d", i),
			}); err != nil {
				t.Errorf("appendRunEvent %d: %v", i, err)
			}
		}(i)
		go func(i int) {
			defer wg.Done()
			msg := model.Message{
				ID: fmt.Sprintf("m_%d", i), FromParticipantID: "pt_in_a",
				ToParticipantID: "pt_in_b", Text: "并发投递",
			}
			s.appendCollabEvent(runID, msg, "in", model.MessageAccepted)
		}(i)
	}
	wg.Wait()

	lines := readJournalFileLines(t, s.DataDir, runID)
	if len(lines) != 2*writers {
		t.Fatalf("journal has %d lines, want %d", len(lines), 2*writers)
	}
	kinds := map[string]int{}
	for _, obj := range lines {
		if kind, _ := obj["kind"].(string); kind != "" {
			kinds[kind]++
		}
	}
	if kinds["collab_message"] != writers || kinds[executor.KindToolCall] != writers {
		t.Fatalf("kinds = %v", kinds)
	}
}

// Delivering into a live run must be visible from that run's event stream (and
// from the console's runEvents reader), not just in the message table.
func TestCollabMessageIsVisibleInRunEvents(t *testing.T) {
	f := newAgentFixture(t)
	run := f.makeLive(t)
	openTestJournal(t, f.srv, run.ID)
	defer f.srv.closeJournal(run.ID)
	// The sender's identity names a run id with no ledger row in the fixture;
	// the journal registry is keyed by run id, so the "out" side can still be
	// asserted the same way dispatch would register it.
	openTestJournal(t, f.srv, f.srcID.RunID)
	defer f.srv.closeJournal(f.srcID.RunID)

	rec := f.call(t, f.srcID, http.MethodPost, "/api/messages",
		sendBody(f.tgtID.ParticipantID, "k-journal", "这一句会进事件流"))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("send: %d %s", rec.Code, rec.Body.String())
	}
	var sent struct {
		Message model.Message `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &sent); err != nil {
		t.Fatal(err)
	}

	incoming := readJournalFileLines(t, f.srv.DataDir, run.ID)
	found := false
	for _, obj := range incoming {
		if obj["kind"] != "collab_message" {
			continue
		}
		if obj["message_id"] != sent.Message.ID || obj["direction"] != "in" ||
			obj["delivery"] != model.MessageAccepted {
			t.Fatalf("in event = %v", obj)
		}
		if obj["from"] != f.srcID.ParticipantID || obj["to"] != f.tgtID.ParticipantID {
			t.Fatalf("in event addressing = %v", obj)
		}
		found = true
	}
	if !found {
		t.Fatalf("no collab_message in recipient journal: %v", incoming)
	}

	outgoing := readJournalFileLines(t, f.srv.DataDir, f.srcID.RunID)
	if len(outgoing) != 1 || outgoing[0]["direction"] != "out" || outgoing[0]["message_id"] != sent.Message.ID {
		t.Fatalf("sender journal = %v", outgoing)
	}

	// The console route reads the same file, so the delivery is visible there
	// too — no second event source, no read-side merge.
	rec = call(t, f.srv, http.MethodGet, "/api/runs/"+run.ID+"/events?limit=20", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"kind":"collab_message"`) {
		t.Fatalf("runEvents: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"message_id":"`+sent.Message.ID+`"`) {
		t.Fatalf("runEvents missing message id: %s", rec.Body.String())
	}
}

// The journal keeps an excerpt, not the mailbox: a long message is clipped and
// tokens/credentials never enter it (the event only carries participant ids).
func TestCollabEventClipsMessageText(t *testing.T) {
	s, _ := newBindingServer(t)
	const runID = "r_clip"
	openTestJournal(t, s, runID)
	defer s.closeJournal(runID)

	long := strings.Repeat("密", 1000)
	s.appendCollabEvent(runID, model.Message{
		ID: "m_long", FromParticipantID: "pt_in_a", ToParticipantID: "pt_in_b", Text: long,
	}, "in", model.MessageAccepted)

	lines := readJournalFileLines(t, s.DataDir, runID)
	if len(lines) != 1 {
		t.Fatalf("lines = %d", len(lines))
	}
	text, _ := lines[0]["text"].(string)
	if runes := []rune(text); len(runes) > 401 {
		t.Fatalf("clipped text is %d runes, want <= 401", len(runes))
	}
	if _, ok := lines[0]["token"]; ok {
		t.Fatal("journal event carries a token field")
	}
}

// A message delivered after the run settled has nowhere to go: dispatch has
// already closed the journal. The delivery must not panic, and the log must not
// grow a half-written or resurrected event.
func TestMessageToFinishedRunIsNotJournaled(t *testing.T) {
	f := newAgentFixture(t)
	run := f.makeLive(t)
	openTestJournal(t, f.srv, run.ID)
	f.srv.closeJournal(run.ID)

	rec := f.call(t, f.srcID, http.MethodPost, "/api/messages",
		sendBody(f.tgtID.ParticipantID, "k-late", "关掉之后才到"))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("send: %d %s", rec.Code, rec.Body.String())
	}
	// The run is still "live" from the message path's point of view; the journal
	// is the only thing that knows the writer is gone. Silence is correct: the
	// message row is the record, and a settled run cannot receive it.
	if lines := readJournalFileLines(t, f.srv.DataDir, run.ID); len(lines) != 0 {
		t.Fatalf("settled run journal grew: %v", lines)
	}
}

// A delivery into the commander's live turn is journalled on the commander run
// as well: "who interrupted the commander" is part of the same audit surface as
// an executor run.
func TestCommanderMessageDeliveryIsJournaled(t *testing.T) {
	f := newAgentFixture(t)
	run, err := f.p.Store.AddRun(model.Run{Purpose: "commander", Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(f.srv.CommanderDir(), ".tw")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := `{"url":"http://127.0.0.1:18766","agent":"local","session":"s_commander"}`
	if err := os.WriteFile(filepath.Join(dir, "aop-session.json"), []byte(marker), 0o644); err != nil {
		t.Fatal(err)
	}
	openTestJournal(t, f.srv, run.ID)
	defer f.srv.closeJournal(run.ID)

	msg, isNew, err := f.p.Store.AddMessage(model.Message{
		ProjectID: f.p.ID, FromParticipantID: f.srcID.ParticipantID,
		ToParticipantID: collab.CommanderID, Kind: "question", Text: "指挥官，看下这条",
		IdempotencyKey: "k-commander", Status: model.MessageQueued,
	})
	if err != nil || !isNew {
		t.Fatalf("AddMessage: new=%v err=%v", isNew, err)
	}
	delivered := f.srv.deliverToCommander(context.Background(), msg)
	if delivered.Status != model.MessageAccepted {
		t.Fatalf("delivery = %+v", delivered)
	}

	lines := readJournalFileLines(t, f.srv.DataDir, run.ID)
	if len(lines) != 1 || lines[0]["kind"] != "collab_message" ||
		lines[0]["message_id"] != msg.ID || lines[0]["direction"] != "in" ||
		lines[0]["delivery"] != model.MessageAccepted {
		t.Fatalf("commander journal = %v", lines)
	}
}

// A queued message injected into a run's prompt is auditable from the run log:
// the prompt itself lives only in the harness session, so without this event
// "was it injected, and how big was it" would be unanswerable afterwards.
func TestInjectionRecordedInRunEvents(t *testing.T) {
	f := newAgentFixture(t)
	f.srv.Exec = &recordExec{}
	if err := f.p.SetNodeBinding([]string{"local"}, "local"); err != nil {
		t.Fatal(err)
	}
	f.srv.LocalNode = "local"

	// Queue a message while the target is not running, then dispatch it: the
	// dispatch prompt carries the message and must journal the injection.
	rec := f.call(t, f.srcID, http.MethodPost, "/api/messages",
		sendBody(f.tgtID.ParticipantID, "k-inject", "派发时带上的那句话"))
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"delivery":"queued"`) {
		t.Fatalf("queue: %d %s", rec.Code, rec.Body.String())
	}
	runID, err := f.srv.StartRun(f.target.ID)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	waitRun(t, f.p, runID)

	lines := readJournalFileLines(t, f.srv.DataDir, runID)
	var injected map[string]any
	executorEvents := 0
	for _, obj := range lines {
		switch obj["kind"] {
		case "injected":
			injected = obj
		case executor.KindSession, executor.KindTurnEnd:
			executorEvents++
		}
	}
	if injected == nil {
		t.Fatalf("no injected event in run journal: %v", lines)
	}
	if injected["source"] != "message" || injected["prompt_section"] != "inbox" ||
		injected["run_id"] != runID {
		t.Fatalf("injected event = %v", injected)
	}
	if bytes, _ := injected["bytes"].(float64); bytes <= 0 {
		t.Fatalf("injected bytes = %v, want > 0", injected["bytes"])
	}
	// The injection is journaled next to the harness events, in one file, with
	// the normal event shape; it does not replace them.
	if executorEvents == 0 {
		t.Fatalf("journal lost the harness events: %v", lines)
	}
}
