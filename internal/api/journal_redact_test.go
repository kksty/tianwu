package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// A run journal must never keep a copy of a credential. The harness echoes
// environment/configuration in error text and some executors dump requests, so
// the journal's single write point replaces known secrets with *** (task-13).
const (
	testAPIKey      = "sk-live-0123456789abcdef" // gitleaks:allow -- deliberately fictional redaction fixture
	testConsoleTok  = "console-token-abcdef0123456789"
	testWeirdSecret = `we"ird-key-0123456789`
)

func redactTestServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, nil, dir, filepath.Join(dir, "cases"))
	s.Secrets = func() []string {
		return []string{testAPIKey, testConsoleTok, testWeirdSecret, "short"}
	}
	return s, st
}

func readJournalLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	trimmed := strings.TrimRight(string(data), "\n")
	if trimmed == "" {
		return nil
	}
	lines := strings.Split(trimmed, "\n")
	for i, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Fatalf("journal line %d is not valid JSON after redaction: %s", i, line)
		}
	}
	return lines
}

func TestJournalRedactsKnownSecrets(t *testing.T) {
	s, st := redactTestServer(t)
	run, err := st.AddRun(model.Run{Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(s.DataDir, "runs", run.ID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := s.openJournal(run.ID, logPath); err != nil {
		t.Fatal(err)
	}
	// 1) A harness error echoing the key in text and in the raw event JSON.
	raw := json.RawMessage(`{"request":{"authorization":"Bearer ` + testAPIKey + `"},"console":"` + testConsoleTok + `"}`)
	if err := s.appendRunEvent(run.ID, executor.Event{
		Kind: executor.KindError,
		Tool: "shell",
		Text: "provider rejected key " + testAPIKey + " (token " + testConsoleTok + ")",
		Raw:  raw,
	}); err != nil {
		t.Fatal(err)
	}
	// 2) A secret with JSON-special characters: it appears escaped in the line,
	// and redaction must keep the line valid.
	if err := s.appendRunEvent(run.ID, executor.Event{
		Kind: executor.KindMessage, Role: "assistant", Text: "token is " + testWeirdSecret,
	}); err != nil {
		t.Fatal(err)
	}
	// 3) A synthetic collaboration event (same write point).
	s.appendCollabEvent(run.ID, model.Message{ID: "m1", Text: "forwarded " + testConsoleTok}, "in", "accepted")
	// 4) The live pulse path, which is persisted in the ledger separately.
	s.noteRunEvent(run.ID, time.Now(), executor.KindError, "", "leak "+testAPIKey)
	s.closeJournal(run.ID)

	lines := readJournalLines(t, logPath)
	if len(lines) != 3 {
		t.Fatalf("lines = %d, want 3: %v", len(lines), lines)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, secret := range []string{testAPIKey, testConsoleTok, testWeirdSecret} {
		if strings.Contains(content, secret) {
			t.Fatalf("journal kept secret %q:\n%s", secret, content)
		}
	}
	if escaped := strings.Trim(string(mustJSON(t, testWeirdSecret)), `"`); strings.Contains(content, escaped) {
		t.Fatalf("journal kept the escaped form of the weird secret:\n%s", content)
	}
	if !strings.Contains(content, "***") {
		t.Fatalf("journal has no redaction marker:\n%s", content)
	}
	// Short values are not credentials; replacing them would corrupt the log.
	if strings.Contains(content, "sh***") || strings.Contains(content, "shor") {
		t.Fatalf("short non-secret value was redacted:\n%s", content)
	}

	got, ok := st.GetRun(run.ID)
	if !ok {
		t.Fatal("run disappeared")
	}
	if strings.Contains(got.LastEventText, testAPIKey) {
		t.Fatalf("pulse kept the key: %q", got.LastEventText)
	}
	if !strings.Contains(got.LastEventText, "***") {
		t.Fatalf("pulse was not redacted: %q", got.LastEventText)
	}
}

// Without a provider (bare test server) the journal is untouched: redaction is
// a backstop wired by the hub, not a global text filter.
func TestJournalWithoutSecretProviderKeepsText(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, nil, dir, filepath.Join(dir, "cases"))
	run, err := st.AddRun(model.Run{Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(s.DataDir, "runs", run.ID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := s.openJournal(run.ID, logPath); err != nil {
		t.Fatal(err)
	}
	if err := s.appendRunEvent(run.ID, executor.Event{Kind: executor.KindError, Text: "normal text"}); err != nil {
		t.Fatal(err)
	}
	s.closeJournal(run.ID)
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "normal text") {
		t.Fatalf("unconfigured server altered the journal: %s", data)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

// redactServerWithSecrets builds a bare server whose redaction table is exactly
// the values given.
func redactServerWithSecrets(t *testing.T, secrets ...string) (*Server, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, nil, dir, filepath.Join(dir, "cases"))
	s.Secrets = func() []string { return append([]string(nil), secrets...) }
	return s, st
}

func newJournalRun(t *testing.T, s *Server, st *store.Store) (model.Run, string) {
	t.Helper()
	run, err := st.AddRun(model.Run{Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(s.DataDir, "runs", run.ID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := s.openJournal(run.ID, logPath); err != nil {
		t.Fatal(err)
	}
	return run, logPath
}

// The real leak from acceptance: the harness clips tool output to 400 runes, so
// a key printed into a tool_result survives as a *prefix* (`sk-magpie-key-30`
// of a 62-character key). Full-value matching cannot find that fragment; every
// prefix down to 12 characters is redacted instead (task-20).
func TestJournalRedactsTruncatedSecretPrefixes(t *testing.T) {
	key := "sk-magpie-key-30" + "abcdefghijklmnopqrstuvwxyz0123456789"
	s, st := redactServerWithSecrets(t, key)
	run, logPath := newJournalRun(t, s, st)

	// What truncated output looks like: only a prefix reaches the event.
	for _, n := range []int{12, 16, 30} {
		if err := s.appendRunEvent(run.ID, executor.Event{
			Kind: executor.KindToolResult, Tool: "shell",
			Text: "clipped tool_result tail: " + key[:n],
		}); err != nil {
			t.Fatal(err)
		}
	}
	// And the pulse path (plain text, not JSON).
	s.noteRunEvent(run.ID, time.Now(), executor.KindToolResult, "shell", "pulse "+key[:30])
	s.closeJournal(run.ID)

	lines := readJournalLines(t, logPath)
	if len(lines) != 3 {
		t.Fatalf("lines = %d, want 3", len(lines))
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, n := range []int{12, 16, 30} {
		if strings.Contains(content, key[:n]) {
			t.Fatalf("journal kept the %d-byte prefix %q:\n%s", n, key[:n], content)
		}
	}
	if !strings.Contains(content, redactedValue) {
		t.Fatalf("no placeholder in:\n%s", content)
	}
	got, ok := st.GetRun(run.ID)
	if !ok {
		t.Fatal("run disappeared")
	}
	if strings.Contains(got.LastEventText, key[:12]) {
		t.Fatalf("pulse kept the truncated prefix: %q", got.LastEventText)
	}
}

// Prefixes of a secret with JSON-special characters: the line must stay valid
// JSON after the escaped prefix is replaced.
func TestJournalRedactsWeirdSecretPrefixes(t *testing.T) {
	secret := `sk-"weird\key-0123456789abcdefghij`
	s, st := redactServerWithSecrets(t, secret)
	run, logPath := newJournalRun(t, s, st)
	for _, n := range []int{12, 16, 20} {
		prefix := secret[:n]
		if err := s.appendRunEvent(run.ID, executor.Event{
			Kind: executor.KindError, Text: "prefix -> " + prefix,
		}); err != nil {
			t.Fatal(err)
		}
	}
	s.closeJournal(run.ID)
	lines := readJournalLines(t, logPath) // helper asserts every line is valid JSON
	if len(lines) != 3 {
		t.Fatalf("lines = %d", len(lines))
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, n := range []int{12, 16, 20} {
		prefix := secret[:n]
		if strings.Contains(content, prefix) || strings.Contains(content, jsonEscapedInner(prefix)) {
			t.Fatalf("journal kept the %d-byte weird prefix:\n%s", n, content)
		}
	}
	if !strings.Contains(content, redactedValue) {
		t.Fatalf("no placeholder in:\n%s", content)
	}
}

// The prefix rule must not redact ordinary short values: a 12-byte prefix of a
// secret is specific, shorter fragments are not generated.
func TestJournalPrefixRuleIgnoresShorterFragments(t *testing.T) {
	key := "sk-magpie-key-30" + "abcdefghijklmnopqrstuvwxyz0123456789"
	s, st := redactServerWithSecrets(t, key)
	run, logPath := newJournalRun(t, s, st)
	if err := s.appendRunEvent(run.ID, executor.Event{
		Kind: executor.KindMessage, Text: "ordinary text " + key[:11] + " remains",
	}); err != nil {
		t.Fatal(err)
	}
	s.closeJournal(run.ID)
	content := string(readFile(t, logPath))
	if !strings.Contains(content, key[:11]) {
		t.Fatalf("an 11-byte fragment (below the prefix floor) was redacted:\n%s", content)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
