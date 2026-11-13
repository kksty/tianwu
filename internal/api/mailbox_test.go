package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/aopexec"
	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/model"
)

// makeCommanderLive opens the one state in which IOA can reach the commander:
// a running commander run plus the AOP session marker in its case directory.
func (f *agentFixture) makeCommanderLive(t *testing.T) model.Run {
	t.Helper()
	run, err := f.p.Store.AddRun(model.Run{Purpose: "commander", Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(f.srv.CommanderDir(), ".tw")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := `{"url":"http://127.0.0.1:18766","agent":"local","session":"s_cmd"}`
	if err := os.WriteFile(filepath.Join(dir, "aop-session.json"), []byte(marker), 0o644); err != nil {
		t.Fatal(err)
	}
	return run
}

func TestCommanderMessagesFollowItsFrozenProfileRoute(t *testing.T) {
	for _, selected := range []bool{false, true} {
		t.Run(fmt.Sprint(selected), func(t *testing.T) {
			f := newAgentFixture(t)
			run := f.makeCommanderLive(t)
			_, err := f.p.Store.UpdateRun(run.ID, func(r *model.Run) {
				r.ResolvedProfile = "strong"
				if selected {
					r.ProfileSource = "commander_strong"
				} else {
					r.ProfileSource = "default"
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			var profile string
			f.srv.Say = func(_ context.Context, in model.Intent, _ aopexec.SessionInfo, _ string, _ bool) (string, error) {
				profile = in.Profile
				return "accepted", nil
			}
			rec := f.call(t, f.srcID, http.MethodPost, "/api/messages", sendBody(collab.CommanderID, "route", "结构化消息"))
			if rec.Code != http.StatusAccepted {
				t.Fatal(rec.Body.String())
			}
			_, delivery := sendResult(t, rec)
			want := ""
			if selected {
				want = "strong"
			}
			if delivery != model.MessageAccepted || profile != want {
				t.Fatalf("delivery=%s profile=%s want=%s", delivery, profile, want)
			}
		})
	}
}

func sendResult(t *testing.T, rec *httptest.ResponseRecorder) (model.Message, string) {
	t.Helper()
	var out struct {
		Message  model.Message `json:"message"`
		Delivery string        `json:"delivery"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Message, out.Delivery
}

// The commander is a participant, so a message for it must be stored like any
// other when its round is not live — never dropped, never turned into an
// untracked harness turn.
func TestMessageToCommanderQueuesWhenRoundIsNotLive(t *testing.T) {
	f := newAgentFixture(t)
	rec := f.call(t, f.srcID, http.MethodPost, "/api/messages",
		sendBody(collab.CommanderID, "k1", "8080 是管理口，别硬打"))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: %d %s", rec.Code, rec.Body.String())
	}
	msg, delivery := sendResult(t, rec)
	if delivery != model.MessageQueued || msg.Status != model.MessageQueued {
		t.Fatalf("delivery = %q status = %q", delivery, msg.Status)
	}
	if msg.ToParticipantID != collab.CommanderID {
		t.Fatalf("to = %q", msg.ToParticipantID)
	}
	if f.say.count() != 0 {
		t.Fatalf("idle round must not be woken by IOA, say calls = %d", f.say.count())
	}
	if pending := f.p.Store.PendingFor(collab.CommanderID); len(pending) != 1 {
		t.Fatalf("pending = %d", len(pending))
	}
}

// A queued message rides the next commander round's prompt and is marked
// delivered there, so it is never injected twice.
func TestCommanderRoundCarriesQueuedMessageOnce(t *testing.T) {
	f := newAgentFixture(t)
	f.call(t, f.srcID, http.MethodPost, "/api/messages",
		sendBody(collab.CommanderID, "k1", "需要你确认 e 系列是否同源"))

	section := f.srv.inboxSection(collab.CommanderID, "prompt:r_cmd", "r_cmd", "结束语", 0)
	if !strings.Contains(section, "需要你确认 e 系列是否同源") {
		t.Fatalf("section misses the text: %q", section)
	}
	if !strings.Contains(section, f.srcID.ParticipantID) {
		t.Fatalf("section misses the sender: %q", section)
	}
	if pending := f.p.Store.PendingFor(collab.CommanderID); len(pending) != 0 {
		t.Fatalf("pending after injection = %d", len(pending))
	}
	if again := f.srv.inboxSection(collab.CommanderID, "prompt:r_cmd", "r_cmd", "结束语", 0); again != "" {
		t.Fatalf("second round re-injected: %q", again)
	}
}

// A live commander round takes the message through IOA, with the addressing in
// the text (IOA only carries a session target).
func TestMessageToCommanderDeliversIntoLiveRound(t *testing.T) {
	f := newAgentFixture(t)
	f.makeCommanderLive(t)
	rec := f.call(t, f.srcID, http.MethodPost, "/api/messages",
		sendBody(collab.CommanderID, "k1", "这台机器的 sqlmap 能用"))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: %d %s", rec.Code, rec.Body.String())
	}
	msg, delivery := sendResult(t, rec)
	if delivery != model.MessageAccepted || msg.Status != model.MessageAccepted {
		t.Fatalf("delivery = %q status = %q err = %q", delivery, msg.Status, msg.Error)
	}
	if f.say.count() != 1 {
		t.Fatalf("say calls = %d", f.say.count())
	}
	call := f.say.last()
	if call.session != "s_cmd" {
		t.Fatalf("session = %q", call.session)
	}
	if !strings.Contains(call.text, "from="+f.srcID.ParticipantID) {
		t.Fatalf("text misses sender: %q", call.text)
	}
	if !strings.Contains(call.text, msg.ID) {
		t.Fatalf("text misses message id: %q", call.text)
	}
}

func TestPausedProjectQueuesMessagesWithoutInjectingCommanderTurn(t *testing.T) {
	f := newAgentFixture(t)
	f.makeCommanderLive(t)
	f.srv.SetPaused(true)
	rec := f.call(t, f.srcID, http.MethodPost, "/api/messages", sendBody(collab.CommanderID, "paused-message", "result ready"))
	if rec.Code != http.StatusAccepted {
		t.Fatal(rec.Body.String())
	}
	msg, delivery := sendResult(t, rec)
	if delivery != model.MessageQueued || msg.Status != model.MessageQueued || f.say.count() != 0 {
		t.Fatalf("paused delivery=%s status=%s IOA calls=%d", delivery, msg.Status, f.say.count())
	}
}

// A retry with the same idempotency key must not deliver the same message twice.
func TestMessageToCommanderRetryDoesNotDeliverTwice(t *testing.T) {
	f := newAgentFixture(t)
	f.makeCommanderLive(t)
	body := sendBody(collab.CommanderID, "same-key", "只投一次")
	first := f.call(t, f.srcID, http.MethodPost, "/api/messages", body)
	if first.Code != http.StatusAccepted {
		t.Fatalf("first: %d %s", first.Code, first.Body.String())
	}
	second := f.call(t, f.srcID, http.MethodPost, "/api/messages", body)
	if second.Code != http.StatusOK {
		t.Fatalf("retry: %d %s", second.Code, second.Body.String())
	}
	if f.say.count() != 1 {
		t.Fatalf("delivered %d times", f.say.count())
	}
}

// The commander has no generation: addressing it with one is a client error,
// not a silent mismatch.
func TestMessageToCommanderRejectsGeneration(t *testing.T) {
	f := newAgentFixture(t)
	body := sendBody(collab.CommanderID, "k", "x")
	body["to_generation"] = 3
	if rec := f.call(t, f.srcID, http.MethodPost, "/api/messages", body); rec.Code != http.StatusBadRequest {
		t.Fatalf("status: %d %s", rec.Code, rec.Body.String())
	}
}

// A sender must be able to see that its message is still queued (definitely
// unread) and take it back; a retry after delivery is a conflict, not a delete.
func TestSenderSeesQueuedMessageAndRetractsIt(t *testing.T) {
	f := newAgentFixture(t)
	rec := f.call(t, f.srcID, http.MethodPost, "/api/messages",
		sendBody(collab.ParticipantID(f.target.ID), "k1", "这条还没被读到"))
	msg, _ := sendResult(t, rec)

	sent := f.call(t, f.srcID, http.MethodGet, "/api/messages?box=sent", nil)
	if !strings.Contains(sent.Body.String(), msg.ID) {
		t.Fatalf("sent box misses the message: %s", sent.Body.String())
	}

	if rec := f.call(t, f.tgtID, http.MethodPost, "/api/messages/"+msg.ID+"/retract", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("foreign retract: %d %s", rec.Code, rec.Body.String())
	}
	rec = f.call(t, f.srcID, http.MethodPost, "/api/messages/"+msg.ID+"/retract", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("retract: %d %s", rec.Code, rec.Body.String())
	}
	// A withdrawn message is gone, not a tombstone: nothing was delivered, so
	// there is nothing to audit and the text must not survive.
	if _, ok := f.p.Store.MessageByID(msg.ID); ok {
		t.Fatal("retracted message still in the ledger")
	}
	if pending := f.p.Store.PendingFor(collab.ParticipantID(f.target.ID)); len(pending) != 0 {
		t.Fatalf("retracted message still queued: %d", len(pending))
	}
	after := f.call(t, f.srcID, http.MethodGet, "/api/messages?box=sent", nil)
	if strings.Contains(after.Body.String(), msg.ID) {
		t.Fatalf("sent box still lists the retracted message: %s", after.Body.String())
	}
	if rec := f.call(t, f.srcID, http.MethodPost, "/api/messages/"+msg.ID+"/retract", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("second retract: %d %s", rec.Code, rec.Body.String())
	}
}

// Once a message is delivered there is no undo: no recall, and no automatic
// "ignore that" reply either. The answer is a conflict; a real correction is a
// new message the sender writes.
func TestRetractAfterDeliveryIsRefused(t *testing.T) {
	f := newAgentFixture(t)
	f.makeLive(t)
	rec := f.call(t, f.srcID, http.MethodPost, "/api/messages",
		sendBody(collab.ParticipantID(f.target.ID), "k1", "已经送出去了"))
	msg, delivery := sendResult(t, rec)
	if delivery != model.MessageAccepted {
		t.Fatalf("delivery = %q", delivery)
	}
	before := len(f.p.Store.Messages(0))
	rec = f.call(t, f.srcID, http.MethodPost, "/api/messages/"+msg.ID+"/retract", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("retract delivered: %d %s", rec.Code, rec.Body.String())
	}
	if _, ok := f.p.Store.MessageByID(msg.ID); !ok {
		t.Fatal("delivered message was deleted")
	}
	if after := len(f.p.Store.Messages(0)); after != before {
		t.Fatalf("retract wrote %d extra rows", after-before)
	}
}

// A queued message for an intent rides that intent's next prompt, exactly like
// the commander's round: queued is a promise, not a mailbox nobody opens.
func TestExecutorPromptCarriesQueuedMessage(t *testing.T) {
	f := newAgentFixture(t)
	rec := f.call(t, f.srcID, http.MethodPost, "/api/messages",
		sendBody(collab.ParticipantID(f.target.ID), "k1", "这条要进下一轮提示词"))
	if _, delivery := sendResult(t, rec); delivery != model.MessageQueued {
		t.Fatalf("delivery = %q", delivery)
	}
	participant := collab.ParticipantID(f.target.ID)
	section := f.srv.inboxSection(participant, "prompt:r_next", "r_next", "结束语", 0)
	if !strings.Contains(section, "这条要进下一轮提示词") || !strings.Contains(section, f.srcID.ParticipantID) {
		t.Fatalf("section = %q", section)
	}
	if again := f.srv.inboxSection(participant, "prompt:r_next", "r_next", "结束语", 0); again != "" {
		t.Fatalf("re-injected: %q", again)
	}
}

// The injected inbox text says where the sender sits. The operator and the
// commander write through the same project API and produce the same message
// shape, so "from=operator" must not be read as the commander speaking, and a
// peer direction is named as one. The annotation is prompt text only: the
// ledger row keeps its sender and body (D86).
func TestInboxMessageNamesWhereTheSenderSits(t *testing.T) {
	s, p := newBindingServer(t)
	self, err := p.Store.CreateIntent(model.Intent{Title: "收件方向"})
	if err != nil {
		t.Fatal(err)
	}
	participant := collab.ParticipantID(self.ID)
	senders := []string{collab.OperatorID, collab.CommanderID, collab.ParticipantID("in_peer")}
	for i, from := range senders {
		if _, _, err := p.Store.AddMessage(model.Message{
			FromParticipantID: from, ToParticipantID: participant,
			Kind: "note", Text: fmt.Sprintf("正文 %d", i),
		}); err != nil {
			t.Fatal(err)
		}
	}
	section := s.inboxSection(participant, "prompt:r_inbox", "r_inbox", "结束语", 0)
	for _, want := range []string{
		"from=operator（操作员/控制台令牌，不是指挥官）",
		"from=commander（本项目指挥官模型）",
		"from=pt_in_peer（执行者方向 in_peer）",
	} {
		if !strings.Contains(section, want) {
			t.Fatalf("inbox section missing %q:\n%s", want, section)
		}
	}
	// The ledger is untouched: annotation only, no body or sender rewrite.
	for _, m := range p.Store.Messages(0) {
		if strings.Contains(m.Text, "（") || strings.Contains(m.Text, "操作员/控制台令牌") {
			t.Fatalf("annotation leaked into the stored message: %+v", m)
		}
		if m.FromParticipantID != collab.OperatorID && m.FromParticipantID != collab.CommanderID &&
			m.FromParticipantID != collab.ParticipantID("in_peer") {
			t.Fatalf("message sender rewritten: %+v", m)
		}
	}
}

// A say from the commander is one message row with the commander as sender, so
// the thread is symmetric instead of a second interjection history (D86).
func TestSayFromCommanderRecordsOneMessage(t *testing.T) {
	s, _, in := newSteerServer(t)
	writeSayMarker(t, filepath.Join(s.CaseRoot, in.ID))
	s.Say = func(context.Context, model.Intent, aopexec.SessionInfo, string, bool) (string, error) {
		return "ioa-7", nil
	}
	req := httptest.NewRequest(http.MethodPost, "/api/intents/"+in.ID+"/say",
		strings.NewReader(`{"text":"确认一下 e 系列","interrupt":false}`))
	req.SetPathValue("id", in.ID)
	req.Header.Set("X-Tw-Actor", collab.CommanderID)
	rec := httptest.NewRecorder()
	s.sayIntent(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("say: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		MessageID string `json:"message_id"`
		Delivery  string `json:"delivery"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	msg, ok := s.Store.MessageByID(out.MessageID)
	if !ok || msg.FromParticipantID != collab.CommanderID ||
		msg.ToParticipantID != collab.ParticipantID(in.ID) || msg.Status != model.MessageAccepted {
		t.Fatalf("message = %+v", msg)
	}
	if out.Delivery != model.MessageAccepted {
		t.Fatalf("delivery = %q", out.Delivery)
	}
	s.CancelAll()
	if !s.WaitIdle(2 * time.Second) {
		t.Fatal("run did not stop")
	}
}
