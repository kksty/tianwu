package store

// Durable per-recipient fact-change outbox (docs/32 stage 1B).
//
// A fact write is shared discovery: another direction that depends on the same
// premise should learn that the record changed, without replaying every fact body
// into every participant. The router reads the ledger's own change journal
// (Memory.History), so it cannot miss a committed change, and it persists its own
// progress, so it does not need the model watch loop to be running.
//
// One checkpoint row per live recipient keeps the cursor. The cursor advances
// only after the change was queued/coalesced or proven unrelated; a full queue
// leaves it alone (Deferred) so the change is retried instead of dropped. The
// notice body is a pointer (fact id, record version, change operation), never the
// fact text: routing decides WHO to tell, the recipient reads the record itself.

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/model"
)

// ErrMessageBackpressure means the recipient's queue is at its declared bound.
// It is a retryable, explicit refusal: the sender keeps its bytes and the caller
// reports the queue, instead of the ledger silently growing or the message
// disappearing.
var ErrMessageBackpressure = errors.New("收件队列容量已满，请稍后用同一幂等键重试")

const (
	// MaxQueuedMessages and MaxQueuedBytes bound one recipient's queue. They are
	// admission limits, not retention policy: durable rows are never swept.
	MaxQueuedMessages = 512
	MaxQueuedBytes    = 2 << 20

	routeBatchDefault = 64
	routeBatchCeiling = 256

	// noticeKeySep separates the recipient from the fact id in coalescing keys.
	// Neither id contains NUL, so the concatenation cannot collide.
	noticeKeySep = "\x00"
)

// QueueStats is one recipient's observable backlog: what is waiting, how much of
// the byte budget it uses, whether the router had to defer a change, and where its
// checkpoint stands. Cursor is a journal sequence, not a read receipt.
type QueueStats struct {
	Recipient string     `json:"recipient"`
	Pending   int        `json:"pending"`
	Bytes     int        `json:"bytes"`
	Oldest    *time.Time `json:"oldest,omitempty"`
	Deferred  bool       `json:"deferred"`
	Cursor    uint64     `json:"cursor"`
}

// queueStatsForLocked counts queue pressure for the requested recipients in one
// message scan. The router and the API both need several recipients at once, and
// a scan per recipient would make reporting cost grow with the roster.
func (s *Store) queueStatsForLocked(recipients map[string]bool) map[string]QueueStats {
	out := make(map[string]QueueStats, len(recipients))
	for to := range recipients {
		out[to] = QueueStats{Recipient: to}
	}
	for _, m := range s.db.Messages {
		q, ok := out[m.ToParticipantID]
		if !ok {
			continue
		}
		// The same three states admission counts: a row in flight or of unknown
		// outcome still occupies the recipient's budget.
		if m.Status != model.MessageQueued && m.Status != model.MessageSending && m.Status != model.MessageUnknown {
			continue
		}
		q.Pending++
		q.Bytes += len(m.Text)
		if q.Oldest == nil || m.CreatedAt.Before(*q.Oldest) {
			at := m.CreatedAt
			q.Oldest = &at
		}
		out[m.ToParticipantID] = q
	}
	for _, cp := range s.db.Routes {
		q, ok := out[cp.ID]
		if !ok {
			continue
		}
		q.Deferred, q.Cursor = cp.Deferred, cp.Cursor
		out[cp.ID] = q
	}
	return out
}

// queueStatsLocked is the single-recipient shape used by admission.
func (s *Store) queueStatsLocked(to string) QueueStats {
	return s.queueStatsForLocked(map[string]bool{to: true})[to]
}

// QueueStatus reports every queue that is worth showing: a live direction's
// checkpoint (even when empty) and any recipient that still holds a
// queued/in-flight row. A settled direction with nothing queued is omitted: it
// cannot be sent to or deliver, so a zero row would only be roster noise. A
// settled direction that still holds a queued notice is shown, because the store
// reconciles that row on the next routing pass rather than hiding it.
func (s *Store) QueueStatus(recipient string) []QueueStats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	live := make(map[string]bool, len(s.db.Intents))
	for _, in := range s.db.Intents {
		if !model.Settled(in.Status) {
			live[collab.ParticipantID(in.ID)] = true
		}
	}
	wanted := map[string]bool{}
	for _, cp := range s.db.Routes {
		if live[cp.ID] {
			wanted[cp.ID] = true
		}
	}
	for _, m := range s.db.Messages {
		switch m.Status {
		case model.MessageQueued, model.MessageSending, model.MessageUnknown:
			wanted[m.ToParticipantID] = true
		}
	}
	out := []QueueStats{}
	for to := range wanted {
		if recipient != "" && to != recipient {
			continue
		}
		out = append(out, s.queueStatsLocked(to))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Recipient < out[j].Recipient })
	return out
}

// entityWaiter is one live direction's claim on a recorded entity. Weight is 2
// when the direction names the entity itself and 1 when it inherits it from a
// fact it references: the match is equally relevant either way, but the index
// keeps the source visible instead of flattening an explicit subscription into an
// inherited one.
type entityWaiter struct {
	IntentID string
	Weight   int
}

// routeIndex is everything one routing pass needs to answer "who waits on this
// recorded change" without scanning the project per change. The draft did a full
// fact/intent scan for every event; building the index once makes a pass cost one
// ledger read plus the number of matches.
type routeIndex struct {
	factByID      map[string]model.Fact
	factWaiters   map[string][]string
	entityWaiters map[string][]entityWaiter
	descendants   map[string][]string
	queued        map[string]int
}

// buildRouteIndexLocked projects the current ledger once. Relation sources are
// explicit ledger fields only: required premises (FromFactIDs), recorded
// references (ContextFactIDs), pins (PinnedFactIDs), declared interest entities,
// entities recorded on referenced facts, and the parent chain. Never a fact
// title, summary or prose.
func (s *Store) buildRouteIndexLocked(live []model.Intent) *routeIndex {
	idx := &routeIndex{
		factByID:      make(map[string]model.Fact, len(s.db.Facts)),
		factWaiters:   map[string][]string{},
		entityWaiters: map[string][]entityWaiter{},
		descendants:   map[string][]string{},
		queued:        map[string]int{},
	}
	for _, f := range s.db.Facts {
		idx.factByID[f.ID] = f
	}
	parent := make(map[string]string, len(s.db.Intents))
	for _, in := range s.db.Intents {
		parent[in.ID] = in.ParentID
	}
	for _, in := range live {
		named := map[string]bool{}
		for _, list := range [][]string{in.FromFactIDs, in.ContextFactIDs, in.PinnedFactIDs} {
			for _, id := range list {
				if id == "" || named[id] {
					continue
				}
				named[id] = true
				idx.factWaiters[id] = append(idx.factWaiters[id], in.ID)
			}
		}
		weights := map[string]int{}
		for _, ref := range in.InterestEntityRefs {
			if ref == "" || weights[ref] >= 2 {
				continue
			}
			weights[ref] = 2
		}
		for id := range named {
			f, ok := idx.factByID[id]
			if !ok || f.EntityRef == "" || weights[f.EntityRef] >= 1 {
				continue
			}
			weights[f.EntityRef] = 1
		}
		entities := make([]string, 0, len(weights))
		for ref := range weights {
			entities = append(entities, ref)
		}
		sort.Strings(entities)
		for _, ref := range entities {
			idx.entityWaiters[ref] = append(idx.entityWaiters[ref], entityWaiter{IntentID: in.ID, Weight: weights[ref]})
		}
		seen := map[string]bool{}
		for p := parent[in.ID]; p != "" && !seen[p]; p = parent[p] {
			seen[p] = true
			idx.descendants[p] = append(idx.descendants[p], in.ID)
		}
	}
	for i, m := range s.db.Messages {
		if m.Status == model.MessageQueued && m.NoticeFactID != "" {
			idx.queued[m.ToParticipantID+noticeKeySep+m.NoticeFactID] = i
		}
	}
	return idx
}

// relevantFact answers whether one recorded fact change concerns one direction.
// The direction's own writes are excluded: it already knows, and echoing every
// declaration back to its author would make the outbox noise instead of sharing.
func (idx *routeIndex) relevantFact(f model.Fact, intentID string) bool {
	if f.IntentID == intentID {
		return false
	}
	for _, id := range idx.factWaiters[f.ID] {
		if id == intentID {
			return true
		}
	}
	if f.EntityRef != "" {
		for _, w := range idx.entityWaiters[f.EntityRef] {
			if w.IntentID == intentID {
				return true
			}
		}
	}
	if f.IntentID != "" {
		for _, id := range idx.descendants[f.IntentID] {
			if id == intentID {
				return true
			}
		}
	}
	return false
}

func factNoticeText(factID string, version uint64, operation string) string {
	return fmt.Sprintf(
		"共享发现变化：事实 %s 的记录版本 v%d（%s）。这是机械通知，不是事实正文，也不代表授权或已读；请按事实 ID 读取当前状态、版本与证据。",
		factID, version, operation)
}

// plannedNoticeRef locates one planned notice: either an existing message row
// being patched in place, or a row the pass will append.
type plannedNoticeRef struct {
	existing int
	newIndex int
}

// routeNoticePlan keeps the pass's message mutations off the live ledger until
// the whole pass succeeded. A corrupt journal entry therefore cannot leave a
// half-applied queue or an advanced cursor behind.
type routeNoticePlan struct {
	db       *DB
	existing map[int]model.Message
	byFact   map[string]plannedNoticeRef
	newRows  []model.Message
}

func (p *routeNoticePlan) row(ref plannedNoticeRef) model.Message {
	if ref.existing >= 0 {
		if m, ok := p.existing[ref.existing]; ok {
			return m
		}
		return p.db.Messages[ref.existing]
	}
	return p.newRows[ref.newIndex]
}

func (p *routeNoticePlan) setRow(ref plannedNoticeRef, m model.Message) {
	if ref.existing >= 0 {
		p.existing[ref.existing] = m
		return
	}
	p.newRows[ref.newIndex] = m
}

// planNoticeLocked folds one relevant change into the plan. false means the
// recipient's queue is at capacity and the caller must leave the checkpoint
// where it is; the change then survives in the journal for the next pass.
func planNoticeLocked(st *routeRecipient, idx *routeIndex, plan *routeNoticePlan, f model.Fact, version uint64, operation string) bool {
	to := collab.ParticipantID(st.in.ID)
	key := to + noticeKeySep + f.ID
	text := factNoticeText(f.ID, version, operation)
	if ref, ok := plan.byFact[key]; ok {
		// The same fact changed again within one pass: one queued row is the
		// point of coalescing, so keep the newest pointer and count the merge.
		m := plan.row(ref)
		m.Text, m.NoticeVersion, m.ToGeneration = text, version, st.in.Attempts
		m.UpdatedAt = time.Now()
		m.Coalesced++
		plan.setRow(ref, m)
		return true
	}
	if at, ok := idx.queued[to+noticeKeySep+f.ID]; ok {
		// A queued notice for the same record: update it in place. Delivered rows
		// (sending/accepted/confirmed/unknown) are not touched, because rewriting
		// them would falsify what the recipient may already have read.
		m := plan.row(plannedNoticeRef{existing: at, newIndex: -1})
		m.Text, m.NoticeVersion, m.ToGeneration = text, version, st.in.Attempts
		m.UpdatedAt = time.Now()
		m.Coalesced++
		plan.existing[at] = m
		plan.byFact[key] = plannedNoticeRef{existing: at, newIndex: -1}
		return true
	}
	if st.stats.Pending >= MaxQueuedMessages || st.stats.Bytes+len(text) > MaxQueuedBytes {
		return false
	}
	now := time.Now()
	m := model.Message{
		ID:                NewID("m"),
		FromParticipantID: model.NoticeSenderID,
		ToParticipantID:   to,
		ToGeneration:      st.in.Attempts,
		Kind:              model.NoticeKindFact,
		Text:              text,
		Durable:           true,
		NoticeFactID:      f.ID,
		NoticeVersion:     version,
		FactRefs:          []string{f.ID},
		Status:            model.MessageQueued,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	plan.newRows = append(plan.newRows, m)
	plan.byFact[key] = plannedNoticeRef{existing: -1, newIndex: len(plan.newRows) - 1}
	// Track the admission budget incrementally so a pass that queues many notices
	// for one recipient stops exactly at the declared bound.
	st.stats.Pending++
	st.stats.Bytes += len(text)
	return true
}

// applyRouteNoticesLocked writes the planned message mutations. Existing rows are
// patched first, then new rows are appended, so the existing indexes stay valid.
// It reports whether anything changed.
func (s *Store) applyRouteNoticesLocked(plan *routeNoticePlan) bool {
	changed := false
	for i, m := range plan.existing {
		s.db.Messages[i] = m
		changed = true
	}
	for i := range plan.newRows {
		m := plan.newRows[i]
		m.Seq = s.nextMessageSeqLocked()
		if m.EventID == "" {
			m.EventID = m.ID
		}
		s.db.Messages = append(s.db.Messages, m)
		changed = true
	}
	return changed
}

// routeRecipient is one live direction's cursor through the pass.
type routeRecipient struct {
	in        model.Intent
	row       int
	cursor    uint64
	processed int
	deferred  bool
	exhausted bool
	stats     QueueStats
}

// syncRouteCheckpointsLocked reconciles checkpoint rows with the live direction
// set and returns the live directions in deterministic order. purge names the
// recipients whose unsendable queued notices this pass must delete (handled by
// the caller together with the flush).
//
// A direction starts subscribing at the journal sequence of its own creation:
// facts recorded before it existed were visible when it was planned, and replaying
// them would be a flood, not a notification. While a direction is settled it has
// no participant to notify; its checkpoint is advanced to the journal head, so an
// explicit later reopen starts from the normal input path (board, graph, brief)
// instead of replaying the idle period.
func (s *Store) syncRouteCheckpointsLocked() (live []model.Intent, purge map[string]bool, changed bool) {
	var history []Change
	if s.db.Memory != nil {
		history = s.db.Memory.History
	}
	lastSeq := uint64(0)
	if len(history) > 0 {
		lastSeq = history[len(history)-1].Seq
	}
	createdAt := make(map[string]uint64, len(s.db.Intents))
	for _, c := range history {
		if c.Kind == "intent" && createdAt[c.ID] == 0 {
			createdAt[c.ID] = c.Seq
		}
	}
	live = make([]model.Intent, 0, len(s.db.Intents))
	liveSet := make(map[string]bool, len(s.db.Intents))
	for _, in := range s.db.Intents {
		if model.Settled(in.Status) {
			continue
		}
		live = append(live, in)
		liveSet[in.ID] = true
	}
	sort.Slice(live, func(i, j int) bool { return live[i].ID < live[j].ID })
	known := make(map[string]bool, len(s.db.Routes))
	for _, cp := range s.db.Routes {
		known[cp.ID] = true
	}
	for _, in := range live {
		to := collab.ParticipantID(in.ID)
		if known[to] {
			continue
		}
		start, ok := createdAt[in.ID]
		if !ok {
			// Defensive: an intent always journals its creation, so this is a
			// corrupt ledger. Starting at the journal head is the honest choice:
			// backfilling the whole history for one participant would be worse
			// than missing a change that the direction's own input already shows.
			start = lastSeq
		}
		s.db.Routes = append(s.db.Routes, model.RouteCheckpoint{ID: to, IntentID: in.ID, Cursor: start})
		known[to] = true
		changed = true
	}
	for i := range s.db.Routes {
		cp := &s.db.Routes[i]
		intentID, ok := collab.IntentIDFromParticipant(cp.ID)
		if ok && liveSet[intentID] {
			continue
		}
		if cp.Cursor < lastSeq || cp.Deferred {
			cp.Cursor, cp.Deferred = lastSeq, false
			changed = true
			// A settled direction has no delivery opportunity: nobody reads its
			// prompt again until an explicit reopen. The notice is only a pointer
			// to a record; on reopen the current facts arrive through the normal
			// input path (brief/graph/board), so keeping an unsendable pointer
			// would be a queue leak, not extra knowledge. Only this pass's
			// undelivered mechanical notices are reconciled here — ordinary
			// messages and sent/unknown rows are separate state and stay.
			if purge == nil {
				purge = map[string]bool{}
			}
			purge[cp.ID] = true
		}
	}
	return live, purge, changed
}

// purgeRouteNoticesLocked deletes queued mechanical notices addressed to the
// given recipients. It scans only for rows this store wrote itself (notice id
// set, still queued); participant messages and in-flight/unknown notices are not
// touched. It reports whether any row was removed.
func (s *Store) purgeRouteNoticesLocked(recipients map[string]bool) bool {
	if len(recipients) == 0 {
		return false
	}
	kept := make([]model.Message, 0, len(s.db.Messages))
	purged := false
	for _, m := range s.db.Messages {
		if recipients[m.ToParticipantID] && m.Status == model.MessageQueued && m.NoticeFactID != "" {
			purged = true
			continue
		}
		kept = append(kept, m)
	}
	if !purged {
		return false
	}
	s.db.Messages = kept
	return true
}

// flushRouteState commits the pass's route and/or message changes. Notices and
// checkpoints are joined into one transaction whenever both changed.
func (s *Store) flushRouteState(routeChanged, messageChanged bool) error {
	switch {
	case routeChanged && messageChanged:
		return s.flushNoRev("route", "message")
	case routeChanged:
		return s.flushNoRev("route")
	case messageChanged:
		return s.flushNoRev("message")
	}
	return nil
}

// RouteFactChanges is the durable outbox for recorded fact changes.
//
// The caller must hold no store lock: fact paths commit first and then call this
// (the store write methods call it after unlocking), and the watch loop calls it
// as restart recovery. It is idempotent — the checkpoint, not the caller, decides
// what is new — so calling it too often is harmless and calling it too late can
// only delay a notice, never lose one.
//
// perRecipient bounds how many journal entries one recipient consumes in a single
// pass so a large declaration batch cannot starve other bookkeeping; the rest are
// picked up by the next pass. Notices and checkpoint rows commit together, in one
// transaction, so a restart can never see a notice without its cursor or a cursor
// without its notice.
func (s *Store) RouteFactChanges(perRecipient int) error {
	if perRecipient <= 0 || perRecipient > routeBatchCeiling {
		perRecipient = routeBatchDefault
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db.Memory == nil || len(s.db.Memory.History) == 0 {
		return nil
	}
	// A mid-pass error must not leave the live checkpoint slices half-updated.
	routesBefore := append([]model.RouteCheckpoint(nil), s.db.Routes...)
	live, purge, changed := s.syncRouteCheckpointsLocked()
	if len(live) == 0 {
		// No live recipient: only settled checkpoints may need advancing and
		// their unsendable notices reconciling. There is no journal scan to do.
		return s.flushRouteState(changed, s.purgeRouteNoticesLocked(purge))
	}
	history := s.db.Memory.History
	lastSeq := history[len(history)-1].Seq
	rows := make(map[string]int, len(s.db.Routes))
	for i, cp := range s.db.Routes {
		rows[cp.ID] = i
	}
	wanted := make(map[string]bool, len(live))
	for _, in := range live {
		wanted[collab.ParticipantID(in.ID)] = true
	}
	stats := s.queueStatsForLocked(wanted)
	states := make([]*routeRecipient, 0, len(live))
	minCursor := lastSeq
	pending := false
	for _, in := range live {
		to := collab.ParticipantID(in.ID)
		row, ok := rows[to]
		if !ok {
			continue
		}
		st := &routeRecipient{in: in, row: row, cursor: s.db.Routes[row].Cursor, stats: stats[to]}
		if st.cursor < lastSeq {
			pending = true
		}
		if st.cursor < minCursor {
			minCursor = st.cursor
		}
		states = append(states, st)
	}
	if !pending {
		return s.flushRouteState(changed, s.purgeRouteNoticesLocked(purge))
	}
	idx := s.buildRouteIndexLocked(live)
	plan := &routeNoticePlan{db: &s.db, existing: map[int]model.Message{}, byFact: map[string]plannedNoticeRef{}}
	start := sort.Search(len(history), func(i int) bool { return history[i].Seq > minCursor })
	for _, c := range history[start:] {
		for _, st := range states {
			if st.exhausted || st.deferred || c.Seq <= st.cursor {
				continue
			}
			if c.Kind != "fact" {
				// Not this recipient's correspondence: consume it so the cursor
				// tracks the journal instead of stalling before unrelated rows.
				st.advance(c.Seq, perRecipient)
				continue
			}
			if c.Operation == "delete" {
				// No fact-delete path exists today (facts are corrected, never
				// removed). A tombstone change carries no row to describe, so it
				// is consumed; if deletion is ever added it must record a value
				// the router can name, not leave the outbox stalled here.
				st.advance(c.Seq, perRecipient)
				continue
			}
			var f model.Fact
			if err := json.Unmarshal(c.Value, &f); err != nil {
				s.db.Routes = routesBefore
				return fmt.Errorf("路由事实变化 %s：%w", c.ID, err)
			}
			if !idx.relevantFact(f, st.in.ID) {
				st.advance(c.Seq, perRecipient)
				continue
			}
			if !planNoticeLocked(st, idx, plan, f, c.Version, c.Operation) {
				// Capacity: the change stays in the journal, checkpoint does not
				// move, and the observation is recorded as Deferred for the UI.
				st.deferred = true
				continue
			}
			st.advance(c.Seq, perRecipient)
		}
		// Every recipient is either done with the journal, done with its batch
		// for this pass, or capacity-blocked: no later entry can be consumed.
		stop := true
		for _, st := range states {
			if !st.exhausted && !st.deferred && st.cursor < lastSeq {
				stop = false
				break
			}
		}
		if stop {
			break
		}
	}
	for _, st := range states {
		cp := &s.db.Routes[st.row]
		if cp.Cursor == st.cursor && cp.Deferred == st.deferred {
			continue
		}
		cp.Cursor, cp.Deferred = st.cursor, st.deferred
		changed = true
	}
	notices := s.applyRouteNoticesLocked(plan)
	// The dead-recipient purge runs after the plan is applied: the plan addresses
	// existing rows by slice index, so deleting rows first would invalidate it.
	purged := s.purgeRouteNoticesLocked(purge)
	return s.flushRouteState(changed, notices || purged)
}

// advance records one consumed journal entry and stops the recipient for this
// pass once its batch budget is spent. A batch stop is not a deferral: there is
// room in the queue, only the pass is over.
func (st *routeRecipient) advance(seq uint64, perRecipient int) {
	st.cursor = seq
	st.processed++
	if st.processed >= perRecipient {
		st.exhausted = true
	}
}

// PendingFactNotices is the durable queue the delivery path drains into live
// sessions. It returns copies ordered by message sequence; a row whose recipient
// is between runs stays queued and rides the next prompt.
func (s *Store) PendingFactNotices() []model.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []model.Message{}
	for _, m := range s.db.Messages {
		if m.NoticeFactID == "" || m.Status != model.MessageQueued {
			continue
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}
