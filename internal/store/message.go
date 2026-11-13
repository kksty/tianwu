package store

// Participant messages live in the same JSON snapshot as the ledger, but they
// are a separate table: the fact/intent/report paths never read Messages.
//
// Idempotency is enforced here, under the store lock, because the caller's
// retry key must resolve to one row even when two HTTP requests arrive
// together. Delivery state is also persisted here: "accepted" means IOA took
// the bytes, not that the model read them, and "unknown" means a timeout left
// the outcome genuinely unknown.

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/kksty/tianwu/internal/model"
)

var (
	// ErrIdempotencyConflict means the same sender reused a key for a different
	// message. Returning the first message would silently drop the second.
	ErrIdempotencyConflict = errors.New("同一个幂等键对应不同的消息内容")
	ErrMessageNotFound     = errors.New("消息不存在")
	// ErrMessageNotSender means the caller is not the sender of the message.
	ErrMessageNotSender = errors.New("只有发件人可以撤回这条消息")
	// ErrMessageNotQueued means the message already left the queue: withdrawing
	// it now would not unsend it, so the answer is a conflict, not a delete.
	ErrMessageNotQueued = errors.New("消息已经投递，不能撤回")
)

// RetractMessage removes a message while it is still queued and unclaimed by
// a delivery path. Only its sender may retract it. Delivery and receipt states
// stay in the conversation; withdrawal cannot unsend bytes already handed off.
func (s *Store) RetractMessage(id, sender string) (model.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.db.Messages {
		m := s.db.Messages[i]
		if m.ID != id {
			continue
		}
		if m.FromParticipantID != sender {
			return model.Message{}, fmt.Errorf("%w（发件人 %s）", ErrMessageNotSender, m.FromParticipantID)
		}
		if m.Status != model.MessageQueued {
			return model.Message{}, fmt.Errorf("%w（当前 %s）", ErrMessageNotQueued, m.Status)
		}
		s.db.Messages = append(s.db.Messages[:i], s.db.Messages[i+1:]...)
		if err := s.flushNoRev("message"); err != nil {
			return model.Message{}, err
		}
		return m, nil
	}
	return model.Message{}, fmt.Errorf("%w：%s", ErrMessageNotFound, id)
}

// SweepQueued applies the ordinary chat retention policy. Task results and
// explicit durable messages keep their delivery opportunity regardless of age
// or transient queue overflow. Delivered conversation is never swept.
//
// Returns how many rows were removed so the caller can log without a second scan.
func (s *Store) SweepQueued(now time.Time, ttl time.Duration, perRecipient int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if perRecipient <= 0 {
		perRecipient = 50
	}
	// Queued rows grouped by recipient, in slice order (AddMessage appends, so
	// this is Seq order: oldest first).
	queued := map[string][]int{}
	for i := range s.db.Messages {
		if s.db.Messages[i].Status == model.MessageQueued && !s.db.Messages[i].Durable {
			to := s.db.Messages[i].ToParticipantID
			queued[to] = append(queued[to], i)
		}
	}
	drop := map[int]bool{}
	// An ordinary queued message is judged by when it was sent; a message a
	// failed run put back on the queue is judged by when it was requeued.
	// Otherwise redelivery would silently delete the very message it exists to
	// save (a 24h-old message requeued now is deliverable now). UpdatedAt is
	// bumped by requeueMessage; it is not a second clock for normal rows.
	queueTime := func(m model.Message) time.Time {
		if m.Redeliveries > 0 {
			return m.UpdatedAt
		}
		return m.CreatedAt
	}
	for _, idxs := range queued {
		slices.SortStableFunc(idxs, func(a, b int) int {
			au, bu := queueTime(s.db.Messages[a]), queueTime(s.db.Messages[b])
			switch {
			case au.After(bu):
				return -1
			case au.Before(bu):
				return 1
			}
			// The wall clock can assign the same timestamp to a burst. The
			// durable message sequence still identifies the newest queue entry.
			if s.db.Messages[a].Seq > s.db.Messages[b].Seq {
				return -1
			}
			if s.db.Messages[a].Seq < s.db.Messages[b].Seq {
				return 1
			}
			return 0
		})
		for pos, i := range idxs {
			m := s.db.Messages[i]
			qt := queueTime(m)
			if ttl > 0 && !qt.IsZero() && now.Sub(qt) > ttl {
				drop[i] = true
				continue
			}
			// Keep the newest perRecipient rows; the rest are cap overflow.
			if pos >= perRecipient {
				drop[i] = true
			}
		}
	}
	if len(drop) == 0 {
		return 0, nil
	}
	kept := s.db.Messages[:0]
	for i, m := range s.db.Messages {
		if drop[i] {
			continue
		}
		kept = append(kept, m)
	}
	s.db.Messages = kept
	if err := s.flushNoRev("message"); err != nil {
		return 0, err
	}
	return len(drop), nil
}

// AddMessage stores one message and reports whether it was created. It is
// idempotent per (sender, key): a retry returns the original row and created=
// false, so the caller must not deliver it a second time.
func (s *Store) AddMessage(m model.Message) (model.Message, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m.Kind == model.MessageResult {
		m.Durable = true
	}
	if m.EventID != "" {
		for _, existing := range s.db.Messages {
			if existing.EventID == m.EventID && existing.FromParticipantID == m.FromParticipantID {
				return existing, false, nil
			}
		}
	}
	if m.IdempotencyKey != "" {
		for _, existing := range s.db.Messages {
			if existing.FromParticipantID != m.FromParticipantID || existing.IdempotencyKey != m.IdempotencyKey {
				continue
			}
			if sameMessageRequest(existing, m) {
				return existing, false, nil
			}
			return model.Message{}, false, fmt.Errorf("%w：%s", ErrIdempotencyConflict, m.IdempotencyKey)
		}
	}
	q := s.queueStatsLocked(m.ToParticipantID)
	if q.Pending >= MaxQueuedMessages || q.Bytes+len(m.Text) > MaxQueuedBytes {
		return model.Message{}, false, ErrMessageBackpressure
	}
	now := time.Now()
	m.ID = NewID("m")
	if m.EventID == "" {
		m.EventID = m.ID
	}
	if m.Kind == model.MessageResult && len(m.Chain) == 0 && m.FromRunID != "" {
		// A result carries the run it came from as its first chain hop, so a
		// duplicate delivery is identifiable even before the parent reads it.
		m.Chain = []string{m.FromRunID}
	}
	m.Seq = s.nextMessageSeqLocked()
	if m.Status == "" {
		m.Status = model.MessageQueued
	}
	m.CreatedAt, m.UpdatedAt = now, now
	s.db.Messages = append(s.db.Messages, m)
	if err := s.flushNoRev("message"); err != nil {
		return model.Message{}, false, err
	}
	return m, true, nil
}

// sameMessageRequest compares the caller-controlled fields. Delivery metadata
// and status are not part of it: a retry of the same text is the same request
// even after the first attempt was accepted or failed.
func sameMessageRequest(a, b model.Message) bool {
	return a.Durable == b.Durable && a.InstructionID == b.InstructionID && a.InstructionVersion == b.InstructionVersion && a.ToParticipantID == b.ToParticipantID &&
		a.ToGeneration == b.ToGeneration &&
		a.Kind == b.Kind &&
		a.Text == b.Text &&
		a.Interrupt == b.Interrupt &&
		a.InReplyTo == b.InReplyTo &&
		slices.Equal(a.FactRefs, b.FactRefs)
}

// nextMessageSeqLocked hands out the per-project read cursor. It is a stored
// counter, not max(row.Seq): deleting a message (withdraw, sweep) must not let
// the next one reuse a sequence a reader has already consumed, or that reader
// would never see it.
func (s *Store) nextMessageSeqLocked() uint64 {
	s.db.MessageSeq++
	return s.db.MessageSeq
}

// repairMessageSeqLocked lifts the counter above every surviving row. It runs at
// open, so a ledger written before the counter existed (schema 5 and older) or
// one whose counter fell behind still allocates monotonically.
func (s *Store) repairMessageSeqLocked() {
	for _, m := range s.db.Messages {
		if m.Seq > s.db.MessageSeq {
			s.db.MessageSeq = m.Seq
		}
	}
}

// UpdateMessage mutates one row and persists it without waking the decider.
func (s *Store) UpdateMessage(id string, fn func(*model.Message)) (model.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.db.Messages {
		if s.db.Messages[i].ID != id {
			continue
		}
		fn(&s.db.Messages[i])
		s.db.Messages[i].UpdatedAt = time.Now()
		if err := s.flushNoRev("message"); err != nil {
			return model.Message{}, err
		}
		return s.db.Messages[i], nil
	}
	return model.Message{}, fmt.Errorf("%w：%s", ErrMessageNotFound, id)
}

func (s *Store) MessageByID(id string) (model.Message, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, m := range s.db.Messages {
		if m.ID == id {
			return m, true
		}
	}
	return model.Message{}, false
}

// MessagesFor returns the conversation visible to one participant: what it
// sent and what was addressed to it, in project order. after is the last Seq the
// caller has seen, so a reader never has to replay the whole history.
func (s *Store) MessagesFor(participant string, after uint64, limit int) []model.Message {
	return s.MessagesForBox(participant, after, limit, "")
}

func (s *Store) MessagesForBox(participant string, after uint64, limit int, box string) []model.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	out := make([]model.Message, 0, limit)
	for _, m := range s.db.Messages {
		if m.Seq <= after {
			continue
		}
		if m.FromParticipantID != participant && m.ToParticipantID != participant {
			continue
		}
		if box == "sent" && m.FromParticipantID != participant || box == "inbox" && m.ToParticipantID != participant {
			continue
		}
		out = append(out, m)
	}
	slices.SortFunc(out, func(a, b model.Message) int {
		switch {
		case a.Seq < b.Seq:
			return -1
		case a.Seq > b.Seq:
			return 1
		}
		return 0
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// BeginMessageDelivery claims the queued row durably before any bytes leave.
// Retract and sweep cannot remove it while the transport is in flight.
func (s *Store) BeginMessageDelivery(id string) (model.Message, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.db.Messages {
		m := &s.db.Messages[i]
		if m.ID != id {
			continue
		}
		if m.Status != model.MessageQueued {
			return *m, false, nil
		}
		if m.EventID == "" {
			// Old row: pin the stable identity now, before any bytes leave.
			m.EventID = m.ID
		}

		m.Status, m.UpdatedAt = model.MessageSending, time.Now()
		if err := s.flushNoRev("message"); err != nil {
			return model.Message{}, false, err
		}
		return *m, true, nil
	}
	return model.Message{}, false, ErrMessageNotFound
}

// FinishMessageDelivery records transport metadata without undoing a recipient
// receipt that may arrive before the transport call itself returns.
func (s *Store) FinishMessageDelivery(id, deliveryID, runID, path, errText string) (model.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.db.Messages {
		m := &s.db.Messages[i]
		if m.ID != id {
			continue
		}
		if m.Status != model.MessageSending && m.Status != model.MessageConfirmed {
			return *m, nil
		}
		m.DeliveryID, m.DeliveredRunID, m.DeliveryPath, m.Error = deliveryID, runID, path, errText
		if m.AckedAt != nil || m.Status == model.MessageConfirmed {
			m.Status = model.MessageConfirmed
		} else if errText != "" {
			m.Status = model.MessageUnknown
		} else {
			m.Status = model.MessageAccepted
		}
		m.UpdatedAt = time.Now()
		if err := s.flushNoRev("message"); err != nil {
			return model.Message{}, err
		}
		return *m, nil
	}
	return model.Message{}, ErrMessageNotFound
}

// RecoverMessageDeliveries cannot tell whether the interrupted transport sent
// its bytes. Mark it unknown; automatically requeueing could duplicate work.
func (s *Store) RecoverMessageDeliveries() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for i := range s.db.Messages {
		m := &s.db.Messages[i]
		if m.Status != model.MessageSending {
			continue
		}
		m.Status, m.Error, m.UpdatedAt = model.MessageUnknown, "进程重启：投递结果未知", time.Now()
		changed = true
	}
	if changed {
		return s.flushNoRev("message")
	}
	return nil
}

// Messages is the console view: every message in the project, in order.
func (s *Store) Messages(limit int) []model.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	out := append([]model.Message(nil), s.db.Messages...)
	slices.SortFunc(out, func(a, b model.Message) int {
		switch {
		case a.Seq < b.Seq:
			return -1
		case a.Seq > b.Seq:
			return 1
		}
		return 0
	})
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

// PendingFor is the queue a participant has not had delivered yet. It is what
// the next run can read when it starts, instead of a message silently vanishing
// because its target was between turns.
func (s *Store) PendingFor(participant string) []model.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []model.Message
	for _, m := range s.db.Messages {
		if m.ToParticipantID == participant && m.Status == model.MessageQueued {
			out = append(out, m)
		}
	}
	return out
}

// ---- redelivery after a failed run (retry with memory) ----
//
// The control plane has no end-to-end read receipt (D87), so a message that was
// accepted just before a failure may or may not have reached the model. Two
// structured moments stand in for a receipt:
//
//   - an explicit ack sets Status=confirmed — never redeliver those;
//   - the run's last turn_started — a message delivered before it could have been
//     drained by that turn, one delivered after it could not have been read yet.
//
// Redelivery is at-least-once and visible: Redeliveries>0 makes the injected
// text carry a [重投] marker with the message id, so a model that already saw
// the text can recognise the duplicate instead of acting twice.

// RedeliverPossiblyUnread puts one failed run's possibly-unread messages back on
// the queue. Only accepted, unconfirmed messages delivered to exactly that run
// are touched; confirmed means the recipient acknowledged it, queued/unknown
// are not safe to replay. since is the run's LastTurnStartedAt (zero when the run
// never started a turn, in which case nothing it carried could have been read).
func (s *Store) RedeliverPossiblyUnread(participant, runID string, since time.Time) (int, error) {
	if runID == "" {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	n := 0
	for i := range s.db.Messages {
		m := &s.db.Messages[i]
		if m.ToParticipantID != participant || m.Status != model.MessageAccepted || m.AckedAt != nil {
			continue
		}
		if m.DeliveredRunID != runID {
			continue
		}
		// Accepted rows are last updated when transport accepts them. Creation
		// may precede delivery by hours, especially for queued or requeued rows.
		if !since.IsZero() && m.UpdatedAt.Before(since) {
			continue
		}
		requeueMessage(m, now)
		n++
	}
	if n == 0 {
		return 0, nil
	}
	return n, s.flushNoRev("message")
}

// RedeliverAllUnconfirmed puts every accepted, unconfirmed message addressed to
// participant back on the queue. A retry that starts a fresh session has no
// conversation memory at all, and DeliveredRunID is too narrow there: a message
// may have been carried by a run two sessions ago (A delivered, resume B
// failed, fresh session C). Duplicates are tagged, loss is not.
func (s *Store) RedeliverAllUnconfirmed(participant string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	n := 0
	for i := range s.db.Messages {
		m := &s.db.Messages[i]
		if m.ToParticipantID != participant || m.Status != model.MessageAccepted || m.AckedAt != nil {
			continue
		}
		requeueMessage(m, now)
		n++
	}
	if n == 0 {
		return 0, nil
	}
	return n, s.flushNoRev("message")
}

func requeueMessage(m *model.Message, now time.Time) {
	m.Status = model.MessageQueued
	m.Redeliveries++
	// The next holder is a new generation; keeping the old target would make a
	// sender's stale addressing look current.
	m.ToGeneration = 0
	m.UpdatedAt = now
}

// RedeliveryCounts is what the retry dialog needs: Resume is how many queued
// rows a resumed turn will inject with a [重投] marker; Fresh is how many rows a
// fresh session must carry that a resume would not have to (the queued
// redeliveries plus every accepted-but-unconfirmed row). The two are different
// sets, not one number twice.
type RedeliveryCounts struct {
	Resume int `json:"resume_redeliver"`
	Fresh  int `json:"new_session_redeliver"`
}

func (s *Store) RedeliveryCounts(participant string) RedeliveryCounts {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out RedeliveryCounts
	for _, m := range s.db.Messages {
		if m.ToParticipantID != participant {
			continue
		}
		switch {
		case m.Status == model.MessageQueued && m.Redeliveries > 0:
			out.Resume++
			// A fresh session also re-injects the queue, so this row counts
			// there too; only the still-accepted rows are new to it.
			out.Fresh++
		case m.Status == model.MessageAccepted && m.AckedAt == nil:
			out.Fresh++
		}
	}
	return out
}
