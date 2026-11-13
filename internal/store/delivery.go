package store

import (
	"encoding/json"
	"fmt"
	"time"
)

// A pending context delivery is replayed verbatim until the model acknowledges
// it. Replay is not retry: without a schedule, a model that never calls
// context/ack makes the heartbeat buy a full global round forever (288 a day),
// and the logs still look healthy. The delivery therefore records how many
// times it was shown and when it may be shown again; the heartbeat consults
// that schedule instead of the bare existence of the delivery.
const (
	// DeliveryRetryBase is the delay after the first unacknowledged showing:
	// one heartbeat pays the first retry, then each further attempt doubles.
	DeliveryRetryBase = 5 * time.Minute
	// DeliveryRetryMax caps the backoff so an unacknowledged delivery is still
	// eventually re-presented, not abandoned.
	DeliveryRetryMax = time.Hour
	// DeliveryPointerAfter is how many full showings a delivery may take before
	// the round gets its identity and a change summary instead of the same page
	// again. The model must still read the context through the API and ACK; only
	// the repeated payload degrades, never the acknowledgement requirement.
	DeliveryPointerAfter = 3
)

// DeliveryRetryDelay is the backoff before the attempt-th presentation may be
// repeated. attempts is the number of showings already made, so the first retry
// waits DeliveryRetryBase.
func DeliveryRetryDelay(attempts int) time.Duration {
	if attempts <= 0 {
		return 0
	}
	d := DeliveryRetryBase
	for i := 1; i < attempts; i++ {
		d *= 2
		if d >= DeliveryRetryMax {
			return DeliveryRetryMax
		}
	}
	return d
}

type Delivery struct {
	ID               string          `json:"id"`
	Expected         Checkpoint      `json:"expected"`
	Next             Checkpoint      `json:"next"`
	Payload          json.RawMessage `json:"payload"`
	AttentionThrough uint64          `json:"attention_through,omitempty"`
	NativeThrough    uint64          `json:"native_through,omitempty"`
	// Attempts counts the rounds that were handed this delivery without it
	// being acknowledged. NextRetry is the earliest time the heartbeat may
	// present it again; a zero NextRetry on memory written before this field
	// existed means "due now", never "never".
	Attempts     int       `json:"attempts,omitempty"`
	NextRetry    time.Time `json:"next_retry,omitempty"`
	LastShownRun string    `json:"last_shown_run,omitempty"`
}

// NoteDeliveryShown records one round presenting a pending delivery and
// schedules the next retry. It is presentation bookkeeping, not a read: GET
// /api/context does not call it, so inspecting the context never moves the
// schedule. The pending payload is unchanged: the same delivery identity is
// replayed until ACK.
func (s *Store) NoteDeliveryShown(name, id string, at time.Time) (Delivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db.Memory == nil {
		return Delivery{}, fmt.Errorf("delivery_not_found")
	}
	d, ok := s.db.Memory.Deliveries[name]
	if !ok {
		return Delivery{}, fmt.Errorf("delivery_not_found")
	}
	if d.ID != id {
		return Delivery{}, fmt.Errorf("delivery_conflict")
	}
	mem := *s.db.Memory
	mem.Deliveries = make(map[string]Delivery, len(s.db.Memory.Deliveries))
	for k, v := range s.db.Memory.Deliveries {
		mem.Deliveries[k] = v
	}
	d.Attempts++
	d.NextRetry = at.Add(DeliveryRetryDelay(d.Attempts))
	mem.Deliveries[name] = d
	s.db.Memory = &mem
	if err := s.flushNoRev("memory"); err != nil {
		return Delivery{}, err
	}
	return d, nil
}

// BindCommanderDeliveryRun records the actual purpose run before its executor
// starts. An ACK or replacement between preparation and launch must not bind an
// old prompt to a different delivery. This does not count a second attempt.
func (s *Store) BindCommanderDeliveryRun(id, runID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db.Memory == nil {
		return fmt.Errorf("delivery_not_found")
	}
	d, ok := s.db.Memory.Deliveries["commander"]
	if !ok || d.ID != id {
		return fmt.Errorf("delivery_conflict")
	}
	found := false
	for _, run := range s.db.Runs {
		if run.ID == runID && run.Purpose == "commander" && run.Status == "running" {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("commander_run_not_running")
	}
	if d.LastShownRun == runID {
		return nil
	}
	mem := *s.db.Memory
	mem.Deliveries = make(map[string]Delivery, len(s.db.Memory.Deliveries))
	for k, v := range s.db.Memory.Deliveries {
		mem.Deliveries[k] = v
	}
	d.LastShownRun = runID
	mem.Deliveries["commander"] = d
	s.db.Memory = &mem
	return s.flushNoRev("memory")
}

// deliveryDue reports whether a pending delivery's retry backoff has elapsed.
// A delivery that was never shown is due: it exists because no round has handed
// it to the model yet.
func deliveryDue(d Delivery, at time.Time) bool {
	return d.NextRetry.IsZero() || !at.Before(d.NextRetry)
}

// BindCommanderDelivery ties explicit context ACK to the global event references
// in that prompt. A failed transport does not consume them; a later explicit ACK
// can, without altering any fact/claim or asserting external execution.
func (s *Store) BindCommanderDelivery(id string, batch AttentionBatch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db.Memory == nil {
		return fmt.Errorf("delivery_not_found")
	}
	d, ok := s.db.Memory.Deliveries["commander"]
	if !ok || d.ID != id {
		return fmt.Errorf("delivery_conflict")
	}
	if batch.Through > s.db.Memory.Seq || batch.NativeThrough > s.db.Memory.NativeMailSeq {
		return fmt.Errorf("attention checkpoint ahead of ledger")
	}
	if d.AttentionThrough >= batch.Through && d.NativeThrough >= batch.NativeThrough {
		return nil
	}
	mem := *s.db.Memory
	mem.Deliveries = make(map[string]Delivery, len(s.db.Memory.Deliveries))
	for k, v := range s.db.Memory.Deliveries {
		mem.Deliveries[k] = v
	}
	d.AttentionThrough = max(d.AttentionThrough, batch.Through)
	d.NativeThrough = max(d.NativeThrough, batch.NativeThrough)
	mem.Deliveries["commander"] = d
	s.db.Memory = &mem
	return s.flushNoRev("memory")
}

func (s *Store) PendingDelivery(name string) (Delivery, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db.Memory == nil {
		return Delivery{}, false
	}
	v, ok := s.db.Memory.Deliveries[name]
	v.Payload = append(json.RawMessage(nil), v.Payload...)
	return v, ok
}
func (s *Store) SaveDelivery(name string, d Delivery) (Delivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db.Memory != nil {
		if old, ok := s.db.Memory.Deliveries[name]; ok {
			return old, nil
		}
		if s.db.Memory.Checkpoints[name] != d.Expected {
			return Delivery{}, fmt.Errorf("checkpoint_conflict")
		}
	}
	mem := Memory{Epoch: s.readEpoch, Checkpoints: map[string]Checkpoint{}}
	if s.db.Memory != nil {
		mem = *s.db.Memory
	}
	mem.Deliveries = map[string]Delivery{}
	if s.db.Memory != nil {
		for k, v := range s.db.Memory.Deliveries {
			mem.Deliveries[k] = v
		}
	}
	mem.Deliveries[name] = d
	s.db.Memory = &mem
	if err := s.flushNoRev("memory"); err != nil {
		return Delivery{}, err
	}
	return d, nil
}
func (s *Store) AcknowledgeDelivery(name, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db.Memory == nil {
		return fmt.Errorf("delivery_not_found")
	}
	d, ok := s.db.Memory.Deliveries[name]
	if !ok || d.ID != id {
		return fmt.Errorf("delivery_conflict: request current context again")
	}
	if s.db.Memory.Checkpoints[name] != d.Expected {
		return fmt.Errorf("checkpoint_conflict")
	}
	mem := *s.db.Memory
	mem.Checkpoints = map[string]Checkpoint{}
	for k, v := range s.db.Memory.Checkpoints {
		mem.Checkpoints[k] = v
	}
	mem.Deliveries = map[string]Delivery{}
	for k, v := range s.db.Memory.Deliveries {
		if k != name {
			mem.Deliveries[k] = v
		}
	}
	mem.Checkpoints[name] = d.Next
	if name == "commander" && d.AttentionThrough > mem.AttentionThrough {
		mem.AttentionThrough = d.AttentionThrough
	}
	if name == "commander" {
		mem.NativeMailThrough = max(mem.NativeMailThrough, d.NativeThrough)
	}
	s.db.Memory = &mem
	return s.flushNoRev("memory")
}
