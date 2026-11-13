package store

// The journal is part of the ledger transaction. It is also the version store
// for snapshot pagination: a reader never walks a moving, truncated list.
import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"time"
)

type Change struct {
	Seq         uint64          `json:"seq"`
	Transaction string          `json:"transaction"`
	Kind        string          `json:"kind"`
	ID          string          `json:"id"`
	Version     uint64          `json:"version"`
	Operation   string          `json:"operation"`
	Fields      []string        `json:"fields,omitempty"`
	At          time.Time       `json:"at"`
	Position    int             `json:"position"`
	Value       json.RawMessage `json:"value,omitempty"`
}

type Checkpoint struct {
	ChangesPending bool   `json:"changes_pending,omitempty"`
	ScanComplete   bool   `json:"scan_complete,omitempty"`
	SurveyHash     string `json:"survey_hash,omitempty"`
	Changes        string `json:"changes,omitempty"`
	Scan           string `json:"scan,omitempty"`
}

type Memory struct {
	Deliveries  map[string]Delivery   `json:"deliveries,omitempty"`
	Epoch       string                `json:"epoch"`
	Seq         uint64                `json:"seq"`
	ContentHash string                `json:"content_hash"`
	History     []Change              `json:"history,omitempty"`
	Checkpoints map[string]Checkpoint `json:"checkpoints,omitempty"`
	// ReviewCursors remembers, per intent, the candidate ID the automatic review
	// walk resumes at (F11). It is queue bookkeeping, not delivery state: keeping
	// it out of Checkpoints means a rewound delivery cannot move the review
	// queue, and SetReviewCursor persists it through the same meta transaction
	// without bumping the decider's change counter.
	ReviewCursors map[string]string `json:"review_cursors,omitempty"`
	// AttentionThrough is a delivery checkpoint for global decision events, not
	// a fact/read ACK. Local events remain in History after routing past them.
	AttentionThrough uint64 `json:"attention_through,omitempty"`
	TaskRouteThrough uint64 `json:"task_route_through,omitempty"`
	// Deferred journal identities survive backpressure independently of the
	// scan cursor, so one full recipient cannot stop other task results.
	TaskRoutePending []uint64 `json:"task_route_pending,omitempty"`
	// Native mailbox contents remain in harness; only its scheduling signal lives here.
	NativeMailID      string `json:"native_mail_id,omitempty"`
	NativeMailSeq     uint64 `json:"native_mail_seq,omitempty"`
	NativeMailThrough uint64 `json:"native_mail_through,omitempty"`
	// One sequence per observer fences callback retries, even when different
	// native hosts interleave mail or a crash delays one observer's receipt.
	NativeMailCursors map[string]int64 `json:"native_mail_cursors,omitempty"`
}

type record struct {
	kind, id     string
	position     int
	version, seq uint64
	value        any
	raw          json.RawMessage
}

func newsKind(kind string) bool {
	return kind == "instruction" || kind == "fact" || kind == "intent" || kind == "evidence" || kind == "goal" || kind == "review" || kind == "playbook"
}
func reportKind(kind string) bool {
	return newsKind(kind) && kind != "playbook" && kind != "instruction"
}

func visitRows(db *DB, visit func(string, string, int, any)) {
	visitKinds(db, nil, visit)
}

func visitKinds(db *DB, kinds map[string]bool, visit func(string, string, int, any)) {
	allowed := func(kind string) bool { return len(kinds) == 0 || kinds[kind] }
	if allowed("route") {
		for i, v := range db.Routes {
			visit("route", v.ID, i, v)
		}
	}
	if allowed("instruction") {
		for i, v := range db.Instructions {
			visit("instruction", v.ID, i, v)
		}
	}
	if allowed("intent") {
		for i, v := range db.Intents {
			visit("intent", v.ID, i, v)
		}
	}
	if allowed("fact") {
		for i, v := range db.Facts {
			visit("fact", v.ID, i, v)
		}
	}
	if allowed("evidence") {
		for i, v := range db.Evidence {
			visit("evidence", v.ID, i, v)
		}
	}
	if allowed("goal") {
		for i, v := range db.Goals {
			visit("goal", v.ID, i, v)
		}
	}
	if allowed("review") {
		for i, v := range db.Reviews {
			visit("review", v.ID, i, v)
		}
	}
	if allowed("run") {
		for i, v := range db.Runs {
			visit("run", v.ID, i, v)
		}
	}
	if allowed("playbook") {
		for i, v := range db.Playbooks {
			visit("playbook", v.ID, i, v)
		}
	}
	if allowed("message") {
		for i, v := range db.Messages {
			visit("message", v.ID, i, v)
		}
	}
	if allowed("action") {
		for i, v := range db.Actions {
			visit("action", v.ID, i, v)
		}
	}
}

func fieldsChanged(before, after json.RawMessage) []string {
	a, b := map[string]json.RawMessage{}, map[string]json.RawMessage{}
	_ = json.Unmarshal(before, &a)
	_ = json.Unmarshal(after, &b)
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	out := []string{}
	for k := range keys {
		if !bytes.Equal(a[k], b[k]) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func recordKey(kind, id string) string { return kind + ":" + id }

// Reflection compares small row headers and unchanged string/slice values;
// only changed rows are serialized and written to SQLite.
func (s *Store) initRecords() {
	s.records = map[string]record{}
	s.recordKinds = map[string]map[string]record{}
	versions := map[string]Change{}
	if s.db.Memory != nil {
		for _, c := range s.db.Memory.History {
			versions[recordKey(c.Kind, c.ID)] = c
		}
	}
	visitRows(&s.db, func(kind, id string, pos int, value any) {
		raw, _ := json.Marshal(value)
		c := versions[recordKey(kind, id)]
		owned := reflect.New(reflect.TypeOf(value))
		_ = json.Unmarshal(raw, owned.Interface())
		r := record{kind, id, pos, c.Version, c.Seq, owned.Elem().Interface(), raw}
		s.putRecord(r)
	})
}

func (s *Store) putRecord(r record) {
	key := recordKey(r.kind, r.id)
	s.records[key] = r
	if s.recordKinds[r.kind] == nil {
		s.recordKinds[r.kind] = map[string]record{}
	}
	s.recordKinds[r.kind][key] = r
}

func rowHash(r record) [32]byte {
	return sha256.Sum256(append([]byte(recordKey(r.kind, r.id)+"\x00"), r.raw...))
}

func contentHash(records map[string]record) string {
	var sum [32]byte
	for _, r := range records {
		if reportKind(r.kind) {
			h := rowHash(r)
			for i := range sum {
				sum[i] ^= h[i]
			}
		}
	}
	return hex.EncodeToString(sum[:])
}

func (s *Store) prepareMemory(selected ...string) (map[string]record, []Change) {
	mem := Memory{Epoch: s.readEpoch, Checkpoints: map[string]Checkpoint{}}
	if s.db.Memory != nil {
		mem = *s.db.Memory
	}
	kinds := map[string]bool{}
	for _, kind := range selected {
		kinds[kind] = true
	}
	next := map[string]record{}
	seen := map[string]bool{}
	var sum [32]byte
	base := mem.ContentHash
	if base == "" {
		base = contentHash(s.records)
	}
	rawSum, _ := hex.DecodeString(base)
	copy(sum[:], rawSum)
	toggle := func(r record) {
		if !reportKind(r.kind) {
			return
		}
		h := rowHash(r)
		for i := range sum {
			sum[i] ^= h[i]
		}
	}
	changes := []Change{}
	txn := NewID("tx")
	now := time.Now().UTC()
	add := func(r record, op string, fields []string) record {
		mem.Seq++
		r.seq = mem.Seq
		r.version++
		changes = append(changes, Change{Seq: mem.Seq, Transaction: txn, Kind: r.kind, ID: r.id, Version: r.version, Operation: op, Fields: fields, At: now, Position: r.position, Value: r.raw})
		return r
	}
	// Existing rows get a baseline on the first explicit write, never on read.
	if s.db.Memory == nil || (len(mem.History) == 0 && len(s.records) > 0 && mem.Seq == 0) {
		keys := make([]string, 0, len(s.records))
		for k := range s.records {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			r := s.records[k]
			r = add(r, "baseline", nil)
			next[k] = r
		}
	}
	visitKinds(&s.db, kinds, func(kind, id string, pos int, value any) {
		key := recordKey(kind, id)
		seen[key] = true
		old, ok := s.records[key]
		if baseline, exists := next[key]; exists {
			old, ok = baseline, true
		}
		if ok && equalValue(reflect.ValueOf(old.value), reflect.ValueOf(value)) {
			if old.position != pos {
				changes = append(changes, Change{Kind: kind, ID: id, Version: old.version, Seq: old.seq, Operation: "move", Position: pos, Value: old.raw})
			}
			if old.position != pos {
				old.position = pos
				next[key] = old
			}
			return
		}
		raw, _ := json.Marshal(value)
		// Keep an owned typed copy: mutable slices must not alter the baseline.
		owned := reflect.New(reflect.TypeOf(value))
		_ = json.Unmarshal(raw, owned.Interface())
		r := record{kind, id, pos, old.version, old.seq, owned.Elem().Interface(), raw}
		op := "update"
		if !ok {
			op = "create"
		}
		if ok {
			toggle(old)
		}
		toggle(r)
		next[key] = add(r, op, fieldsChanged(old.raw, raw))
	})
	deleted := []string{}
	for kind, records := range s.recordKinds {
		if len(kinds) > 0 && !kinds[kind] {
			continue
		}
		for key := range records {
			if !seen[key] {
				deleted = append(deleted, key)
			}
		}
	}
	sort.Strings(deleted)
	for _, key := range deleted {
		r := s.records[key]
		toggle(r)
		r.raw = nil
		add(r, "delete", nil)
	}
	mem.ContentHash = hex.EncodeToString(sum[:])
	for _, c := range changes {
		if newsKind(c.Kind) && c.Operation != "move" {
			mem.History = append(mem.History, c)
		}
	}
	s.db.Memory = &mem
	return next, changes
}

// Time's monotonic clock and location are process details, not content
// revisions. Empty optional collections likewise survive JSON round trips.
func equalValue(a, b reflect.Value) bool {
	if !a.IsValid() || !b.IsValid() {
		return a.IsValid() == b.IsValid()
	}
	if a.Type() != b.Type() {
		return false
	}
	if a.Type() == reflect.TypeOf(time.Time{}) {
		return a.Interface().(time.Time).Equal(b.Interface().(time.Time))
	}
	switch a.Kind() {
	case reflect.Struct:
		for i := 0; i < a.NumField(); i++ {
			if !equalValue(a.Field(i), b.Field(i)) {
				return false
			}
		}
		return true
	case reflect.Slice, reflect.Array:
		if a.Len() != b.Len() {
			return false
		}
		for i := 0; i < a.Len(); i++ {
			if !equalValue(a.Index(i), b.Index(i)) {
				return false
			}
		}
		return true
	case reflect.Map:
		if a.Len() != b.Len() {
			return false
		}
		it := a.MapRange()
		for it.Next() {
			v := b.MapIndex(it.Key())
			if !v.IsValid() || !equalValue(it.Value(), v) {
				return false
			}
		}
		return true
	case reflect.Pointer, reflect.Interface:
		if a.IsNil() || b.IsNil() {
			return a.IsNil() == b.IsNil()
		}
		return equalValue(a.Elem(), b.Elem())
	default:
		return reflect.DeepEqual(a.Interface(), b.Interface())
	}
}

func (s *Store) ContentHash() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db.Memory != nil {
		return s.db.Memory.ContentHash
	}
	return contentHash(s.records)
}

func (s *Store) History(kind, id string) []Change {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []Change{}
	if s.db.Memory != nil {
		for _, c := range s.db.Memory.History {
			if c.Kind == kind && c.ID == id {
				c.Value = append(json.RawMessage(nil), c.Value...)
				c.Fields = append([]string(nil), c.Fields...)
				out = append(out, c)
			}
		}
	}
	return out
}

type ReadVersion struct {
	Epoch string `json:"epoch"`
	Seq   uint64 `json:"seq"`
}

func (s *Store) ReadView() (DB, ReadVersion) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v := ReadVersion{Epoch: s.readEpoch}

	if s.db.Memory != nil {
		v = ReadVersion{s.db.Memory.Epoch, s.db.Memory.Seq}
	}
	db := s.db.clone()
	db.Memory = nil
	return db, v
}

func (s *Store) ReadAt(v ReadVersion) (DB, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	epoch := s.readEpoch
	seq := uint64(0)

	if s.db.Memory != nil {
		epoch = s.db.Memory.Epoch
		seq = s.db.Memory.Seq
	}
	if v.Epoch != epoch || v.Seq > seq {
		return DB{}, fmt.Errorf("cursor_expired: ledger identity or watermark changed")
	}
	if v.Seq == seq {
		db := s.db.clone()
		db.Memory = nil
		return db, nil
	}
	// Run/message bookkeeping advances the durable sequence but does not change
	// any object that participates in content pagination.
	history := s.db.Memory.History
	effective := sort.Search(len(history), func(i int) bool { return history[i].Seq > v.Seq }) - 1
	if effective == len(history)-1 {
		db := s.db.clone()
		db.Memory, db.Runs, db.Messages, db.Actions = nil, nil, nil, nil
		return db, nil
	}
	contentSeq := uint64(0)
	if effective >= 0 {
		contentSeq = history[effective].Seq
	}
	if cached, ok := s.cachedView(contentSeq); ok {
		return cached, nil
	}

	rows := map[string]Change{}
	if s.db.Memory != nil {
		for _, c := range s.db.Memory.History {
			if c.Seq <= v.Seq || c.Operation == "baseline" {
				rows[recordKey(c.Kind, c.ID)] = c
			}
		}
	}
	db := DB{Version: s.db.Version}
	bytes := 0
	keys := make([]string, 0, len(rows))
	for key := range rows {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := rows[keys[i]], rows[keys[j]]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Position < b.Position
	})
	for _, key := range keys {
		c := rows[key]
		if c.Operation == "delete" {
			continue
		}
		if err := appendRow(&db, c.Kind, c.Value); err != nil {
			return DB{}, err
		}
		bytes += len(c.Value) + 512
	}
	s.cacheView(contentSeq, db, bytes)
	return db.clone(), nil
}

func (s *Store) ReadCheckpoint(name string) Checkpoint {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db.Memory == nil {
		return Checkpoint{}
	}
	return s.db.Memory.Checkpoints[name]
}
