package store

// Message correlation: a request, who accepted it, and the result that closes
// it. The message ledger already stores the raw edges (in_reply_to, kind=result,
// fact_refs); this read model is the one place that joins them, so the console
// and a model read the same association instead of guessing it from prose or
// from wall-clock order (audit item 9).
//
// It is deliberately read-only and derived: no row is added, no status changes,
// and no message becomes a fact. The delivery/read/execution triple is kept
// separate because they are different facts:
//
//   - delivery: queued / sending / accepted / confirmed / unknown (transport
//     state, see model.Message.Status);
//   - read: only an explicit recipient ack proves the message was processed,
//     and even that is a receipt, not comprehension;
//   - executed: the message ledger cannot prove an external action happened.
//     Evidence for work belongs in the fact ledger, so this stays "unknown"
//     unless the result message cites fact ids.
//
// A reply is accepted only when the quoted message is one the replier actually
// received; a reply that quotes a message between other participants is marked
// invalid rather than silently counted as an acceptance.

import (
	"slices"

	"github.com/kksty/tianwu/internal/model"
)

// MessageThreadMax bounds one correlation read. A request thread is a
// conversation, not a data export; callers that need more read the message list
// with its cursor.
const MessageThreadMax = 200

// MessageCorrelation is the closed-loop view of one message as a request.
type MessageCorrelation struct {
	// Request is the message the thread is rooted at, with its delivery/read/
	// execution triple.
	Request ThreadMessage `json:"request"`
	// AcceptedBy lists the distinct participants who replied to the request
	// (arrival of a reply is an acceptance; it is not proof of execution).
	AcceptedBy []string `json:"accepted_by,omitempty"`
	// Replies are non-result replies, in project order. Their own quoted
	// results are attached as Results.
	Replies []ThreadMessage `json:"replies,omitempty"`
	// Results are result-kind messages that quote the request or one of its
	// replies (depth two). FactRefs on a result are the ledger association the
	// receiving side can follow; an empty list means "no cited proof".
	Results []ThreadMessage `json:"results,omitempty"`
	// UnknownResults counts results whose delivery outcome is unknown: they may
	// or may not have reached the peer and must not be resent automatically.
	UnknownResults int `json:"unknown_results,omitempty"`
	// Unanswered reports that no reply/result quotes the request yet (the
	// request is still unaccepted, whatever its own delivery state).
	Unanswered bool `json:"unanswered"`
	// Truncated reports that the thread exceeded MessageThreadMax.
	Truncated bool `json:"truncated,omitempty"`
}

// ThreadMessage is one message plus the association facts a reader needs.
type ThreadMessage struct {
	Message model.Message `json:"message"`
	// Quoted is the message this one quotes, when the quote still exists.
	Quoted *model.Message `json:"quoted,omitempty"`
	// QuoteValid is true when the quoted message is between this message's
	// sender and recipient (the request direction). A quote of a third party's
	// message is not an association.
	QuoteValid bool `json:"quote_valid,omitempty"`
	// QuoteMissing is true when in_reply_to names a message that is not in the
	// ledger (deleted, or a wrong id): the quoted request cannot be shown.
	QuoteMissing bool `json:"quote_missing,omitempty"`
	// Delivery is the transport state: accepted != read.
	Delivery string `json:"delivery"`
	// Read is "acked" only when the recipient explicitly acknowledged the
	// message; otherwise "unknown". An ack is a receipt, not comprehension.
	Read string `json:"read"`
	// Executed is always "unknown" here: a message never proves an external
	// action. Work is proved by the fact ledger the result cites.
	Executed string `json:"executed"`
}

const (
	readAcked   = "acked"
	readUnknown = "unknown"
	execUnknown = "unknown"
)

// MessageThread builds the correlation view for one request id. ok=false means
// the root message does not exist.
func (s *Store) MessageThread(rootID string, max int) (MessageCorrelation, bool) {
	if max <= 0 || max > MessageThreadMax {
		max = MessageThreadMax
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	byID := make(map[string]model.Message, len(s.db.Messages))
	children := make(map[string][]model.Message, len(s.db.Messages))
	var root model.Message
	found := false
	for _, m := range s.db.Messages {
		byID[m.ID] = m
		if m.ID == rootID {
			root = m
			found = true
		}
		if m.InReplyTo != "" {
			children[m.InReplyTo] = append(children[m.InReplyTo], m)
		}
	}
	if !found {
		return MessageCorrelation{}, false
	}
	total := 0
	take := func(msgs []model.Message) []model.Message {
		out := make([]model.Message, 0, len(msgs))
		for _, m := range msgs {
			if total >= max {
				return out
			}
			out = append(out, m)
			total++
		}
		return out
	}
	thread := MessageCorrelation{Request: threadMessageLocked(root, byID)}
	direct := children[rootID]
	slices.SortStableFunc(direct, compareMessageOrder)
	accepted := map[string]bool{}
	var replies []model.Message
	var results []model.Message
	for _, m := range take(direct) {
		entry := threadMessageLocked(m, byID)
		if entry.QuoteMissing {
			// A reply that quotes a deleted message cannot be associated.
			continue
		}
		if m.Kind == model.MessageResult {
			results = append(results, m)
			continue
		}
		replies = append(replies, m)
		if entry.QuoteValid && m.FromParticipantID == root.ToParticipantID {
			accepted[m.FromParticipantID] = true
		}
	}
	// Results may answer a reply (depth two): a peer asks, the worker accepts,
	// then reports the result under that acceptance.
	for _, reply := range replies {
		nested := children[reply.ID]
		slices.SortStableFunc(nested, compareMessageOrder)
		for _, m := range take(nested) {
			if m.Kind != model.MessageResult {
				continue
			}
			results = append(results, m)
		}
	}
	slices.SortStableFunc(results, compareMessageOrder)
	for _, m := range replies {
		thread.Replies = append(thread.Replies, threadMessageLocked(m, byID))
	}
	for _, m := range results {
		entry := threadMessageLocked(m, byID)
		thread.Results = append(thread.Results, entry)
		// A result whose delivery never completed (queued, or transport outcome
		// unknown) may or may not have reached the requesting peer; counting it
		// keeps that uncertainty visible instead of reading it as delivered.
		if m.Status == model.MessageUnknown || (m.Status == model.MessageQueued && m.DeliveredRunID == "") {
			thread.UnknownResults++
		}
	}
	for participant := range accepted {
		thread.AcceptedBy = append(thread.AcceptedBy, participant)
	}
	slices.Sort(thread.AcceptedBy)
	thread.Unanswered = len(thread.Replies) == 0 && len(thread.Results) == 0
	thread.Truncated = total >= max
	return thread, true
}

// threadMessageLocked derives one entry. Caller holds s.mu (read or write).
func threadMessageLocked(m model.Message, byID map[string]model.Message) ThreadMessage {
	entry := ThreadMessage{
		Message:  m,
		Delivery: m.Status,
		Read:     readUnknown,
		Executed: execUnknown,
	}
	if m.AckedAt != nil || m.Status == model.MessageConfirmed {
		entry.Read = readAcked
	}
	if m.InReplyTo == "" {
		return entry
	}
	quoted, ok := byID[m.InReplyTo]
	if !ok {
		entry.QuoteMissing = true
		return entry
	}
	entry.Quoted = &quoted
	entry.QuoteValid = quoted.FromParticipantID == m.ToParticipantID &&
		quoted.ToParticipantID == m.FromParticipantID
	return entry
}

// compareMessageOrder is the project order: durable Seq, then id as a tiebreak.
func compareMessageOrder(a, b model.Message) int {
	switch {
	case a.Seq < b.Seq:
		return -1
	case a.Seq > b.Seq:
		return 1
	}
	switch {
	case a.ID < b.ID:
		return -1
	case a.ID > b.ID:
		return 1
	}
	return 0
}
