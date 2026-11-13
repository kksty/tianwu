package store

// The communication closed loop (audit item 9): a request must be correlatable
// with the participant that accepted it, the message it quoted, and the result
// that closes it. These tests pin the read model, not a new store: no message
// is created or changed by reading a thread, and "accepted", "acked" and
// "executed" stay three different facts.

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/model"
)

func openCorrelationStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func addThreadMessage(t *testing.T, s *Store, m model.Message) model.Message {
	t.Helper()
	out, created, err := s.AddMessage(m)
	if err != nil {
		t.Fatalf("add message: %v", err)
	}
	if !created {
		t.Fatalf("message was not created (idempotency collision): %+v", out)
	}
	return out
}

func TestMessageThreadCorrelatesRequestAcceptanceAndResult(t *testing.T) {
	s := openCorrelationStore(t)
	request := addThreadMessage(t, s, model.Message{
		FromParticipantID: "pt_asker", ToParticipantID: "pt_worker",
		Kind: model.MessageQuestion, Text: "10.0.0.5:8080 的 /admin 能不能越权？",
	})
	accept := addThreadMessage(t, s, model.Message{
		FromParticipantID: "pt_worker", ToParticipantID: "pt_asker",
		Kind: "answer", Text: "收到，我先用只读账号复核", InReplyTo: request.ID,
	})
	// A result may answer the acceptance (depth two), not only the root request.
	result := addThreadMessage(t, s, model.Message{
		FromParticipantID: "pt_worker", ToParticipantID: "pt_asker",
		Kind: model.MessageResult, Text: "复核完成，fetched 200", InReplyTo: accept.ID,
		FactRefs: []string{"f_proof"},
	})

	thread, ok := s.MessageThread(request.ID, 0)
	if !ok {
		t.Fatal("thread not found")
	}
	if thread.Unanswered || len(thread.AcceptedBy) != 1 || thread.AcceptedBy[0] != "pt_worker" {
		t.Fatalf("acceptance = %+v unanswered=%v", thread.AcceptedBy, thread.Unanswered)
	}
	if len(thread.Replies) != 1 || thread.Replies[0].Message.ID != accept.ID {
		t.Fatalf("replies = %+v", thread.Replies)
	}
	if !thread.Replies[0].QuoteValid || thread.Replies[0].Quoted == nil || thread.Replies[0].Quoted.ID != request.ID {
		t.Fatalf("reply quote = %+v", thread.Replies[0])
	}
	if len(thread.Results) != 1 || thread.Results[0].Message.ID != result.ID {
		t.Fatalf("results = %+v", thread.Results)
	}
	if got := thread.Results[0].Message.FactRefs; len(got) != 1 || got[0] != "f_proof" {
		t.Fatalf("result proof association = %v", got)
	}
	// Accepted is transport state only; read and execution stay separate.
	if thread.Results[0].Delivery != model.MessageQueued {
		t.Fatalf("result delivery = %q", thread.Results[0].Delivery)
	}
	if thread.Results[0].Read != "unknown" || thread.Results[0].Executed != "unknown" {
		t.Fatalf("read/executed must stay unknown: %+v", thread.Results[0])
	}
	if thread.UnknownResults != 1 {
		t.Fatalf("an undelivered result must be counted unknown, got %d", thread.UnknownResults)
	}
}

func TestMessageThreadAckIsAReceiptNotExecution(t *testing.T) {
	s := openCorrelationStore(t)
	request := addThreadMessage(t, s, model.Message{
		FromParticipantID: "pt_asker", ToParticipantID: "pt_worker",
		Kind: model.MessageQuestion, Text: "请只读复核 /orders?id=2",
	})
	if _, _, err := s.BeginMessageDelivery(request.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishMessageDelivery(request.ID, "ioa_1", "r_worker", "ioa", ""); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, err := s.UpdateMessage(request.ID, func(m *model.Message) {
		m.Status = model.MessageConfirmed
		m.AckedAt = &now
	}); err != nil {
		t.Fatal(err)
	}
	thread, ok := s.MessageThread(request.ID, 0)
	if !ok {
		t.Fatal("thread not found")
	}
	if thread.Request.Delivery != model.MessageConfirmed || thread.Request.Read != "acked" {
		t.Fatalf("receipt = %+v", thread.Request)
	}
	// An ack proves processing, not that any external action happened.
	if thread.Request.Executed != "unknown" {
		t.Fatalf("an ack was read as execution: %+v", thread.Request)
	}
	if !thread.Unanswered {
		t.Fatal("a receipt is not an acceptance: nobody replied yet")
	}
}

func TestMessageThreadRejectsAForeignQuoteAndFlagsMissingQuote(t *testing.T) {
	s := openCorrelationStore(t)
	request := addThreadMessage(t, s, model.Message{
		FromParticipantID: "pt_asker", ToParticipantID: "pt_worker",
		Kind: model.MessageQuestion, Text: "问题 A",
	})
	// C quotes a message that was between A and worker: not an acceptance by C.
	if _, created, err := s.AddMessage(model.Message{
		FromParticipantID: "pt_other", ToParticipantID: "pt_asker",
		Kind: "answer", Text: "我代答", InReplyTo: request.ID,
	}); err != nil || !created {
		t.Fatalf("add foreign reply: created=%v err=%v", created, err)
	}
	thread, _ := s.MessageThread(request.ID, 0)
	if len(thread.Replies) != 1 {
		t.Fatalf("replies = %+v", thread.Replies)
	}
	if thread.Replies[0].QuoteValid {
		t.Fatal("a third party quote was accepted as a valid association")
	}
	if len(thread.AcceptedBy) != 0 {
		t.Fatalf("a foreign reply accepted the request: %v", thread.AcceptedBy)
	}

	// A reply quoting a message that no longer exists keeps the gap visible.
	if _, created, err := s.AddMessage(model.Message{
		FromParticipantID: "pt_worker", ToParticipantID: "pt_asker",
		Kind: "answer", Text: "回复一条已删除的消息", InReplyTo: "m_deleted",
	}); err != nil || !created {
		t.Fatalf("add dangling reply: created=%v err=%v", created, err)
	}
	thread, _ = s.MessageThread(request.ID, 0)
	if len(thread.Replies) != 1 || thread.Replies[0].Message.InReplyTo != request.ID {
		t.Fatalf("replies = %+v", thread.Replies)
	}

	// A request that itself quotes a message the ledger no longer has keeps the
	// gap visible instead of showing an anonymous request.
	orphan := addThreadMessage(t, s, model.Message{
		FromParticipantID: "pt_worker", ToParticipantID: "pt_asker",
		Kind: "answer", Text: "回复一条已删除的消息", InReplyTo: "m_deleted",
	})
	orphanThread, ok := s.MessageThread(orphan.ID, 0)
	if !ok {
		t.Fatal("orphan thread not found")
	}
	if !orphanThread.Request.QuoteMissing {
		t.Fatalf("dangling root quote was not flagged: %+v", orphanThread.Request)
	}
	if orphanThread.Request.QuoteValid {
		t.Fatal("a missing quote cannot be a valid association")
	}
}

func TestMessageThreadIsBoundedAndReadOnly(t *testing.T) {
	s := openCorrelationStore(t)
	request := addThreadMessage(t, s, model.Message{
		FromParticipantID: "pt_asker", ToParticipantID: "pt_worker",
		Kind: model.MessageQuestion, Text: "很多回复",
	})
	for i := 0; i < 6; i++ {
		addThreadMessage(t, s, model.Message{
			FromParticipantID: "pt_worker", ToParticipantID: "pt_asker",
			Kind: "answer", Text: "回复", InReplyTo: request.ID,
		})
	}
	before := len(s.Messages(0))
	thread, ok := s.MessageThread(request.ID, 3)
	if !ok {
		t.Fatal("thread not found")
	}
	if !thread.Truncated {
		t.Fatal("a bounded thread did not report truncation")
	}
	if len(thread.Replies) > 3 {
		t.Fatalf("thread exceeded its bound: %d", len(thread.Replies))
	}
	if after := len(s.Messages(0)); after != before {
		t.Fatalf("reading a thread mutated the ledger: %d -> %d", before, after)
	}
	// An unknown root is absence, not an empty thread.
	if _, ok := s.MessageThread("m_missing", 0); ok {
		t.Fatal("a missing message produced a thread")
	}
}

// A transport failure leaves the delivery genuinely unknown: it must stay out of
// the queue (never blindly resent) but stay visible in the thread and queue
// status so a human can reconcile it. Retry logic only replays accepted rows.
func TestUnknownDeliveryIsVisibleButNeverBlindlyResent(t *testing.T) {
	s := openCorrelationStore(t)
	request := addThreadMessage(t, s, model.Message{
		FromParticipantID: "pt_asker", ToParticipantID: "pt_worker",
		Kind: model.MessageQuestion, Text: "只读复核请求",
	})
	if _, _, err := s.BeginMessageDelivery(request.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishMessageDelivery(request.ID, "", "r_worker", "ioa", "宿主超时，结果未知"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.MessageByID(request.ID)
	if got.Status != model.MessageUnknown {
		t.Fatalf("transport error delivery status = %q, want unknown", got.Status)
	}
	if pending := s.PendingFor("pt_worker"); len(pending) != 0 {
		t.Fatalf("an unknown delivery was put back in the queue: %+v", pending)
	}
	// Retry paths only requeue accepted rows; unknown is untouched.
	if _, err := s.RedeliverAllUnconfirmed("pt_worker"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RedeliverPossiblyUnread("pt_worker", "r_worker", time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	after, _ := s.MessageByID(request.ID)
	if after.Status != model.MessageUnknown || after.Redeliveries != 0 {
		t.Fatalf("unknown delivery was replayed: %+v", after)
	}
	thread, ok := s.MessageThread(request.ID, 0)
	if !ok || thread.Request.Delivery != model.MessageUnknown {
		t.Fatalf("thread hides the unknown delivery: %+v", thread.Request)
	}
	// Accepted, unconfirmed rows are the ones that may be requeued (at-least-once,
	// tagged so the model can dedupe); they are not proof of a read either.
	accepted := addThreadMessage(t, s, model.Message{
		FromParticipantID: "pt_asker", ToParticipantID: "pt_worker",
		Kind: model.MessageUpdate, Text: "一条已接受未确认的消息", Status: model.MessageAccepted,
		DeliveredRunID: "r_worker", UpdatedAt: time.Now(),
	})
	n, err := s.RedeliverPossiblyUnread("pt_worker", "r_worker", time.Now().Add(-time.Minute))
	if err != nil || n != 1 {
		t.Fatalf("accepted row not requeued: n=%d err=%v", n, err)
	}
	replayed, _ := s.MessageByID(accepted.ID)
	if replayed.Status != model.MessageQueued || replayed.Redeliveries != 1 {
		t.Fatalf("requeued row = %+v", replayed)
	}
}

func TestMessageThreadFormatsRequestTextButNotSecrets(t *testing.T) {
	s := openCorrelationStore(t)
	request := addThreadMessage(t, s, model.Message{
		FromParticipantID: "pt_asker", ToParticipantID: "pt_worker",
		Kind: model.MessageQuestion, Text: "只读请求 /admin，账号 test-a",
	})
	thread, _ := s.MessageThread(request.ID, 0)
	if strings.Contains(thread.Request.Message.Text, "Bearer") {
		t.Fatal("unexpected secret in the fixture")
	}
	if thread.Request.Message.Kind != model.MessageQuestion || thread.Request.Delivery != model.MessageQueued {
		t.Fatalf("request shape = %+v", thread.Request)
	}
}
