package ioa

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// Record is one observed harness application record. Error is set when the
// metadata claimed to be protocol v1 but could not be parsed; the raw IOA
// identity and content are kept so the console can show it as rejected instead
// of silently dropping it.
type Record struct {
	Seq          int64          `json:"seq"`
	Envelope     *Envelope      `json:"envelope,omitempty"`
	IOAMessageID string         `json:"ioa_message_id"`
	Sender       string         `json:"sender,omitempty"`
	CreatedAt    string         `json:"created_at,omitempty"`
	Content      map[string]any `json:"content,omitempty"`
	ObservedAt   time.Time      `json:"observed_at"`
	Error        string         `json:"error,omitempty"`
	// Notify is persisted with the observation before scheduling is attempted.
	// Historical bootstrap/recovery rows leave it false and are never replayed.
	Notify     bool   `json:"notify,omitempty"`
	ObserverID string `json:"observer_id,omitempty"`
}

// Key is the application-level dedupe key. A stable envelope id survives
// transport retries; records without one fall back to the IOA message id.
func (r Record) Key() string {
	if r.Envelope != nil && r.Envelope.ID != "" {
		return r.Envelope.ID
	}
	return "ioa:" + r.IOAMessageID
}

// Observer tails one IOA space, parses v1 application records, dedupes them by
// envelope id and appends them to an append-only JSONL log. It is read-only:
// nothing is sent back to IOA and no harness state is touched.
type Observer struct {
	// OnRecord durably handles an incremental observation, set before Run.
	// Errors are retried, including after restart; consumers must be idempotent.
	// Historical bootstrap/recovery records never notify. The callback must not
	// call Observer methods; it runs under the ingestion lock.
	OnRecord func(Record) error
	client   *Client
	dir      string

	mu          sync.Mutex
	cursor      string
	seq         int64
	seen        map[string]bool
	initialized bool
	startedAt   time.Time
	observerID  string
	notified    int64
	pending     []Record
}

// NewObserver opens (or creates) the observer state under dir. Existing records
// are re-read to rebuild the dedupe set, so a restart cannot re-append an
// envelope whose transport id changed.
func NewObserver(client *Client, dir string) (*Observer, error) {
	if client == nil {
		return nil, errors.New("IOA observer needs a client")
	}
	if dir == "" {
		return nil, errors.New("IOA observer needs a state directory")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	o := &Observer{client: client, dir: dir, seen: make(map[string]bool), startedAt: time.Now().UTC(), observerID: fmt.Sprintf("%x", sha256.Sum256([]byte(abs)))}
	ack, err := os.ReadFile(o.notificationPath())
	if err == nil {
		o.notified, err = strconv.ParseInt(string(ack), 10, 64)
		if err != nil || o.notified < 0 {
			return nil, fmt.Errorf("invalid IOA notification checkpoint %q", ack)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	cursor, err := os.ReadFile(o.cursorPath())
	if err == nil {
		o.cursor = string(cursor)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := o.load(); err != nil {
		return nil, err
	}
	return o, nil
}

func (o *Observer) cursorPath() string {
	return filepath.Join(o.dir, "cursor")
}

func (o *Observer) logPath() string {
	return filepath.Join(o.dir, "events.jsonl")
}

func (o *Observer) notificationPath() string {
	return filepath.Join(o.dir, "notified")
}

// Cursor is the last IOA message id consumed.
func (o *Observer) Cursor() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.cursor
}

// Records re-reads the append-only log (used by the API and tests).
func (o *Observer) Records() ([]Record, error) {
	return ReadRecords(o.dir)
}

// ReadRecords inspects a retired source without a network client or writes.
func ReadRecords(dir string) ([]Record, error) {
	f, err := os.Open(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []Record
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	for scanner.Scan() {
		var rec Record
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			continue
		}
		out = append(out, rec)
	}
	return out, scanner.Err()
}

func (o *Observer) load() error {
	f, err := os.Open(o.logPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	for scanner.Scan() {
		var rec Record
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			continue
		}
		o.seen[rec.Key()] = true
		if rec.Seq > o.seq {
			o.seq = rec.Seq
		}
		if rec.Notify && rec.Error == "" && rec.Seq > o.notified {
			o.pending = append(o.pending, rec)
		}
	}
	return scanner.Err()
}

// Step reads the next page and appends new records. It returns how many records
// were appended. The cursor advances past unrelated records too, so the same
// page is not re-read forever.
func (o *Observer) Step(ctx context.Context) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	// Previously logged demand is independent of whether this host is reachable
	// now. A failed callback or a crash after append cannot lose scheduling.
	if err := o.notifyPending(); err != nil {
		return 0, err
	}
	previousCursor := o.cursor
	limit := 200
	if previousCursor == "" {
		// IOA applies a no-cursor limit to the tail, not the beginning. An
		// initial snapshot must not silently discard the earlier records.
		limit = 0
	}
	messages, err := o.client.Read(ctx, previousCursor, limit)
	recovered := false
	if errors.Is(err, ErrCursorGone) && previousCursor != "" {
		messages, err = o.client.Read(ctx, "", 0)
		if err != nil {
			return 0, fmt.Errorf("IOA 游标恢复失败（保留原游标）：%w", err)
		}
		// Only a complete successful snapshot can replace the rejected cursor.
		// Keep the append-only log and dedupe set across the replay.
		o.cursor = ""
		recovered = true
	}
	if err != nil {
		return 0, err
	}
	added := 0
	for _, msg := range messages {
		env, parseErr := ParseEnvelope(msg.Meta)
		if env == nil && parseErr == nil {
			// Another application's IOA data: visible in IOA, not harness work.
			o.cursor = msg.ID
			continue
		}
		rec := Record{
			Seq: o.seq + 1, Envelope: env, IOAMessageID: msg.ID, Sender: msg.Sender,
			CreatedAt: msg.CreatedAt, Content: msg.Content, ObservedAt: time.Now().UTC(),
		}
		if parseErr != nil {
			rec.Error = parseErr.Error()
		}
		if o.seen[rec.Key()] {
			o.cursor = msg.ID
			continue
		}
		created, createdErr := time.Parse(time.RFC3339Nano, rec.CreatedAt)
		live := o.initialized || (!o.startedAt.IsZero() && createdErr == nil && !created.Before(o.startedAt))
		rec.Notify = live && !recovered && rec.Error == ""
		rec.ObserverID = o.observerID
		if err := o.append(rec); err != nil {
			return added, err
		}
		o.seen[rec.Key()] = true
		o.seq = rec.Seq
		added++
		o.cursor = msg.ID
		if rec.Notify {
			o.pending = append(o.pending, rec)
		}
		if err := o.notifyPending(); err != nil {
			return added, err
		}
	}
	if err := o.saveCursor(); err != nil {
		return added, err
	}
	if recovered {
		log.Printf("ioa observer: 游标 %s 已失效；已重新读取现存历史并保留去重，新增 %d 条观察，当前游标 %s", previousCursor, added, o.cursor)
	}
	o.initialized = true
	return added, nil
}

func (o *Observer) notifyPending() error {
	for len(o.pending) > 0 && o.OnRecord != nil {
		rec := o.pending[0]
		if err := o.OnRecord(rec); err != nil {
			return fmt.Errorf("notify IOA record %s: %w", rec.Key(), err)
		}
		// A crash after the callback but before this checkpoint may replay it;
		// the consumer's source sequence makes that retry idempotent.
		if err := saveCheckpoint(o.notificationPath(), strconv.FormatInt(rec.Seq, 10)); err != nil {
			return err
		}
		o.notified = rec.Seq
		o.pending = o.pending[1:]
	}
	return nil
}

// Run polls until ctx is canceled. Errors are reported through onError; a
// transient host restart must not stop the observer forever.
func (o *Observer) Run(ctx context.Context, interval time.Duration, onError func(error)) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if _, err := o.Step(ctx); err != nil && ctx.Err() == nil && onError != nil {
			onError(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (o *Observer) append(rec Record) error {
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encode IOA record: %w", err)
	}
	f, err := os.OpenFile(o.logPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

func (o *Observer) saveCursor() error {
	return saveCheckpoint(o.cursorPath(), o.cursor)
}

func saveCheckpoint(path, value string) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(value), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
