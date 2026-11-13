package api

// The run journal is the single writer of a run's event log.
//
// A run's `<DataDir>/runs/<id>.jsonl` used to be written only by the dispatch
// goroutine (an `os.File` + `json.Encoder` owned by that goroutine). Anything
// else that wanted to append to the same log - the message delivery path, for
// one - would have had to share those two objects without a lock, so a message
// arriving while an event was being encoded could interleave two JSON objects
// on one line. Losing that file means losing the only record of what the run
// did, so the writer is centralised here: every append goes through the same
// mutex, and the file stays the one source of truth that `runEvents` and the
// agent's tail endpoint read.
//
// The lock protects Encode only. It is never held across a Store call, and
// nothing outside this file touches the encoder.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/kksty/tianwu/internal/traffic"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/kksty/tianwu/internal/access"
	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
)

// ---- secret redaction (task-13, prefixes in task-20) ----
//
// A run journal is the most likely place for a credential to end up by
// accident: the harness echoes environment and configuration in error text, an
// executor may print a request dump, and the console token rides in the local
// host's environment. The rule is "do not print secrets", but the journal is
// the last write point every run shares, so it also enforces the rule. This is
// a backstop, not a licence to log keys.
//
// The values come from the hub (profile keys + console token), never from the
// event: redaction must not guess.
//
// Prefixes matter (task-20). A secret that reaches a truncated field — the
// harness clips tool output to 400 runes, a model may print a key into a
// tool_result — survives as a *fragment*, e.g. `sk-magpie-key-30` out of a
// 62-character key. No full-value match can find that, and a leaked prefix is
// still key material (enough to identify the credential and, for short
// truncations, to narrow a brute-force). So every secret also contributes its
// prefixes down to redactionPrefixLen; they are replaced longest-first because
// a shorter prefix is contained in a longer one.
const (
	// secretRefresh re-reads the provider at most this often: a key added in the
	// console becomes protected without paying a config read per event.
	secretRefresh = time.Minute
	// minSecretLen mirrors settings.SecretValues: values shorter than this are
	// not credentials and replacing them would corrupt ordinary text.
	minSecretLen = 8
	// redactionPrefixLen is the shortest secret prefix that is still specific
	// enough to redact. Twelve characters of a provider key are not a word that
	// occurs in normal prose, while matching shorter prefixes would shred logs.
	redactionPrefixLen = 12
	// Imported browser storage can contain megabytes. Keep the complete value
	// plus bounded short prefixes, avoiding quadratic escaped-string allocation.
	maxRedactionPrefixLen = 512
	// redactedValue keeps the surrounding JSON valid: it is plain ASCII inside a
	// string, and the same placeholder in pulse text.
	redactedValue = "***"
)

// secretPattern is one credential in every form a journal can contain it: the
// JSON-escaped forms for encoded lines, raw forms usable inside JSON, and raw
// forms for free text (pulse lines, notes). Each list runs longest → shortest.
type secretPattern struct {
	escaped []string
	raw     []string
	plain   []string
	// probe* are the shortest forms: a line without one cannot contain any
	// longer form of this secret, so most patterns are skipped with one cheap
	// substring check.
	probeEscaped string
	probeRaw     string
	probePlain   string
}

// newSecretPattern precompiles one credential. ok is false for values that are
// not credentials (too short).
func newSecretPattern(value string) (secretPattern, bool) {
	value = strings.TrimSpace(value)
	if len(value) < minSecretLen {
		return secretPattern{}, false
	}
	var pat secretPattern
	for _, prefix := range valuePrefixes(value) {
		pat.escaped = append(pat.escaped, jsonEscapedInner(prefix))
		pat.plain = append(pat.plain, prefix)
		// A prefix with JSON-special characters can only appear escaped inside a
		// valid JSON line; replacing the raw form there could break the document.
		if !strings.ContainsAny(prefix, "\\\"\n\r\t") {
			pat.raw = append(pat.raw, prefix)
		}
	}
	pat.probeEscaped = lastString(pat.escaped)
	pat.probeRaw = lastString(pat.raw)
	pat.probePlain = lastString(pat.plain)
	return pat, true
}

// valuePrefixes returns the value and every prefix down to redactionPrefixLen
// bytes, longest first. Prefixes that would split a UTF-8 rune are skipped;
// credentials are ASCII in practice, this only keeps a hand-written --secret
// from producing an invalid pattern.
func valuePrefixes(value string) []string {
	out := []string{value}
	for l := min(len(value)-1, maxRedactionPrefixLen); l >= redactionPrefixLen; l-- {
		prefix := value[:l]
		if !utf8.ValidString(prefix) {
			continue
		}
		out = append(out, prefix)
	}
	return out
}

func lastString(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[len(values)-1]
}

// secretPatterns returns the cached redaction table.
func (s *Server) secretPatterns() []secretPattern {
	if s == nil {
		return nil
	}
	s.secretMu.Lock()
	defer s.secretMu.Unlock()
	if !s.secretAt.IsZero() && time.Since(s.secretAt) < secretRefresh {
		return s.secretPats
	}
	values := access.SecretValues(s.CaseRoot)
	if s.Secrets != nil {
		values = append(values, s.Secrets()...)
	}
	pats := make([]secretPattern, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if len(value) < minSecretLen || seen[value] {
			continue
		}
		seen[value] = true
		if pat, ok := newSecretPattern(value); ok {
			pats = append(pats, pat)
		}
	}
	s.secretPats, s.secretAt = pats, time.Now()
	return pats
}

// redactSecrets replaces known credentials — full values and their prefixes —
// in free text (pulse lines, notes, exit reasons).
func (s *Server) redactSecrets(text string) string {
	pats := s.secretPatterns()
	if len(pats) == 0 || text == "" {
		return text
	}
	for _, pat := range pats {
		if pat.probePlain == "" || !strings.Contains(text, pat.probePlain) {
			continue
		}
		for _, form := range pat.plain {
			text = strings.ReplaceAll(text, form, redactedValue)
		}
	}
	return text
}

// redactJSONLine replaces known credentials in one encoded journal line. It
// works on the encoded bytes so the event's Raw JSON is covered too; replacing
// the escaped or plain-safe form inside a JSON string keeps the document valid.
func (s *Server) redactJSONLine(line []byte) []byte {
	pats := s.secretPatterns()
	if len(pats) == 0 || len(line) == 0 {
		return line
	}
	for _, pat := range pats {
		if !containsAny(line, pat.probeEscaped, pat.probeRaw) {
			continue
		}
		for _, form := range pat.escaped {
			line = bytes.ReplaceAll(line, []byte(form), []byte(redactedValue))
		}
		for _, form := range pat.raw {
			line = bytes.ReplaceAll(line, []byte(form), []byte(redactedValue))
		}
	}
	return line
}

// containsAny is the cheap "could this pattern occur at all" gate.
func containsAny(line []byte, probes ...string) bool {
	for _, probe := range probes {
		if probe != "" && bytes.Contains(line, []byte(probe)) {
			return true
		}
	}
	return false
}

// jsonEscapedInner returns s as it appears between the quotes of a JSON string.
func jsonEscapedInner(s string) string {
	encoded, err := json.Marshal(s)
	if err != nil || len(encoded) < 2 {
		return s
	}
	return string(encoded[1 : len(encoded)-1])
}

// errNoJournal means the run has no open journal: dispatch has not started it,
// or it has already settled (the journal is closed at the end of dispatch).
// A late message delivery is the normal way to see this; callers log and move
// on, because a message that arrives after the turn ended was never going to be
// delivered anyway (the live check in deliverMessage would have kept it queued).
var errNoJournal = errors.New("run journal is not open")

// runJournal serialises appends to one run's JSONL log. It is created by
// dispatch and closed when dispatch returns, so the file is open exactly for
// the life of the run.
type runJournal struct {
	mu sync.Mutex
	f  *os.File
	// redact is applied to the encoded line before it is written. Nil is a bare
	// test journal without a secret provider.
	redact func([]byte) []byte
}

func newRunJournal(f *os.File, redact func([]byte) []byte) *runJournal {
	return &runJournal{f: f, redact: redact}
}

// append encodes one JSON line. Holding mu across Encode is what makes a
// concurrent message event and a harness event land as whole, parseable lines.
//
// Redaction happens on the encoded bytes (redactJSONLine) so both typed fields
// and an event's Raw JSON are covered at this one write point.
func (j *runJournal) append(v any) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.f == nil {
		return errNoJournal
	}
	line, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if j.redact != nil {
		line = j.redact(line)
	}
	line = append(line, '\n')
	_, err = j.f.Write(line)
	return err
}

// close stops the writer. It is safe to call twice and safe to race with an
// in-flight append: the append either finishes first or fails with
// "file already closed" (which the caller logs; it is a late event, not data
// loss - the run is over).
func (j *runJournal) close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.f == nil {
		return nil
	}
	f := j.f
	j.f = nil
	return f.Close()
}

// openJournal registers the run's log as its one live writer. dispatch calls
// this instead of keeping the *os.File to itself.
func (s *Server) openJournal(runID, logPath string) (*runJournal, error) {
	f, err := os.Create(logPath)
	if err != nil {
		return nil, err
	}
	j := newRunJournal(f, s.redactJSONLine)
	s.journalsMu.Lock()
	if s.journals == nil {
		// Lazy init keeps bare test servers (Server literals without New) and
		// old call paths working without a second constructor contract.
		s.journals = map[string]*runJournal{}
	}
	s.journals[runID] = j
	s.journalsMu.Unlock()
	return j, nil
}

// journal returns the live writer for a run, or nil. The returned journal is
// only safe to append through because runJournal.append holds its own lock;
// close may still race it, which append reports as an error.
func (s *Server) journal(runID string) *runJournal {
	s.journalsMu.Lock()
	defer s.journalsMu.Unlock()
	return s.journals[runID]
}

// closeJournal removes the writer from the registry and closes the file. It is
// idempotent; callers are close paths, including error returns from dispatch.
func (s *Server) closeJournal(runID string) {
	s.journalsMu.Lock()
	j := s.journals[runID]
	delete(s.journals, runID)
	s.journalsMu.Unlock()
	if j != nil {
		_ = j.close()
	}
}

// appendEvent writes one already-shaped event through the run's journal. A
// missing journal is an error, not a silent drop; dispatch logs it because it
// should only happen if the writer was closed early (a bug), while message
// delivery treats it as the normal "run already settled" race.
func (s *Server) appendEvent(runID string, v any) error {
	j := s.journal(runID)
	if j == nil {
		return errNoJournal
	}
	return j.append(v)
}

// OpenPurposeRunLog opens the journal of a run started by a helper (verifier,
// report generator) instead of dispatch, and returns its event appender and its
// closer.
//
// Those runs used to pass a no-op event callback: the run row counted events at
// settle, but `runs/<id>.jsonl` was never created, so the console's event stream
// answered "no log for run …" and the execution review had nothing to read on
// exactly the runs an operator wants to audit ("did the review really re-run the
// proof?"). The journal is the same single writer every run uses, so a helper run
// gets the same record, the same redaction and the same reader — not a second
// event source.
//
// A run whose log cannot be opened still runs: losing the journal must not turn
// "can we re-prove this finding" into a failed review. The failure is logged and
// left on the row as a warning; the appender becomes a no-op.
func (s *Server) OpenPurposeRunLog(runID string) (func(executor.Event), func()) {
	noop := func(executor.Event) {}
	if s == nil || strings.TrimSpace(runID) == "" || strings.TrimSpace(s.DataDir) == "" {
		return noop, func() {}
	}
	logDir := filepath.Join(s.DataDir, "runs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		s.noteRunLogFailure(runID, err)
		return noop, func() {}
	}
	logPath := filepath.Join(logDir, runID+".jsonl")
	if _, err := s.openJournal(runID, logPath); err != nil {
		s.noteRunLogFailure(runID, err)
		return noop, func() {}
	}
	if s.Store != nil {
		_, _ = s.Store.UpdateRun(runID, func(r *model.Run) { r.LogPath = logPath })
	}
	emit := func(ev executor.Event) {
		if err := s.appendRunEvent(runID, ev); err != nil {
			log.Printf("run %s: 写事件失败：%v", runID, err)
		}
	}
	return emit, func() { s.closeJournal(runID) }
}

// noteRunLogFailure leaves "this run has no journal" on the row, where the
// console already renders warnings, instead of only in the process log.
func (s *Server) noteRunLogFailure(runID string, err error) {
	log.Printf("run %s: 无法写运行事件日志：%v", runID, err)
	if s.Store == nil {
		return
	}
	_, _ = s.Store.UpdateRun(runID, func(r *model.Run) {
		r.Warnings = append(r.Warnings, "无法写运行事件日志："+err.Error())
	})
}

// appendRunEvent writes one harness event. It exists as its own method so the
// dispatch write point is typed and can be found; synthetic audit events go
// through appendEvent.
func (s *Server) appendRunEvent(runID string, ev executor.Event) error {
	if ev.NativeFlow != nil {
		run, ok := s.Store.GetRun(runID)
		if !ok {
			return fmt.Errorf("HTTP 观察所属 run 不存在：%s", runID)
		}
		s.artifactMu.Lock()
		record, err := traffic.SaveCapture(s.CaseRoot, runID, run.IntentID, ev.SessionID, ev.NativeEventID, ev.NativeFlow, ev.NativeOperation)
		s.artifactMu.Unlock()
		ev.NativeFlow, ev.NativeOperation = nil, nil
		if err != nil {
			ev.Kind, ev.Text = "warning", "保存 HTTP 观察失败："+err.Error()
			_, _ = s.Store.UpdateRun(runID, func(r *model.Run) { r.Warnings = append(r.Warnings, ev.Text) })
		} else {
			ev.FlowRef = record.Ref
		}
	}
	if ev.SessionID != "" || (ev.Kind == executor.KindToolResult && !ev.OK) {
		if err := s.Store.ObserveRun(runID, ev.SessionID, ev.Kind == executor.KindToolResult && !ev.OK); err != nil {
			return err
		}
	}
	if ev.Native != nil {
		run, ok := s.Store.GetRun(runID)
		if !ok {
			return fmt.Errorf("原生结果所属 run 不存在：%s", runID)
		}
		if err := s.captureArtifact(&ev, runID, run.IntentID); err != nil {
			ev.Kind, ev.Text, ev.Native = "warning", err.Error(), nil
			_, _ = s.Store.UpdateRun(runID, func(r *model.Run) { r.Warnings = append(r.Warnings, ev.Text) })
		}
	}
	return s.appendEvent(runID, ev)
}

// noteRunEvent is the single write point for the live pulse (Store.NoteEvent).
// The pulse is persisted in the ledger and copied into board prompts, so it gets
// the same secret redaction as the journal: one event text, two write points.
func (s *Server) noteRunEvent(runID string, at time.Time, kind, tool, text string) {
	s.Store.NoteEvent(runID, at, kind, tool, s.redactSecrets(text))
}

// collabEvent is the synthetic run event for one message delivery or send. It
// uses the same top-level "kind" key as executor.Event so the JSONL stays one
// format, and the console's journal renderer ignores kinds it does not know.
type collabEvent struct {
	Kind      string    `json:"kind"` // "collab_message"
	At        time.Time `json:"at"`
	MessageID string    `json:"message_id"`
	From      string    `json:"from"`      // participant id
	To        string    `json:"to"`        // participant id
	Direction string    `json:"direction"` // "out" (sent by this run) | "in" (delivered to it)
	Delivery  string    `json:"delivery"`  // accepted|unknown|queued
	Text      string    `json:"text,omitempty"`
}

// appendCollabEvent records a message touching one run's event stream. Only the
// direction/state and a clipped excerpt go in the journal: the full body stays
// in the message ledger, so the journal cannot become a second mailbox (or leak
// more than the audit needs).
//
// A run with no open journal is not an error here: a message delivered just as
// the run settles has nowhere to go and was not going to be read anyway. Real
// write failures are logged so a broken journal is visible instead of silent.
func (s *Server) appendCollabEvent(runID string, m model.Message, direction, delivery string) {
	if runID == "" {
		return
	}
	if direction == "" {
		direction = "in"
	}
	err := s.appendEvent(runID, collabEvent{
		Kind: "collab_message", At: time.Now(), MessageID: m.ID,
		From: m.FromParticipantID, To: m.ToParticipantID,
		Direction: direction, Delivery: delivery, Text: clipRunes(m.Text, 400),
	})
	if err != nil && !errors.Is(err, errNoJournal) {
		log.Printf("run %s: 写消息事件失败：%v", runID, err)
	}
}

// injectedEvent records that some text was put into a run's prompt. It is the
// audit half of "injection is visible": a run event log can answer what was
// injected, from where, how big it was, and in which prompt section, even
// though the prompt itself only lives in the harness session.
type injectedEvent struct {
	Kind          string    `json:"kind"` // "injected"
	At            time.Time `json:"at"`
	RunID         string    `json:"run_id"`
	Source        string    `json:"source"` // message|hint|state|peer
	MessageID     string    `json:"message_id,omitempty"`
	Bytes         int       `json:"bytes"`
	PromptSection string    `json:"prompt_section"` // inbox|hint|state|handoff
}

// NoteInjected is the one write path for injection audit events. Other prompt
// builders (commander rounds, steer, wake) call it with the source and section
// they just appended, so a delivered message is provable from the run log
// rather than only from the mailbox.
func (s *Server) NoteInjected(runID, source, messageID string, bytes int, section string) {
	if runID == "" {
		return
	}
	err := s.appendEvent(runID, injectedEvent{
		Kind: "injected", At: time.Now(), RunID: runID, Source: source,
		MessageID: messageID, Bytes: bytes, PromptSection: section,
	})
	if err != nil && !errors.Is(err, errNoJournal) {
		log.Printf("run %s: 写注入事件失败：%v", runID, err)
	}
}

// ---- ledger-external delegation audit (task-24) ----
//
// The harness cannot be configured to remove its native `subagent` tool (task-19
// investigation: cmd/aiscan always installs subagentext.New, and
// subagentext.NewTools when a session is configured; --tools only selects
// search/browser/record, --no-agent removes the agent itself, guardrail only
// approves/blocks individual calls). So the tianwu-side fallback is observation:
// when a structured tool_call event names that tool, the run records a warning
// saying the child session is outside the ledger. It does not stop the call (we
// cannot) and it does not pretend the ledger is complete.
//
// Detection boundary: ONLY the structured tool name on a tool_call event is
// inspected. Tool arguments, bash command text, tool_result output and model
// prose are never parsed for keywords - an executor calling bash with
// "ioa send ..." or printing the word "subagent" is not proof of a
// ledger-external session, and keyword matching there would both false-positive
// and cross the agent.md §1 line. IOA CLI usage is therefore NOT reported from
// this hook: the harness exposes it as an ordinary bash command whose arguments
// are not a structured field we can trust, so the honest answer is "unknown",
// not a guess.
const (
	// nativeSubagentTool is the harness tool name from
	// agent/subagent/sessionexec/tool.go (Name() == "subagent").
	nativeSubagentTool = "subagent"
	// maxRunWarningRows mirrors ledger's per-run warning cap: a warning added
	// mid-run must not make the run row unbounded.
	maxRunWarningRows = 20
)

// nativeSubagentWarning is the fixed text added to Run.Warnings. It is a
// constant, so it needs no redaction and dedupes by exact match.
const nativeSubagentWarning = "本轮使用了 harness 原生 subagent：它在 tianwu 账本之外创建子会话（没有 run/usage，不可取消、不计费）；需要分工请改用 spawn / children（POST /agent/<project>/api/spawn 或 POST /intents/{id}/children）。"

// noteNativeSubagentUsage records the warning once per run when a structured
// tool_call event names the harness subagent tool. The event callback is the
// only write path (dispatch and commander both call it); the warning itself
// reaches the journal at settle through flushRunWarnings, so it is not appended
// twice. Dedupe is checked before and inside UpdateRun because a retried or
// re-settled run must not grow a second copy.
func (s *Server) noteNativeSubagentUsage(runID string, ev executor.Event) {
	if runID == "" || ev.Kind != executor.KindToolCall || ev.Tool != nativeSubagentTool {
		return
	}
	if run, ok := s.Store.GetRun(runID); ok {
		for _, w := range run.Warnings {
			if w == nativeSubagentWarning {
				return
			}
		}
	}
	if _, err := s.Store.UpdateRun(runID, func(r *model.Run) {
		for _, w := range r.Warnings {
			if w == nativeSubagentWarning {
				return
			}
		}
		if len(r.Warnings) >= maxRunWarningRows {
			return
		}
		r.Warnings = append(r.Warnings, nativeSubagentWarning)
	}); err != nil {
		log.Printf("run %s: 记录原生 subagent 告警失败：%v", runID, err)
	}
}

// flushRunWarnings turns a run's warning rows into synthetic journal events once
// the run is about to settle (dispatch) or has settled (commander). The journal
// is the single writer, so appends stay serialized and redacted; a warning is
// written exactly once because both callers run it at their single settle point.
func (s *Server) flushRunWarnings(runID string) {
	run, ok := s.Store.GetRun(runID)
	if !ok || len(run.Warnings) == 0 {
		return
	}
	// Warning text can quote model-supplied paths, so it crosses the same secret
	// boundary as the exit reason and the journal (task-13): a credential echoed
	// into a path must not reach the ledger unredacted.
	clean := make([]string, len(run.Warnings))
	changed := false
	for i, w := range run.Warnings {
		clean[i] = s.redactSecrets(w)
		if clean[i] != w {
			changed = true
		}
	}
	if changed {
		run.Warnings = clean
		_, _ = s.Store.UpdateRun(runID, func(r *model.Run) { r.Warnings = clean })
	}
	for _, w := range run.Warnings {
		if err := s.appendEvent(runID, runWarningEvent{Kind: "warning", At: time.Now(), Text: w}); err != nil {
			log.Printf("run %s：写告警事件失败：%v", runID, err)
			break
		}
	}
}
