package api

// Delivery side of the association router. The store decides WHICH recorded fact
// changes are relevant to which participant and persists the notices together
// with their checkpoints (internal/store/routing.go); this file moves
// already-durable notices into live sessions, reports queue pressure, and turns a
// capacity refusal into an explicit answer. It never wakes a model by itself and
// never starts a run: a queued notice is read by an already-running session or by
// the recipient's next prompt, and an ACK is just a receipt.

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/store"
)

const (
	// noticeDeliveryBatch bounds how many notices one delivery pass actually
	// hands to live sessions. Rows for directions between runs are skipped
	// cheaply and do not consume the batch, so an old backlog for an idle
	// direction cannot starve a running one. Whatever stays queued is durable:
	// the next pass or the recipient's next prompt picks it up.
	noticeDeliveryBatch = 16
	noticeDeliveryWait  = 2 * time.Second
)

// registerCommunicationRoutes is the single registration point for the
// communication surface. The project mux and the participant mux both mount it;
// the handler narrows a participant to its own queue.
func (s *Server) registerCommunicationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/communication", s.communicationStatus)
	// A request -> acceptance -> result thread. The message ledger stores the
	// edges (in_reply_to, kind=result, fact_refs); this endpoint is the one read
	// that joins them, so "who accepted, what was quoted, which result closed
	// it" does not have to be guessed from prose or arrival order (item 9).
	mux.HandleFunc("GET /api/messages/{id}/thread", s.messageThread)
}

// messageThread returns the closed-loop correlation of one message as a request.
// A participant may only read a thread it participates in; anything else is a
// 404 so the endpoint cannot be used to enumerate other directions'
// conversations. Delivery, read (ack) and execution are reported separately:
// accepted != read, ACK != executed, and an unknown result is never replayed by
// a read.
func (s *Server) messageThread(w http.ResponseWriter, r *http.Request) {
	max := store.MessageThreadMax
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 || n > store.MessageThreadMax {
			writeErr(w, http.StatusBadRequest, "limit 必须在 1–"+strconv.Itoa(store.MessageThreadMax)+" 之间")
			return
		}
		max = n
	}
	thread, ok := s.Store.MessageThread(r.PathValue("id"), max)
	if !ok {
		writeErr(w, http.StatusNotFound, "消息不存在")
		return
	}
	if ident, agent := identityOf(r); agent && !threadIncludesParticipant(thread, ident.ParticipantID) {
		writeErr(w, http.StatusNotFound, "消息不存在")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"thread": thread,
		"note":   "accepted 只表示传输接受，read=acked 只是收件回执，都不证明外部动作已执行；执行证据按 result.fact_refs 到事实账本读。未知投递不自动重发。",
	})
}

// threadIncludesParticipant reports whether a participant is one of the
// conversation's own parties (root or an associated reply/result).
func threadIncludesParticipant(thread store.MessageCorrelation, participant string) bool {
	if participant == "" {
		return false
	}
	if thread.Request.Message.FromParticipantID == participant || thread.Request.Message.ToParticipantID == participant {
		return true
	}
	for _, entries := range [][]store.ThreadMessage{thread.Replies, thread.Results} {
		for _, entry := range entries {
			if entry.Message.FromParticipantID == participant || entry.Message.ToParticipantID == participant {
				return true
			}
		}
	}
	return false
}

// communicationStatus reports per-recipient queue pressure, the routing
// checkpoint and the declared bounds. A participant sees only its own queue: the
// backlog of other directions is not its business. The operator (and the
// commander, which shares the project credential) may ask for one recipient or
// see them all.
func (s *Server) communicationStatus(w http.ResponseWriter, r *http.Request) {
	want := strings.TrimSpace(r.URL.Query().Get("recipient"))
	if id, ok := identityOf(r); ok {
		if want != "" && want != id.ParticipantID {
			writeErr(w, http.StatusForbidden, "参与者只能查看自己的通信队列")
			return
		}
		want = id.ParticipantID
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"queues":       s.Store.QueueStatus(want),
		"max_messages": store.MaxQueuedMessages,
		"max_bytes":    store.MaxQueuedBytes,
		"paused":       s.paused.Load(),
	})
}

// writeBackpressure answers a capacity refusal with 429 and the machine code the
// console and a peer retry can act on. The refused bytes were not queued; the
// caller's idempotency key makes retrying the same send safe.
func (s *Server) writeBackpressure(w http.ResponseWriter, to string, err error) {
	if err == nil {
		err = store.ErrMessageBackpressure
	}
	writeJSON(w, http.StatusTooManyRequests, map[string]any{
		"error":        err.Error(),
		"code":         "message_backpressure",
		"recipient":    to,
		"queues":       s.Store.QueueStatus(to),
		"max_messages": store.MaxQueuedMessages,
		"max_bytes":    store.MaxQueuedBytes,
	})
}

// deliverPendingNotices moves durable fact notices into sessions that are already
// running. A notice whose target is between runs stays queued for its next
// prompt: the queued row is the durable record, and starting a turn here would
// let the router create model work the ledger did not schedule.
func (s *Server) deliverPendingNotices() {
	// A paused or closing project keeps persisting notices (the routing pass ran
	// before this call) but does not deliver into sessions.
	if s.paused.Load() || s.closing.Load() || s.deleting.Load() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), noticeDeliveryWait)
	defer cancel()
	delivered := 0
	for _, msg := range s.Store.PendingCoordinationNotices() {
		if delivered >= noticeDeliveryBatch || ctx.Err() != nil {
			return
		}
		intentID, ok := collab.IntentIDFromParticipant(msg.ToParticipantID)
		if !ok {
			if msg.ToParticipantID == collab.CommanderID {
				s.deliverToCommander(ctx, msg)
				delivered++
			}
			// The router only addresses intent participants. A row for any
			// other identity is not ours to push into a model session.
			continue
		}
		target, found := s.Store.GetIntent(intentID)
		if !found {
			continue
		}
		before := msg.Status
		after := s.deliverMessage(ctx, target, msg)
		if after.Status != before {
			delivered++
		}
	}
}

// routeAndDeliver is one routing pass plus delivery. It is called after a fact
// commit (so a run's own declarations do not wait for a ticker) and on watch
// startup/tick as restart recovery: the checkpoint, not the call site, makes it
// idempotent.
//
// Routing persistence errors are logged but do not stop delivery of notices that
// an earlier pass already committed; the unfiled change survives in the journal
// and is retried by the next pass.
func (s *Server) routeAndDeliver() {
	if err := s.Store.RouteTaskResults(); err != nil {
		log.Printf("任务结果路由失败：%v", err)
	}
	if err := s.Store.RouteFactChanges(64); err != nil {
		log.Printf("关联通知入队失败：%v", err)
	}
	s.deliverPendingNotices()
	s.wakeCommanderIfNeeded("ledger")
}
