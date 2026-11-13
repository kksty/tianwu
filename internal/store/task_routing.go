package store

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/model"
)

// RouteTaskResults recovers local settlement notifications from the SAME
// transaction journal as the task state. A crash between finish and its callback
// cannot lose the result. Queue admission and its cursor commit together.
func (s *Store) RouteTaskResults() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db.Memory == nil {
		return nil
	}
	mem := *s.db.Memory
	start := sort.Search(len(mem.History), func(i int) bool { return mem.History[i].Seq > mem.TaskRouteThrough })
	end := min(start+512, len(mem.History))
	retried := min(512, len(mem.TaskRoutePending))
	changes := make([]Change, 0, retried+end-start)
	for _, seq := range mem.TaskRoutePending[:retried] {
		i := sort.Search(len(mem.History), func(i int) bool { return mem.History[i].Seq >= seq })
		if i == len(mem.History) || mem.History[i].Seq != seq {
			return fmt.Errorf("missing deferred task routing event %d", seq)
		}
		changes = append(changes, mem.History[i])
	}
	changes = append(changes, mem.History[start:end]...)
	// Decode before mutating queues: a corrupt event must not leave a partial
	// in-memory pass that a later write could accidentally commit.
	decoded := make(map[uint64]model.Intent, len(changes))
	for _, c := range changes {
		if c.Kind == "intent" {
			var at model.Intent
			if err := json.Unmarshal(c.Value, &at); err != nil {
				return fmt.Errorf("decode task routing event %d: %w", c.Seq, err)
			}
			decoded[c.Seq] = at
		}
	}
	mem.TaskRoutePending = append([]uint64(nil), mem.TaskRoutePending[retried:]...)
	intents := make(map[string]model.Intent, len(s.db.Intents))
	for _, in := range s.db.Intents {
		intents[in.ID] = in
	}
	changed := false
	// A queued result whose owner went away is offered to the next coordinator
	// or to the commander. In-flight/unknown/accepted deliveries are never moved.
	for i := range s.db.Messages {
		m := &s.db.Messages[i]
		if m.Kind != model.NoticeKindTask || m.FromParticipantID != model.NoticeSenderID || m.Status != model.MessageQueued || m.ToParticipantID == collab.CommanderID {
			continue
		}
		ownerID, ok := collab.IntentIDFromParticipant(m.ToParticipantID)
		owner, exists := intents[ownerID]
		if ok && exists && !model.Settled(owner.Status) && !owner.Hold && !owner.Aborted {
			continue
		}
		next := coordinatorLocked(intents[m.NoticeIntentID], intents)
		m.ToParticipantID = collab.CommanderID
		if next != "" {
			m.ToParticipantID = collab.ParticipantID(next)
		}
		m.ToGeneration = 0
		changed = true
	}
	for _, c := range changes {
		if c.Kind == "intent" {
			at := decoded[c.Seq]
			cur, ok := intents[c.ID]
			if ok && model.Settled(at.Status) && cur.RunID == at.RunID && cur.Status == at.Status {
				ownerID := coordinatorLocked(cur, intents)
				if ownerID != "" && !waitingForChild(intents[ownerID], cur.ID) {
					key := "task-result:" + cur.ID + ":" + cur.RunID + ":" + cur.Status
					found := false
					for _, m := range s.db.Messages {
						if m.FromParticipantID == model.NoticeSenderID && m.IdempotencyKey == key {
							found = true
							break
						}
					}
					if !found {
						to := collab.ParticipantID(ownerID)
						q := s.queueStatsLocked(to)
						text := fmt.Sprintf("局部任务 %s（run %s）已结束，status=%s。你是当前承接者；按 ID 核对结果与证据后决定后续，不需指挥官重复批准。状态通知不是新事实或执行授权。", cur.ID, cur.RunID, cur.Status)
						if q.Pending >= MaxQueuedMessages || q.Bytes+len(text) > MaxQueuedBytes {
							mem.TaskRoutePending = append(mem.TaskRoutePending, c.Seq)
							mem.TaskRouteThrough = max(mem.TaskRouteThrough, c.Seq)
							continue
						}
						now := time.Now()
						id := NewID("m")
						result := s.waitResultLocked(cur)
						s.db.Messages = append(s.db.Messages, model.Message{ID: id, EventID: id, Seq: s.nextMessageSeqLocked(), FromParticipantID: model.NoticeSenderID,
							ToParticipantID: to, Kind: model.NoticeKindTask, Durable: true, NoticeIntentID: cur.ID, NoticeRunID: cur.RunID, Text: text,
							FactRefs: append([]string(nil), result.FactIDs...), IdempotencyKey: key, Status: model.MessageQueued, CreatedAt: now, UpdatedAt: now})
						changed = true
					}
				}
			}
		}
		mem.TaskRouteThrough = max(mem.TaskRouteThrough, c.Seq)
	}
	// nextMessageSeqLocked may have updated Memory's sequence; preserve it.
	mem.Seq = s.db.Memory.Seq
	if mem.TaskRouteThrough != s.db.Memory.TaskRouteThrough || !slices.Equal(mem.TaskRoutePending, s.db.Memory.TaskRoutePending) {
		changed = true
	}
	if !changed {
		return nil
	}
	s.db.Memory = &mem
	return s.flushNoRev("message", "memory")
}

func waitingForChild(owner model.Intent, child string) bool {
	return owner.Wait != nil && hasID(owner.Wait.Children, child)
}

func (s *Store) PendingCoordinationNotices() []model.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []model.Message{}
	for _, m := range s.db.Messages {
		if m.Status == model.MessageQueued && (m.NoticeFactID != "" || (m.Kind == model.NoticeKindTask && m.FromParticipantID == model.NoticeSenderID)) {
			m.FactRefs = append([]string(nil), m.FactRefs...)
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}
