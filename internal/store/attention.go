package store

import (
	"fmt"
	"sort"
	"time"

	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/model"
)

// AttentionReason is a recorded reason to offer a GLOBAL decision opportunity.
// It does not decide strategy or infer intent from titles/model prose.
type AttentionReason struct {
	Code     string `json:"code"`
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	IntentID string `json:"intent_id,omitempty"`
	Seq      uint64 `json:"seq,omitempty"`
	Version  uint64 `json:"version,omitempty"`
	Detail   string `json:"detail"`
}

type AttentionBatch struct {
	Through          uint64            `json:"through"`
	DeliveredThrough uint64            `json:"delivered_through"`
	NativeThrough    uint64            `json:"native_through,omitempty"`
	Local            int               `json:"local_events"`
	More             bool              `json:"more"`
	Reasons          []AttentionReason `json:"reasons"`
}

func coordinatorLocked(in model.Intent, intents map[string]model.Intent) string {
	seen := map[string]bool{in.ID: true}
	for id := in.ParentID; id != "" && !seen[id]; {
		seen[id] = true
		p, ok := intents[id]
		if !ok {
			break
		}
		if !model.Settled(p.Status) && !p.Hold && !p.Aborted {
			return p.ID
		}
		id = p.ParentID
	}
	if id, ok := collab.IntentIDFromParticipant(in.CreatedBy); ok && !seen[id] {
		if p, exists := intents[id]; exists && !model.Settled(p.Status) && !p.Hold && !p.Aborted {
			return id
		}
	}
	return ""
}

// IntentCoordinator identifies the live parent/creator responsible for a local
// result. Creation and task ownership are separate relationships.
func (s *Store) IntentCoordinator(id string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	intents := make(map[string]model.Intent, len(s.db.Intents))
	for _, in := range s.db.Intents {
		intents[in.ID] = in
	}
	return coordinatorLocked(intents[id], intents)
}

// CommanderAttention reads the existing durable change journal. A process crash
// or a failed model request cannot lose the demand: only an explicit successful
// delivery advances AttentionThrough. This is independent of context/ack.
func (s *Store) CommanderAttention() AttentionBatch {
	return s.CommanderAttentionAt(time.Now())
}

// CommanderAttentionAt is CommanderAttention with the caller's clock. Retry
// backoff is the only time-dependent part: an unacknowledged context delivery
// is a pending demand only when its schedule says it is due, so the heartbeat
// cannot buy a full global round every tick for the same unacknowledged page.
// CommanderAttention stays the production entry point (real clock); tests walk
// the timeline instead of sleeping.
func (s *Store) CommanderAttentionAt(at time.Time) AttentionBatch {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := AttentionBatch{Reasons: []AttentionReason{}}
	if s.db.Memory == nil {
		return out
	}
	mem := s.db.Memory
	out.Through, out.DeliveredThrough = mem.AttentionThrough, mem.AttentionThrough
	out.NativeThrough = mem.NativeMailSeq
	intents := make(map[string]model.Intent, len(s.db.Intents))
	for _, in := range s.db.Intents {
		intents[in.ID] = in
	}
	facts := make(map[string]model.Fact, len(s.db.Facts))
	for _, f := range s.db.Facts {
		facts[f.ID] = f
	}
	reasons := map[string]AttentionReason{}
	var routing *routeIndex
	live := []model.Intent{}
	for _, in := range s.db.Intents {
		if !model.Settled(in.Status) && !in.Hold && !in.Aborted {
			live = append(live, in)
		}
	}
	add := func(key string, c Change, code, detail, intent string) {
		reasons[key] = AttentionReason{Code: code, Kind: c.Kind, ID: c.ID, IntentID: intent, Seq: c.Seq, Version: c.Version, Detail: detail}
	}
	start := sort.Search(len(mem.History), func(i int) bool { return mem.History[i].Seq > mem.AttentionThrough })
	end := min(start+512, len(mem.History))
	out.More = end < len(mem.History)
	for _, c := range mem.History[start:end] {
		out.Through = c.Seq
		// Superseded failures/claims do not ask a model to undo a newer holder.
		r, exists := s.records[recordKey(c.Kind, c.ID)]
		if !exists || r.version != c.Version {
			out.Local++
			continue
		}
		switch c.Kind {
		case "goal":
			g := r.value.(model.Goal)
			if c.Operation == "create" && g.CreatedByRun != "" {
				if creator, ok := s.records[recordKey("run", g.CreatedByRun)]; ok {
					run := creator.value.(model.Run)
					if run.Purpose == "commander" && run.Status == model.RunDone {
						out.Local++
						continue
					}
				}
			}
			add("goal:"+c.ID, c, "goal_changed", "目标或完成证明发生变化", "")
		case "instruction":
			add("instruction:"+c.ID, c, "instruction_changed", "操作员的持久约束发生变化", "")
		case "intent":
			in := r.value.(model.Intent)
			if coordinatorLocked(in, intents) != "" {
				out.Local++
				continue
			}
			if in.Status == model.StatusDone || in.Status == model.StatusFailed || in.Status == model.StatusReviewFailed || in.Status == model.StatusParked {
				add("intent:"+in.ID, c, "unowned_result", "方向已结束，没有局部承接者；需要全局判断", in.ID)
			} else if in.Status == model.StatusPending && !in.Hold && in.Wait == nil {
				add("intent:"+in.ID, c, "unowned_work", "待推进方向没有局部承接者", in.ID)
			} else {
				out.Local++
			}
		case "fact":
			f := r.value.(model.Fact)
			// A review (or a corrected assertion) can clear the exact blocker a
			// parked direction recorded. Signal it once here, before the
			// local-consumption branches below: the premise may have been produced
			// by a direction that is still running, and that must not swallow the
			// parked direction's notice.
			if f.Status == "verified" {
				for _, parked := range s.db.Intents {
					if parked.Status != model.StatusParked || !hasID(parked.ParkedPremiseFactIDs, f.ID) {
						continue
					}
					if !parkedPremiseReady(parked, facts, s.db.Reviews) {
						continue
					}
					reasons["parked:"+parked.ID] = AttentionReason{
						Code: "parked_premise_ready", Kind: "intent", ID: parked.ID, IntentID: parked.ID,
						Seq: c.Seq, Version: c.Version,
						Detail: "停放方向的前提已复核通过，可重开或确认合并",
					}
				}
			}
			for _, g := range s.db.Goals {
				if g.Met && hasID(g.From, f.ID) && markGoalReceipt(g, s.db.Facts).ReceiptStale {
					add("goal:"+g.ID, c, "goal_support_changed", "完成证明的依据变化，需要重新验收", f.IntentID)
				}
			}
			in, ok := intents[f.IntentID]
			if ok && ((!model.Settled(in.Status) && !in.Hold && !in.Aborted) || coordinatorLocked(in, intents) != "") {
				out.Local++
				continue
			}
			if routing == nil {
				routing = s.buildRouteIndexLocked(live)
			}
			consumedLocally := false
			for _, target := range live {
				if routing.relevantFact(f, target.ID) {
					consumedLocally = true
					break
				}
			}
			if consumedLocally {
				out.Local++
				continue
			}
			key := f.IntentID
			if key == "" {
				key = "project"
			}
			add("fact:"+key, c, "unowned_discovery", "发现没有局部承接者；需要全局判断", f.IntentID)
		default:
			out.Local++
		}
	}
	if !out.More {
		out.Through = mem.Seq
	}
	// Explicit requests are already durable messages. Only queued messages may
	// buy another round; unknown transport outcomes are never blindly replayed.
	for _, m := range s.db.Messages {
		if m.ToParticipantID == collab.CommanderID && m.Status == model.MessageQueued {
			reasons["message:"+m.ID] = AttentionReason{Code: "direct_request", Kind: "message", ID: m.ID, Seq: m.Seq, Detail: "有明确发给指挥官的请求"}
		}
	}
	if d, ok := mem.Deliveries["commander"]; ok && deliveryDue(d, at) {
		detail := "上一轮工作上下文尚未确认，保留原交付重试"
		if d.Attempts > 0 {
			detail = fmt.Sprintf("工作上下文尚未确认（已展示 %d 次），按退避重试原交付", d.Attempts)
		}
		reasons["context:"+d.ID] = AttentionReason{Code: "context_unacknowledged", Kind: "context", ID: d.ID, Detail: detail}
	}
	if mem.NativeMailSeq > mem.NativeMailThrough {
		reasons["native:"+mem.NativeMailID] = AttentionReason{Code: "native_request", Kind: "native_message", ID: mem.NativeMailID, Version: mem.NativeMailSeq, Detail: "原生邮箱有明确发给指挥官的消息；正文仍由 harness 读取"}
	}
	for _, v := range reasons {
		out.Reasons = append(out.Reasons, v)
	}
	sort.Slice(out.Reasons, func(i, j int) bool {
		if out.Reasons[i].Seq == out.Reasons[j].Seq {
			return out.Reasons[i].ID < out.Reasons[j].ID
		}
		return out.Reasons[i].Seq < out.Reasons[j].Seq
	})
	return out
}

// parkedPremiseReady reports whether every premise that blocked a parked
// direction at park time is usable now. It uses the same assertion and proof
// rules as the dispatch gate, without re-hashing original files: the signal is
// "the blocker cleared", not a substitute for the final readiness check.
func parkedPremiseReady(in model.Intent, facts map[string]model.Fact, reviews []model.Review) bool {
	if len(in.ParkedPremiseFactIDs) == 0 {
		return false
	}
	for _, id := range in.ParkedPremiseFactIDs {
		f, ok := facts[id]
		if !ok {
			return false
		}
		if model.PremiseIssue(f, model.PremiseVerifiedOnly) != "" {
			return false
		}
		if _, ok := model.CurrentProof(f, reviews); !ok {
			return false
		}
	}
	return true
}

func hasID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// AdvanceCommanderAttention records delivery, not understanding, fact ACK or
// external execution. Events written after through remain pending.
func (s *Store) AdvanceCommanderAttention(through uint64) error {
	return s.CompleteCommanderAttention(AttentionBatch{Through: through})
}

func (s *Store) CompleteCommanderAttention(batch AttentionBatch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db.Memory == nil {
		return nil
	}
	if batch.Through > s.db.Memory.Seq || batch.NativeThrough > s.db.Memory.NativeMailSeq {
		return fmt.Errorf("attention checkpoint is ahead of the ledger")
	}
	mem := *s.db.Memory
	if batch.Through <= mem.AttentionThrough && batch.NativeThrough <= mem.NativeMailThrough {
		return nil
	}
	mem.AttentionThrough = max(mem.AttentionThrough, batch.Through)
	mem.NativeMailThrough = max(mem.NativeMailThrough, batch.NativeThrough)
	s.db.Memory = &mem
	return s.flushNoRev("memory")
}

// NoteCommanderNativeMail persists scheduling demand, never a second mailbox,
// read receipt or fact. A later native message cannot be consumed by an old round.
func (s *Store) NoteCommanderNativeMail(id string) error {
	return s.NoteCommanderNativeMailAt(id, "", 0)
}

// NoteCommanderNativeMailAt atomically records demand and the observer's
// durable sequence. Replaying an older callback cannot wake a consumed round.
func (s *Store) NoteCommanderNativeMailAt(id, observer string, seq int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == "" || s.db.Memory == nil {
		return fmt.Errorf("native mail requires an identity and memory")
	}
	mem := *s.db.Memory
	if observer != "" {
		if seq <= 0 {
			return fmt.Errorf("native mail observer requires a positive sequence")
		}
		if mem.NativeMailCursors[observer] >= seq {
			return nil
		}
		mem.NativeMailCursors = make(map[string]int64, len(s.db.Memory.NativeMailCursors)+1)
		for source, through := range s.db.Memory.NativeMailCursors {
			mem.NativeMailCursors[source] = through
		}
		mem.NativeMailCursors[observer] = seq
	} else if mem.NativeMailID == id {
		return nil
	}
	mem.NativeMailID = id
	mem.NativeMailSeq++
	s.db.Memory = &mem
	return s.flushNoRev("memory")
}
