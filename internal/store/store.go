// Package store is Tianwu's ledger persistence.
//
// All project ledgers use embedded SQLite. JSON is only a portable export format.
package store

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kksty/tianwu/internal/model"
)

type DB struct {
	Routes        []model.RouteCheckpoint `json:"routes,omitempty"`
	Instructions  []model.Instruction     `json:"instructions,omitempty"`
	ReadEdges     []Relation              `json:"-"`
	ReadExistence map[string]string       `json:"-"`
	ReadRevision  uint64                  `json:"-"`
	ContentHash   string                  `json:"-"`
	Memory        *Memory                 `json:"memory,omitempty"`
	Actions       []model.ExternalAction  `json:"actions,omitempty"`
	Version       int                     `json:"version"`
	Intents       []model.Intent          `json:"intents"`
	Facts         []model.Fact            `json:"facts"`
	Evidence      []model.Evidence        `json:"evidence"`
	Goals         []model.Goal            `json:"goals"`
	Reviews       []model.Review          `json:"reviews"`
	Runs          []model.Run             `json:"runs"`
	// Playbooks is project knowledge, not findings: a reusable tactic with
	// provenance but no evidence and no review lifecycle. It is a table in the
	// same snapshot because the ledger is one atomic state, not because a
	// playbook is a fact — nothing in the fact paths (board counts, report,
	// verification) reads this field (D70).
	Playbooks []model.Playbook `json:"playbooks,omitempty"`
	// Messages are participant-to-participant conversation. They share the
	// atomic snapshot because idempotency and delivery state must survive a
	// restart with the rest of the project, but they are not facts: board,
	// report, review and coverage never read this field.
	Messages []model.Message `json:"messages,omitempty"`
	// MessageSeq is the read cursor's high-water mark. It is stored instead of
	// derived from the rows because a withdrawn or swept message is deleted: a
	// maximum over the surviving rows would rewind and a reader's `after`
	// cursor would then skip the next message (D86).
	MessageSeq uint64 `json:"message_seq,omitempty"`
}

type Store struct {
	sqlite         *sqliteBackend
	records        map[string]record
	recordKinds    map[string]map[string]record
	readCacheMu    sync.Mutex
	readCache      map[uint64]cachedRead
	readCacheOrder []uint64
	readCacheBytes int
	readEpoch      string
	mu             sync.RWMutex
	path           string
	db             DB
	// rev counts durable writes. It is the decider's "the world changed" signal
	// for edits no count can see: a fact's title/doc/summary rewritten, a ref
	// added, a row changed while it sat outside the board's top-N (D79). Live
	// pulse touches (NoteEvent, ProgressRun) deliberately do not bump it — those
	// are "still alive", not news, and waking on them would burn a round per
	// event.
	rev uint64
	// Failed transactions restore the last committed revision and SQLite rows.
	committedRev uint64
	// onFact is called after a write, outside the lock. The instance uses it to
	// mirror vulnerabilities into the global library; the store itself stays
	// project-scoped and knows nothing about the library.
	onFact func(model.Fact, []string)
}

// OnFact registers a mirror hook. It runs with the store unlocked, after the
// write is durable: a hook that reads this store would deadlock otherwise.
func (s *Store) OnFact(fn func(model.Fact, []string)) {
	s.mu.Lock()
	s.onFact = fn
	s.mu.Unlock()
}

// evidenceURIsLocked maps evidence ids to case-relative URIs. Caller holds the
// lock.
func (s *Store) evidenceURIsLocked(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	byID := make(map[string]string, len(s.db.Evidence))
	for _, e := range s.db.Evidence {
		byID[e.ID] = e.URI
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if uri, ok := byID[id]; ok {
			out = append(out, uri)
		}
	}
	return out
}

// schemaVersion identifies the current SQLite layout; older layouts are unsupported.
const schemaVersion = 13

// CurrentSchemaVersion is the layout this build writes and accepts. It is
// exported for fixtures that build a ledger snapshot directly: a hard-coded
// number in a test is a silent schema claim that stops being true on the next
// bump.
func CurrentSchemaVersion() int { return schemaVersion }

func Open(path string) (*Store, error) { return OpenSQLite(path) }

func NewID(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("ledger identity entropy unavailable: " + err.Error())
	}
	return fmt.Sprintf("%s_%s", prefix, hex.EncodeToString(b[:]))
}

// flush must be called with the write lock held.
//
// Affected rows, revisions, indexes and metadata commit in one SQLite
// transaction. A failed commit restores the last committed in-memory state.
func (s *Store) flush(kinds ...string) error {
	s.rev++
	return s.flushNoRev(kinds...)
}

// flushNoRev persists without bumping the decider's change counter. Messages
// use it: they are durable project state, but a chat line is not a change to the
// fact/intent graph, and waking the commander once per message would burn a
// model round for nothing (D79 defines rev as that wake signal, not a raw write
// count).
func (s *Store) flushNoRev(kinds ...string) error { return s.flushSQLite(kinds...) }

func (s *Store) CreateIntent(in model.Intent) (model.Intent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.applyInitialContextLocked(&in); err != nil {
		return model.Intent{}, err
	}
	s.snapshotPremiseClaimsLocked(&in)
	now := time.Now()
	in.ID = NewID("in")
	in.Status = model.StatusPending
	in.CreatedAt, in.UpdatedAt = now, now
	in = detachIntent(in)
	s.db.Intents = append(s.db.Intents, in)
	return detachIntent(in), s.flush("intent")
}

func (s *Store) ListIntents() []model.Intent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Intent, len(s.db.Intents))
	for i, in := range s.db.Intents {
		out[i] = detachIntent(in)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

func (s *Store) GetIntent(id string) (model.Intent, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, in := range s.db.Intents {
		if in.ID == id {
			return detachIntent(in), true
		}
	}
	return model.Intent{}, false
}

// ClaimIntent atomically moves pending -> claimed. It is the only concurrency
// guard in v0: no lease, no expiry, no version. A dead worker is recovered by
// the watchdog resetting the intent to pending.
//
// A claim is also the end of an abort marking. Aborted describes how the run
// that was just cancelled should settle (failed, so a parent's wait unblocks);
// it is not a property of the direction forever. Keeping it sticky made every
// later cancellation of that direction settle as failed+Hold, so after one
// worker kill / subtree cancel a retried direction could never be steered
// again: steer cancels the run and waits for pending, and the stale flag sent
// it to failed instead. Every deliberate start path (run, retry, continue,
// wake) goes through this claim; the scheduler skips held intents, so a
// cancelled subtree cannot restart itself this way.
func (s *Store) ClaimIntent(id, by string, owner ...string) (model.Intent, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.db.Intents {
		in := &s.db.Intents[i]
		if in.ID != id {
			continue
		}
		if in.Status != model.StatusPending && in.Status != model.StatusFailed && in.Status != model.StatusReviewFailed {
			return detachIntent(*in), false, nil
		}
		if !handoffOwnerMatches(in, firstOwner(owner)) {
			return detachIntent(*in), false, ErrHandoffConflict
		}
		in.Status = model.StatusClaimed
		in.ClaimedBy = by
		in.Hold = false
		in.HandoffOperation = ""
		in.Aborted = false
		in.Attempts++
		in.UpdatedAt = time.Now()
		return detachIntent(*in), true, s.flush("intent")
	}
	return model.Intent{}, false, fmt.Errorf("intent %s not found", id)
}

// ReopenIntent moves a parked intent back to pending. Re-opening is explicit:
// a park is a decision, so undoing it is one too (docs/06, D9).
func (s *Store) ReopenIntent(id string) (model.Intent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.db.Intents {
		in := &s.db.Intents[i]
		if in.ID != id {
			continue
		}
		if in.Status != model.StatusParked {
			return detachIntent(*in), fmt.Errorf("intent %s 不是停放状态（%s）", id, in.Status)
		}
		in.Status = model.StatusPending
		in.ParkReason = ""
		in.Hold = false
		in.UpdatedAt = time.Now()
		return detachIntent(*in), s.flush("intent")
	}
	return model.Intent{}, fmt.Errorf("intent %s not found", id)
}

func (s *Store) UpdateIntent(id string, fn func(*model.Intent)) (model.Intent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.db.Intents {
		if s.db.Intents[i].ID != id {
			continue
		}
		previous := detachIntent(s.db.Intents[i])
		fn(&s.db.Intents[i])
		row := detachIntent(s.db.Intents[i])
		if err := s.validateIntentStructureLocked(&row, previous); err != nil {
			s.db.Intents[i] = previous
			return model.Intent{}, err
		}
		s.db.Intents[i] = row
		s.db.Intents[i].UpdatedAt = time.Now()
		return detachIntent(s.db.Intents[i]), s.flush("intent")
	}
	return model.Intent{}, fmt.Errorf("intent %s not found", id)
}

// UpdateIntentIfCurrent prepares a lifecycle transition only for the run/status
// the caller inspected. Stale retries must not reset an already claimed run.
func (s *Store) UpdateIntentIfCurrent(id, status, runID string, fn func(*model.Intent)) (model.Intent, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.db.Intents {
		in := &s.db.Intents[i]
		if in.ID != id {
			continue
		}
		if in.Status != status || in.RunID != runID {
			return detachIntent(*in), false, nil
		}
		fn(in)
		*in = detachIntent(*in)
		in.UpdatedAt = time.Now()
		out := detachIntent(*in)
		err := s.flush("intent")
		return out, err == nil, err
	}
	return model.Intent{}, false, fmt.Errorf("intent %s not found", id)
}

func (s *Store) AddFact(f model.Fact) (model.Fact, error) {
	s.mu.Lock()
	f = detachFact(f)
	f.ID = NewID("f")
	if f.CreatedAt.IsZero() {
		f.CreatedAt = time.Now()
	}
	if f.Confidence == "" {
		f.Confidence = "observed"
	}
	if f.Status == "" {
		f.Status = "unverified"
	}
	// Unknown origin is treated as the least-trusted one: a forgotten call site
	// must not be able to inflate the board's declared count.
	if f.Origin == "" {
		f.Origin = model.OriginHeuristic
	}
	// A new row is the first version of its assertion. The fingerprint is
	// computed from the claim only, never from the row's review status or its
	// display title (see claimHash).
	f.ClaimVersion, f.ClaimHash = 1, claimHash(f)
	s.db.Facts = append(s.db.Facts, f)
	err := s.flush("fact")
	hook := s.onFact
	uris := s.evidenceURIsLocked(f.EvidenceIDs)
	s.mu.Unlock()

	if err == nil && hook != nil {
		hook(f, uris)
	}
	if err == nil {
		s.notifyFactChange()
	}
	return detachFact(f), err
}

// notifyFactChange runs one association-routing pass after a committed fact
// change. It is called without the write lock (RouteFactChanges takes it), and
// a routing failure is only logged: the fact commit stands, the change is
// durable in the journal, and the next pass resumes from each recipient's
// checkpoint, so a delayed notice is never a lost one.
func (s *Store) notifyFactChange() {
	if err := s.RouteFactChanges(64); err != nil {
		log.Printf("关联事实通知入队失败（事实已提交，游标未推进，下次重试）：%v", err)
	}
}

// FindFact is gone: matching explicit observations by (intent, kind, title)
// made two real observations with the same label collapse into one row. The
// remaining title identity belongs to a single derived note and lives in
// FindDerivedFact (claim.go); explicit declarations resolve through
// FindDeclaration by writer-supplied identity (D74).

// UpdateFact mutates an existing row and flushes. The ID and CreatedAt stay:
// a live fact that later gains evidence is the same fact, not a new one.
//
// Changing the claim itself (summary, doc, evidence, tags, proof, ...) starts a
// new assertion version and returns the row to unverified: an old verdict
// proved the old content. Bookkeeping-only writes (source run, declaration ref,
// display title) keep the version and the review status.
func (s *Store) UpdateFact(id string, fn func(*model.Fact)) (model.Fact, error) {
	s.mu.Lock()
	var out model.Fact
	found := false
	for i := range s.db.Facts {
		if s.db.Facts[i].ID != id {
			continue
		}
		s.db.Facts[i] = detachFact(s.db.Facts[i])
		previous := s.db.Facts[i]
		fn(&s.db.Facts[i])
		row := &s.db.Facts[i]
		row.ClaimHash = claimHash(*row)
		if row.ClaimHash != previous.ClaimHash {
			row.ClaimVersion = previous.ClaimVersion + 1
			row.Status = "unverified"
		} else {
			row.ClaimVersion = previous.ClaimVersion
		}
		s.db.Facts[i] = detachFact(s.db.Facts[i])
		out = detachFact(s.db.Facts[i])
		found = true
		break
	}
	if !found {
		s.mu.Unlock()
		return model.Fact{}, fmt.Errorf("fact %s not found", id)
	}
	err := s.flush("fact")
	hook := s.onFact
	uris := s.evidenceURIsLocked(out.EvidenceIDs)
	s.mu.Unlock()
	if err == nil && hook != nil {
		hook(out, uris)
	}
	if err == nil {
		s.notifyFactChange()
	}
	return out, err
}

func (s *Store) ListFacts(intentID string, limit int) []model.Fact {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Fact, 0, len(s.db.Facts))
	for _, f := range s.db.Facts {
		if intentID == "" || f.IntentID == intentID {
			out = append(out, detachFact(f))
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (s *Store) AddEvidence(e model.Evidence) (model.Evidence, error) {
	e, err := normalizeEvidence(e)
	if err != nil {
		return model.Evidence{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Replaying a committed declaration must keep its proof identity. The same
	// preserved bytes at the same source in one intent reuse their first
	// admission; a new capture time belongs to the fact's observation metadata.
	if e.Digest != "" && e.IntentID != "" {
		for _, old := range s.db.Evidence {
			if old.IntentID == e.IntentID && old.URI == e.URI && old.Digest == e.Digest &&
				old.SourceURI == e.SourceURI && old.From == e.From && old.Name == e.Name && old.Mime == e.Mime && old.Size == e.Size {
				return old, nil
			}
		}
	}
	e.ID = NewID("e")
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now()
	}
	s.db.Evidence = append(s.db.Evidence, e)
	return e, s.flush("evidence")
}

func (s *Store) AddRun(r model.Run) (model.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r.ID = NewID("r")
	r.StartedAt = time.Now()
	r.Status = "running"
	r = detachRun(r)
	s.db.Runs = append(s.db.Runs, r)
	return detachRun(r), s.flush("run")
}

// NoteEvent updates the live pulse of a running run without flushing to disk.
// It is called for every executor event: the board needs to see activity, but
// a durable transaction per event would make the ledger the bottleneck. A
// later run write persists the pulse; finish replaces Events with the harness's
// authoritative count.
//
// Beyond the counter it keeps the one-line glance: the most recent *meaningful*
// event's kind/tool/text. Usage and delta events keep the counter moving but do
// not overwrite the glance — "still alive" is not "doing something", and a
// board that says "usage" on every run tells the decider nothing.
//
// The text is clipped here, at the write: this field rides in every board
// prompt, so it must stay a glance, not a transcript. The run_events tool
// carries the tail for the rounds that need to look closer (D57, D71).
func (s *Store) NoteEvent(id string, at time.Time, kind, tool, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.db.Runs {
		if s.db.Runs[i].ID != id {
			continue
		}
		s.db.Runs[i].LastEventAt = &at
		s.db.Runs[i].Events++
		if kind == "turn_started" {
			// Persisted by the run's finish (which overwrites/flushes the row).
			// Redelivery uses it as "messages from this instant on may be
			// unread"; zero means the run never started a turn.
			s.db.Runs[i].LastTurnStartedAt = &at
		}
		switch kind {
		case "", "usage", "message_delta", "raw", "turn_started":
			// Keep the previous glance.
		default:
			s.db.Runs[i].LastEventKind = kind
			s.db.Runs[i].LastEventTool = tool
			s.db.Runs[i].LastEventText = clipEventText(text, 160)
		}
		break
	}
}

// ObserveRun records execution metadata separately from the human-readable pulse.
// Failures are transient until the next durable write, like NoteEvent. Session
// identity is flushed once so communication remains attributable after restart.
func (s *Store) ObserveRun(id, sessionID string, failed bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.db.Runs {
		r := &s.db.Runs[i]
		if r.ID != id {
			continue
		}
		if failed {
			r.ToolFailures++
		}
		if sessionID != "" && r.SessionID != sessionID {
			r.SessionID = sessionID
			return s.flush("run")
		}
		return nil
	}
	return fmt.Errorf("run %s not found", id)
}

func clipEventText(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

func (s *Store) UpdateRun(id string, fn func(*model.Run)) (model.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.db.Runs {
		if s.db.Runs[i].ID != id {
			continue
		}
		s.db.Runs[i] = detachRun(s.db.Runs[i])
		fn(&s.db.Runs[i])
		s.db.Runs[i] = detachRun(s.db.Runs[i])
		if s.db.Runs[i].Status != model.RunRunning && s.db.Runs[i].CancelCause != model.CancelCauseActionWait {
			s.markActionsUnknownLocked(id)
		}
		return detachRun(s.db.Runs[i]), s.flush("run", "action", "intent")
	}
	return model.Run{}, fmt.Errorf("run %s not found", id)
}

func (s *Store) GetRun(id string) (model.Run, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.db.Runs {
		if r.ID == id {
			return detachRun(r), true
		}
	}
	return model.Run{}, false
}

// ProgressRun records the live context pulse of a running run without flushing
// to disk: one write per model call would make the ledger the bottleneck, and
// the settled run is written by finish (the same contract as NoteEvent).
func (s *Store) ProgressRun(id string, contextTokens int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.db.Runs {
		if s.db.Runs[i].ID != id {
			continue
		}
		if contextTokens > s.db.Runs[i].ContextTokens {
			s.db.Runs[i].ContextTokens = contextTokens
		}
		return
	}
}

// ProgressWindow records the harness-resolved context window while the run is
// live, without flushing: the window is constant for the run, so the first
// report is the useful one, and the settled run is written by finish. The
// console pressure bar needs it before the run ends (D80).
func (s *Store) ProgressWindow(id string, window int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.db.Runs {
		if s.db.Runs[i].ID != id {
			continue
		}
		s.db.Runs[i].ContextWindow = window
		return
	}
}

func (s *Store) Runs() []model.Run {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Run, len(s.db.Runs))
	copy(out, s.db.Runs)
	return out
}

func (s *Store) Evidence() []model.Evidence {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Evidence, len(s.db.Evidence))
	copy(out, s.db.Evidence)
	return out
}

// ---- goals ----

// AddGoal appends one goal. It is the plain create path the operator console
// uses (the agent surface cannot create goals); the explicit "append a goal and
// continue" path uses AddGoalIdempotent so a retried request finds the goal it
// already created instead of appending a second one.
func (s *Store) AddGoal(g model.Goal) (model.Goal, error) {
	out, _, err := s.AddGoalIdempotent(g)
	return out, err
}

func (s *Store) Goals() []model.Goal {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Goal, len(s.db.Goals))
	for i, g := range s.db.Goals {
		out[i] = markGoalReceipt(detachGoal(g), s.db.Facts)
	}
	return out
}

// PinGoal marks a goal met. It refuses to do so without an evidence row that
// exists and a written reason: "盖章必须有实测" is enforced here, not in the
// prompt, and a stamp nobody can explain is not a decision the page can show.
// from is the cited fact list; it may be empty for an operator's manual pin
// (the console then shows the evidence row instead).
func (s *Store) PinGoal(id, evidenceID, note string, from []string, policies ...string) (model.Goal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	evidenceID = strings.TrimSpace(evidenceID)
	if evidenceID == "" {
		return model.Goal{}, fmt.Errorf("盖章必须挂证据：evidence_id 不能为空")
	}
	note = strings.TrimSpace(note)
	if note == "" {
		return model.Goal{}, fmt.Errorf("盖章必须写理由：说清这些证据为什么足以证明目标达成")
	}
	from = cleanIDs(from)
	policy := model.PremiseVerifiedOnly
	if len(policies) > 0 {
		policy = policies[0]
	}
	if !model.ValidPremisePolicy(policy) {
		return model.Goal{}, fmt.Errorf("unknown premise policy %q", policy)
	}
	if policy == model.PremiseVerifiedOnly && len(from) == 0 {
		return model.Goal{}, fmt.Errorf("本模式完成目标必须引用已独立核验的事实")
	}
	claims := make(map[string]uint64, len(from))
	knownEvidence := map[string]bool{}
	for _, e := range s.db.Evidence {
		knownEvidence[e.ID] = true
	}
	associated := false
	for _, fid := range from {
		r, ok := s.records[recordKey("fact", fid)]
		if !ok {
			return model.Goal{}, fmt.Errorf("引用事实 %s 不存在", fid)
		}
		f := r.value.(model.Fact)
		if f.Status == "rejected" || len(f.EvidenceIDs) == 0 {
			return model.Goal{}, fmt.Errorf("引用事实 %s 已否定或没有证据，不能支持目标达成", fid)
		}
		available := false
		for _, eid := range f.EvidenceIDs {
			if knownEvidence[eid] {
				available = true
			}
			if eid == evidenceID {
				associated = true
			}
		}
		if !available {
			return model.Goal{}, fmt.Errorf("事实 %s 引用的证据均不存在", fid)
		}
		if policy == model.PremiseVerifiedOnly {
			if issue := model.PremiseIssue(f, policy); issue != "" {
				return model.Goal{}, fmt.Errorf("事实 %s：%s", fid, issue)
			}
			if _, ok := model.CurrentProof(f, s.db.Reviews); !ok {
				return model.Goal{}, fmt.Errorf("事实 %s 缺少当前断言的独立核验收据", fid)
			}
		}
		claims[fid] = f.ClaimVersion
	}
	if len(from) > 0 && !associated {
		return model.Goal{}, fmt.Errorf("所选证据不属于引用事实，不能写入完成收据")
	}
	found := false
	for _, e := range s.db.Evidence {
		if e.ID == evidenceID {
			found = true
			break
		}
	}
	if !found {
		return model.Goal{}, fmt.Errorf("evidence %s 不存在：盖章必须挂真实证据", evidenceID)
	}
	for i := range s.db.Goals {
		if s.db.Goals[i].ID != id {
			continue
		}
		if s.db.Goals[i].Met {
			return model.Goal{}, fmt.Errorf("目标 %s 已经达成", id)
		}
		now := time.Now()
		s.db.Goals[i].Met = true
		s.db.Goals[i].EvidenceID = evidenceID
		s.db.Goals[i].Note = note
		s.db.Goals[i].From = from
		s.db.Goals[i].FromClaimVersions = claims
		s.db.Goals[i].CompletedAt = &now
		s.db.Goals[i].CompletionPolicy = policy
		return markGoalReceipt(detachGoal(s.db.Goals[i]), s.db.Facts), s.flush("goal")
	}
	return model.Goal{}, fmt.Errorf("goal %s not found", id)
}

// ReopenGoal withdraws a stamp. Since D97 the judgement belongs to the
// commander, and a judgement can be wrong; until this existed there was no way
// back — the only workaround was to add another goal, which hid the mistake
// instead of correcting it (D98). The receipt is cleared with the stamp:
// note/from/completed_at describe a decision that no longer stands, and a
// reason sitting next to "未达成" would read as if the goal were still met. The
// round that made the stamp still names it in that round's decisions.jsonl.
//
// Only a met goal can be reopened: an open goal has no stamp to withdraw.
func (s *Store) ReopenGoal(id string) (model.Goal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.db.Goals {
		if s.db.Goals[i].ID != id {
			continue
		}
		if !s.db.Goals[i].Met {
			return model.Goal{}, fmt.Errorf("目标 %s 没有盖章，无需撤销", id)
		}
		s.db.Goals[i].Met = false
		s.db.Goals[i].EvidenceID = ""
		s.db.Goals[i].Note = ""
		s.db.Goals[i].From = nil
		s.db.Goals[i].FromClaimVersions = nil
		s.db.Goals[i].CompletedAt = nil
		s.db.Goals[i].CompletionPolicy = ""
		s.db.Goals[i].ReceiptStale, s.db.Goals[i].ReceiptChanged = false, nil
		return detachGoal(s.db.Goals[i]), s.flush("goal")
	}
	return model.Goal{}, fmt.Errorf("goal %s not found", id)
}

// cleanIDs trims, drops empties and de-duplicates while keeping the caller's
// order: the citation list is a receipt, so it must not silently reorder.
func cleanIDs(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// ---- reviews ----

// ReviewFact records one verifier verdict and moves the fact's status with it.
// A verdict nobody can reproduce must not leave the fact looking proven, so
// "needs_evidence" keeps it unverified rather than leaving the old status.
//
// A non-zero ClaimVersion binds the verdict to the assertion the verifier
// actually proved: an asynchronous verifier must carry the version it started
// with, and a fact changed meanwhile rejects the stale verdict
// (ErrReviewConflict) instead of letting an old proof speak for new content. A
// zero version is the synchronous human verdict: it is recorded under this
// lock, so "the assertion I judged" is exactly the row being changed.
func (s *Store) ReviewFact(r model.Review) (model.Review, error) {
	s.mu.Lock()

	var status string
	switch r.Verdict {
	case "pass":
		status = "verified"
	case "reject":
		status = "rejected"
	case "needs_evidence":
		status = "unverified"
	default:
		s.mu.Unlock()
		return model.Review{}, fmt.Errorf("verdict 不合法：%q", r.Verdict)
	}

	idx := -1
	for i := range s.db.Facts {
		if s.db.Facts[i].ID == r.FactID {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.mu.Unlock()
		return model.Review{}, fmt.Errorf("fact %s not found", r.FactID)
	}
	fact := &s.db.Facts[idx]
	if r.ClaimVersion != 0 && (r.ClaimVersion != fact.ClaimVersion || r.ClaimHash != "" && r.ClaimHash != fact.ClaimHash) {
		s.mu.Unlock()
		return model.Review{}, fmt.Errorf("%w（判定 v%d，当前 v%d）", ErrReviewConflict, r.ClaimVersion, fact.ClaimVersion)
	}
	r.ClaimVersion, r.ClaimHash = fact.ClaimVersion, fact.ClaimHash
	r.ID = NewID("rv")
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	fact.Status = status
	s.db.Reviews = append(s.db.Reviews, r)
	err := s.flush("fact", "review")
	hook := s.onFact
	f := *fact
	uris := s.evidenceURIsLocked(f.EvidenceIDs)
	s.mu.Unlock()

	// The mirror carries the verdict: an unverified finding that passes review
	// must stop looking provisional in the library.
	if err == nil && hook != nil {
		hook(f, uris)
	}
	if err == nil {
		s.notifyFactChange()
	}
	return r, err
}

func (s *Store) Reviews(factID string) []model.Review {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Review, 0, len(s.db.Reviews))
	for _, r := range s.db.Reviews {
		if factID == "" || r.FactID == factID {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// ---- playbooks ----

// playbookKey is the duplicate guard: case and surrounding whitespace only.
// Folding punctuation away made different tactics collide, the same mistake the
// intent title guard avoids (D21, D54).
func playbookKey(title string) string {
	return strings.ToLower(strings.TrimSpace(title))
}

func (s *Store) playbookIndexLocked(title string) int {
	key := playbookKey(title)
	if key == "" {
		return -1
	}
	for i := range s.db.Playbooks {
		if playbookKey(s.db.Playbooks[i].Title) == key {
			return i
		}
	}
	return -1
}

// Playbooks returns the whole table, most recently touched first. It is a copy:
// callers sort and filter their own view.
func (s *Store) Playbooks() []model.Playbook {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Playbook, len(s.db.Playbooks))
	copy(out, s.db.Playbooks)
	for i := range out {
		out[i] = detachPlaybook(out[i])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}

func (s *Store) PlaybookByID(id string) (model.Playbook, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.db.Playbooks {
		if p.ID == id {
			return detachPlaybook(p), true
		}
	}
	return model.Playbook{}, false
}

// UpsertPlaybook writes one entry, deduplicated by title. It returns the stored
// row and whether the write changed anything (false means a confirmed or
// retired row already existed and was left alone).
//
// The two writers are not equal. An executor declaration is a live observation
// of the tactic and always wins: it overwrites content, promotes a candidate,
// revives a retired row and confirms the entry. A commander nomination may only
// create or refine a candidate; it can never rewrite a row an executor
// confirmed, because the commander never saw the bytes (D70).
func (s *Store) UpsertPlaybook(p model.Playbook) (model.Playbook, bool, error) {
	p.Title = strings.TrimSpace(p.Title)
	p.When = strings.TrimSpace(p.When)
	p.Doc = strings.TrimSpace(p.Doc)
	p.Tags = cleanTags(p.Tags)
	p.RequiredTags = cleanRequirements(p.RequiredTags)
	if err := ValidatePattern(p.Pattern); err != nil {
		return model.Playbook{}, false, err
	}
	p = detachPlaybook(p)
	if p.Title == "" {
		return model.Playbook{}, false, fmt.Errorf("playbook 需要 title")
	}
	if p.Outcome != model.PlaybookDeadEnd {
		p.Outcome = model.PlaybookSuccess
	}
	if p.Origin != model.PlaybookFromExecutor {
		p.Origin = model.PlaybookFromCommander
	}
	if p.Status == "" {
		if p.Origin == model.PlaybookFromExecutor {
			p.Status = model.PlaybookConfirmed
		} else {
			p.Status = model.PlaybookCandidate
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	idx := s.playbookIndexLocked(p.Title)
	if idx < 0 {
		p.FromFactVersions = map[string]uint64{}
		for _, id := range p.FromFactIDs {
			if r, ok := s.records[recordKey("fact", id)]; ok {
				p.FromFactVersions[id] = r.version
			}
		}
		p.ID = NewID("pb")
		p.CreatedAt, p.UpdatedAt = now, now
		s.db.Playbooks = append(s.db.Playbooks, p)
		return detachPlaybook(p), true, s.flush("playbook")
	}

	cur := &s.db.Playbooks[idx]
	p.ID = cur.ID
	switch {
	case p.Origin == model.PlaybookFromExecutor:
		if p.Pattern != nil {
			cur.Pattern = p.Pattern
		}
		cur.SourceRunID = p.SourceRunID
		if p.RequiredTags != nil {
			cur.RequiredTags = append([]string(nil), p.RequiredTags...)
		}
		if p.When != "" {
			cur.When = p.When
		}
		if p.Doc != "" {
			cur.Doc = p.Doc
		}
		cur.Outcome = p.Outcome
		cur.Status = model.PlaybookConfirmed
		cur.Origin = model.PlaybookFromExecutor
		cur.IntentID = p.IntentID
		cur.Tags = mergeTags(cur.Tags, p.Tags)
		cur.FromFactIDs = mergeIDs(cur.FromFactIDs, p.FromFactIDs)
		cur.UpdatedAt = now
	case cur.Status == model.PlaybookCandidate:
		if p.Pattern != nil {
			cur.Pattern = p.Pattern
		}
		if p.RequiredTags != nil {
			cur.RequiredTags = append([]string(nil), p.RequiredTags...)
		}
		// The latest nomination wins: candidates are proposals, not decisions.
		if p.When != "" {
			cur.When = p.When
		}
		if p.Doc != "" {
			cur.Doc = p.Doc
		}
		cur.Outcome = p.Outcome
		cur.Tags = mergeTags(cur.Tags, p.Tags)
		cur.FromFactIDs = mergeIDs(cur.FromFactIDs, p.FromFactIDs)
		cur.UpdatedAt = now
	default:
		// A confirmed or retired entry is not the commander's to edit.
		return detachPlaybook(*cur), false, nil
	}
	if cur.FromFactVersions == nil {
		cur.FromFactVersions = map[string]uint64{}
	}
	for _, id := range p.FromFactIDs {
		if r, ok := s.records[recordKey("fact", id)]; ok {
			cur.FromFactVersions[id] = r.version
		}
	}
	return detachPlaybook(*cur), true, s.flush("playbook")
}

// RetirePlaybook drops an entry out of recall without deleting its text. A
// wrong tactic is still a lesson: keeping it retired is friendlier than making
// the same mistake again to relearn it.
func (s *Store) RetirePlaybook(id string) (model.Playbook, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.db.Playbooks {
		if s.db.Playbooks[i].ID != id {
			continue
		}
		s.db.Playbooks[i].Status = model.PlaybookRetired
		s.db.Playbooks[i].UpdatedAt = time.Now()
		return detachPlaybook(s.db.Playbooks[i]), s.flush("playbook")
	}
	return model.Playbook{}, fmt.Errorf("playbook %s not found", id)
}

var ErrPlaybookChanged = errors.New("战场知识已更新，请刷新后再操作")

// DeletePlaybook is explicit operator curation. It never removes source facts
// or independently published world knowledge, and does not wake a model.
func (s *Store) DeletePlaybook(id string, updatedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, p := range s.db.Playbooks {
		if p.ID != id {
			continue
		}
		if !p.UpdatedAt.Equal(updatedAt) {
			return ErrPlaybookChanged
		}
		s.db.Playbooks = append(s.db.Playbooks[:i], s.db.Playbooks[i+1:]...)
		return s.flushNoRev("playbook")
	}
	return os.ErrNotExist
}

func cleanTags(in []string) []string {
	out := make([]string, 0, len(in))
	for _, t := range in {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		out = append(out, t)
		if len(out) >= 8 {
			break
		}
	}
	return out
}

func mergeTags(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, list := range [][]string{a, b} {
		for _, t := range list {
			t = strings.TrimSpace(t)
			if t == "" || seen[strings.ToLower(t)] {
				continue
			}
			seen[strings.ToLower(t)] = true
			out = append(out, t)
			if len(out) >= 8 {
				return out
			}
		}
	}
	return out
}

func mergeIDs(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, list := range [][]string{a, b} {
		for _, id := range list {
			id = strings.TrimSpace(id)
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// ---- point reads ----

func (s *Store) FactByID(id string) (model.Fact, bool) {
	f, _, ok := s.FactVersion(id)
	return f, ok
}

func (s *Store) FactVersion(id string) (model.Fact, uint64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.records[recordKey("fact", id)]
	if !ok {
		return model.Fact{}, 0, false
	}
	f, ok := r.value.(model.Fact)
	return detachFact(f), r.version, ok
}

func (s *Store) EvidenceByID(id string) (model.Evidence, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.db.Evidence {
		if e.ID == id {
			return e, true
		}
	}
	return model.Evidence{}, false
}

// SearchFacts is the "depth by query" path: the board never carries the full
// text, so looking something up is an explicit act with an explicit limit.
func (s *Store) SearchFacts(q, kind string, limit int) []model.Fact {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	q = strings.ToLower(strings.TrimSpace(q))
	out := make([]model.Fact, 0, limit)
	for _, f := range s.db.Facts {
		if kind != "" && f.Kind != kind {
			continue
		}
		if q != "" &&
			!strings.Contains(strings.ToLower(f.Title), q) &&
			!strings.Contains(strings.ToLower(f.Summary), q) &&
			!strings.Contains(strings.ToLower(f.Doc), q) {
			continue
		}
		out = append(out, f)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Snapshot returns a detached copy of the ledger. It must be a copy, not a
// slice of the live DB: writers mutate rows in place under the lock
// (UpdateFact, UpdateIntent, UpdateRun, RetirePlaybook), so handing the live
// slices to a caller that reads them without the lock is a data race and a
// torn read — and every caller here (board, prompt, report) does exactly that.
func (s *Store) Snapshot() DB {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := s.db.clone()
	out.ReadRevision = s.rev
	if s.db.Memory != nil {
		out.ContentHash = s.db.Memory.ContentHash
	} else {
		out.ContentHash = contentHash(s.records)
	}
	return out
}

// clone copies the top-level slices. Row values never share mutable state with
// the live DB: writers replace rows wholesale (a new Tags/EvidenceIDs slice, a
// new *time.Time), they do not edit the slices inside a row.
func (db DB) clone() DB {
	out := db
	out.Routes = append([]model.RouteCheckpoint(nil), db.Routes...)
	out.Instructions = append([]model.Instruction(nil), db.Instructions...)
	out.Actions = append([]model.ExternalAction(nil), db.Actions...)
	out.Intents = append([]model.Intent(nil), db.Intents...)
	for i := range out.Intents {
		out.Intents[i] = detachIntent(out.Intents[i])
	}
	out.Facts = append([]model.Fact(nil), db.Facts...)
	for i := range out.Facts {
		out.Facts[i] = detachFact(out.Facts[i])
	}
	out.Evidence = append([]model.Evidence(nil), db.Evidence...)
	out.Goals = append([]model.Goal(nil), db.Goals...)
	for i := range out.Goals {
		// The snapshot is read by the board and the graph: they need the same
		// "the cited claim has moved" signal the goals API carries, and it is
		// computed on the copy so the stored stamp is never rewritten.
		out.Goals[i] = markGoalReceipt(detachGoal(out.Goals[i]), out.Facts)
	}
	out.Reviews = append([]model.Review(nil), db.Reviews...)
	out.Runs = append([]model.Run(nil), db.Runs...)
	for i := range out.Runs {
		out.Runs[i] = detachRun(out.Runs[i])
	}
	out.Playbooks = append([]model.Playbook(nil), db.Playbooks...)
	for i := range out.Playbooks {
		out.Playbooks[i] = detachPlaybook(out.Playbooks[i])
	}
	out.Messages = append([]model.Message(nil), db.Messages...)
	return out
}

// Path is where the ledger file lives. The bundle layer needs it to carry the
// ledger inside the archive.
func (s *Store) Path() string { return s.path }

// Revision is a monotonic count of durable writes to this ledger. It is not
// persisted: a restart starts at zero and the first board after it therefore
// looks changed, which is exactly right — no round has seen the reloaded world.
// The board puts it in the decider digest (D79); everything else ignores it.
func (s *Store) Revision() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rev
}
