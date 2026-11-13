package api

// Behaviour tests for the communication surface: a paused project persists fact
// notices without delivering them, a notice's delivery never creates a run, an
// ACK is only a receipt, queue status is scoped to the caller, and a full queue
// is answered with an explicit 429 instead of a 500 or a silent drop.
//
// Everything runs against a temporary project ledger and fake transport; no
// browser, harness, model or real project data is touched.

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

func liveNoticeFixture(t *testing.T) *agentFixture {
	t.Helper()
	f := newAgentFixture(t)
	if _, err := f.p.Store.UpdateIntent(f.target.ID, func(in *model.Intent) {
		in.InterestEntityRefs = []string{"host:10.0.0.5"}
	}); err != nil {
		t.Fatal(err)
	}
	f.makeLive(t)
	return f
}

func addSharedFact(t *testing.T, f *agentFixture) model.Fact {
	t.Helper()
	fact, err := f.p.Store.AddFact(model.Fact{
		IntentID:  f.src.ID,
		Kind:      "host",
		Title:     "10.0.0.5 可达",
		EntityRef: "host:10.0.0.5",
	})
	if err != nil {
		t.Fatal(err)
	}
	return fact
}

func noticeRowsFor(t *testing.T, s *store.Store, recipient string) []model.Message {
	t.Helper()
	out := []model.Message{}
	for _, m := range s.Messages(0) {
		if m.ToParticipantID == recipient && m.NoticeFactID != "" {
			out = append(out, m)
		}
	}
	return out
}

func TestPausedRoutingPersistsNoticesWithoutDelivering(t *testing.T) {
	f := liveNoticeFixture(t)
	to := collab.ParticipantID(f.target.ID)
	f.srv.paused.Store(true)
	fact := addSharedFact(t, f)
	f.srv.routeAndDeliver()

	if got := f.say.count(); got != 0 {
		t.Fatalf("暂停时分发通知不应投递到会话，say 调用了 %d 次", got)
	}
	// The notice is durable while the project is paused: routing is persistence,
	// delivery is a separate gate.
	rows := noticeRowsFor(t, f.p.Store, to)
	if len(rows) != 1 || rows[0].Status != model.MessageQueued || rows[0].NoticeFactID != fact.ID {
		t.Fatalf("暂停时应只持久化一条排队通知：%+v", rows)
	}
	if got := len(f.p.Store.Runs()); got != 1 {
		t.Fatalf("暂停时不应创建运行，run 数 = %d", got)
	}

	f.srv.paused.Store(false)
	f.srv.routeAndDeliver()
	if got := f.say.count(); got != 1 {
		t.Fatalf("恢复后应投递一次，say 调用了 %d 次", got)
	}
	last := f.say.last()
	if !strings.Contains(last.text, "from="+model.NoticeSenderID) || !strings.Contains(last.text, fact.ID) {
		t.Fatalf("投递文本应带机械发送者与事实 ID：%q", last.text)
	}
	for _, m := range noticeRowsFor(t, f.p.Store, to) {
		if m.Status != model.MessageAccepted {
			t.Fatalf("投递后状态应为 accepted：%+v", m)
		}
	}
	if got := len(f.p.Store.Runs()); got != 1 {
		t.Fatalf("投递通知不应创建运行，run 数 = %d", got)
	}
}

func TestNoticeRidesNextPromptWhenDirectionIsBetweenRuns(t *testing.T) {
	f := newAgentFixture(t)
	if _, err := f.p.Store.UpdateIntent(f.target.ID, func(in *model.Intent) {
		in.InterestEntityRefs = []string{"host:10.0.0.5"}
	}); err != nil {
		t.Fatal(err)
	}
	to := collab.ParticipantID(f.target.ID)
	fact := addSharedFact(t, f)
	f.srv.routeAndDeliver()

	if got := f.say.count(); got != 0 {
		t.Fatalf("目标未运行时不应 IOA 投递：%d", got)
	}
	rows := noticeRowsFor(t, f.p.Store, to)
	if len(rows) != 1 || rows[0].Status != model.MessageQueued {
		t.Fatalf("目标未运行时通知应留在队列：%+v", rows)
	}
	if got := len(f.p.Store.Runs()); got != 0 {
		t.Fatalf("路由/投递不应创建运行，run 数 = %d", got)
	}
	// The durable queue is what the next prompt carries; that is the other
	// legitimate consumer besides an already-live session.
	inbox := f.srv.inboxSection(to, "prompt:fixture", "r_fixture", "", 0)
	if !strings.Contains(inbox, fact.ID) || !strings.Contains(inbox, "from="+model.NoticeSenderID) {
		t.Fatalf("下一次提示词应带上排队通知：%q", inbox)
	}
	if got := len(f.p.Store.Runs()); got != 0 {
		t.Fatalf("注入排队通知不应创建运行，run 数 = %d", got)
	}
}

func TestNoticeAckIsOnlyAReceipt(t *testing.T) {
	f := liveNoticeFixture(t)
	to := collab.ParticipantID(f.target.ID)
	addSharedFact(t, f)
	f.srv.routeAndDeliver()

	rows := noticeRowsFor(t, f.p.Store, to)
	if len(rows) != 1 || rows[0].Status != model.MessageAccepted {
		t.Fatalf("通知应先被投递：%+v", rows)
	}
	runsBefore := len(f.p.Store.Runs())
	saysBefore := f.say.count()
	rec := f.call(t, f.tgtID, http.MethodPost, "/api/messages/"+rows[0].ID+"/ack", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("ack = %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Message model.Message `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Message.Status != model.MessageConfirmed || out.Message.AckedAt == nil {
		t.Fatalf("ack 后状态不对：%+v", out.Message)
	}
	if got := len(f.p.Store.Runs()); got != runsBefore {
		t.Fatalf("ACK 不应创建运行：before=%d after=%d", runsBefore, got)
	}
	if got := f.say.count(); got != saysBefore {
		t.Fatalf("ACK 不应再触发投递：before=%d after=%d", saysBefore, got)
	}
}

func communicationRecorder(t *testing.T, s *Server, target string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	s.registerCommunicationRoutes(mux)
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestCommunicationStatusScopesParticipantQueues(t *testing.T) {
	s, p := newBindingServer(t)
	first, err := p.Store.CreateIntent(model.Intent{Title: "甲方", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.Store.CreateIntent(model.Intent{Title: "乙方", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	firstID := collab.ParticipantID(first.ID)
	secondID := collab.ParticipantID(second.ID)
	for _, to := range []string{firstID, secondID} {
		if _, _, err := p.Store.AddMessage(model.Message{FromParticipantID: "pt_flood", ToParticipantID: to, Text: "排队"}); err != nil {
			t.Fatal(err)
		}
	}

	var participant struct {
		Queues      []store.QueueStats `json:"queues"`
		MaxMessages int                `json:"max_messages"`
		MaxBytes    int                `json:"max_bytes"`
		Paused      bool               `json:"paused"`
	}
	mux := http.NewServeMux()
	s.registerCommunicationRoutes(mux)
	req := httptest.NewRequest(http.MethodGet, "/api/communication", nil)
	req = req.WithContext(WithIdentity(req.Context(), collab.Identity{
		ProjectID: p.ID, ParticipantID: firstID, IntentID: first.ID, RunID: "r_first",
	}))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("participant status = %d %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &participant); err != nil {
		t.Fatal(err)
	}
	if len(participant.Queues) != 1 || participant.Queues[0].Recipient != firstID {
		t.Fatalf("参与者只应看到自己的队列：%+v", participant.Queues)
	}
	if participant.MaxMessages != store.MaxQueuedMessages || participant.MaxBytes != store.MaxQueuedBytes {
		t.Fatalf("响应缺少队列上限：%+v", participant)
	}

	// A participant cannot ask about another direction.
	req = httptest.NewRequest(http.MethodGet, "/api/communication?recipient="+url.QueryEscape(secondID), nil)
	req = req.WithContext(WithIdentity(req.Context(), collab.Identity{ProjectID: p.ID, ParticipantID: firstID, IntentID: first.ID}))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("跨收件人查询应被拒绝，得到 %d %s", rec.Code, rec.Body.String())
	}

	// The operator view (no participant identity) sees every queue, and may
	// narrow to one recipient.
	var operator struct {
		Queues []store.QueueStats `json:"queues"`
	}
	rec = communicationRecorder(t, s, "/api/communication")
	if rec.Code != http.StatusOK {
		t.Fatalf("operator status = %d %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &operator); err != nil {
		t.Fatal(err)
	}
	if len(operator.Queues) != 2 {
		t.Fatalf("操作员应看到全部队列：%+v", operator.Queues)
	}
	rec = communicationRecorder(t, s, "/api/communication?recipient="+url.QueryEscape(secondID))
	operator.Queues = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &operator); err != nil {
		t.Fatal(err)
	}
	if len(operator.Queues) != 1 || operator.Queues[0].Recipient != secondID {
		t.Fatalf("按收件人过滤 = %+v", operator.Queues)
	}
}

func TestBackpressureResponseCarriesQueueAndRetryCode(t *testing.T) {
	s, p := newBindingServer(t)
	in, err := p.Store.CreateIntent(model.Intent{Title: "收件方向", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	to := collab.ParticipantID(in.ID)
	// Fill the byte budget in one row so the next admission is refused without
	// paying one durable commit per placeholder.
	if _, _, err := p.Store.AddMessage(model.Message{FromParticipantID: "pt_flood", ToParticipantID: to, Text: strings.Repeat("x", store.MaxQueuedBytes)}); err != nil {
		t.Fatal(err)
	}
	_, _, err = p.Store.AddMessage(model.Message{FromParticipantID: "pt_peer", ToParticipantID: to, Text: "hello"})
	if !errors.Is(err, store.ErrMessageBackpressure) {
		t.Fatalf("满队列发送应返回 ErrMessageBackpressure，得到 %v", err)
	}
	rec := httptest.NewRecorder()
	s.writeBackpressure(rec, to, err)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("背压状态码 = %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error       string             `json:"error"`
		Code        string             `json:"code"`
		Recipient   string             `json:"recipient"`
		Queues      []store.QueueStats `json:"queues"`
		MaxMessages int                `json:"max_messages"`
		MaxBytes    int                `json:"max_bytes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "message_backpressure" || body.Recipient != to || body.Error == "" {
		t.Fatalf("背压响应体 = %+v", body)
	}
	if len(body.Queues) != 1 || body.Queues[0].Recipient != to || body.Queues[0].Pending != 1 {
		t.Fatalf("背压响应应带队列状态：%+v", body.Queues)
	}
	if body.MaxMessages != store.MaxQueuedMessages || body.MaxBytes != store.MaxQueuedBytes {
		t.Fatalf("背压响应应带队列上限：%+v", body)
	}
}
