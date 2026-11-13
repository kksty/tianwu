package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testMainKey   = "sk-main-0123456789" // gitleaks:allow -- deliberately fictional redaction fixture
	testPoolKey   = "sk-pool-0123456789"
	testFlatKey   = "sk-flat-0123456789"
	testToken     = "console-token-0123456789"
	testWeird     = `sk-"weird\key-0123456789`
	cleanLineText = "正常的运行事件，没有凭据"
)

type fixture struct {
	dataDir string
	harness string
	rootLog string
	projLog string
}

func jsonLine(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	harness := filepath.Join(root, "cyber.yaml")
	f := &fixture{dataDir: dataDir, harness: harness}
	for _, dir := range []string{
		filepath.Join(dataDir, "runs"),
		filepath.Join(dataDir, "projects", "p1", "runs"),
		filepath.Join(dataDir, "projects", "p1", "cases", "in_x", ".tw"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	config := `llm:
    providers:
        - id: fast
          provider: openai
          base_url: http://gw:8080
          api_key: ` + testMainKey + `
          api_keys:
              - ` + testMainKey + `
              - ` + testPoolKey + `
          model: fast-model
`
	if err := os.WriteFile(harness, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "console.token"), []byte(testToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	f.rootLog = filepath.Join(dataDir, "runs", "r_root.jsonl")
	rootBody := strings.Join([]string{
		jsonLine(t, map[string]any{"kind": "error", "text": "provider rejected key " + testMainKey}),
		jsonLine(t, map[string]any{"kind": "message", "text": "round 1", "raw": map[string]string{"auth": testPoolKey}}),
	}, "\n") + "\n"
	if err := os.WriteFile(f.rootLog, []byte(rootBody), 0o600); err != nil {
		t.Fatal(err)
	}
	f.projLog = filepath.Join(dataDir, "projects", "p1", "runs", "r_p1.jsonl")
	projBody := strings.Join([]string{
		jsonLine(t, map[string]any{"kind": "error", "text": "console token " + testToken + " leaked"}),
		jsonLine(t, map[string]any{"kind": "message", "text": cleanLineText}),
	}, "\n") + "\n"
	if err := os.WriteFile(f.projLog, []byte(projBody), 0o600); err != nil {
		t.Fatal(err)
	}
	// Things the tool must never touch: case evidence and the ledger.
	if err := os.WriteFile(filepath.Join(dataDir, "projects", "p1", "cases", "in_x", ".tw", "facts.jsonl"),
		[]byte(`{"title":"evidence","detail":"`+testMainKey+`"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "projects", "p1", "ledger.json"),
		[]byte(`{"runs":[{"id":"r1","reason":"`+testMainKey+`"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

func runTool(t *testing.T, cfg config) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cfg.out = &out
	err := run(cfg)
	return out.String(), err
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func assertValidJSONLines(t *testing.T, body string) {
	t.Helper()
	for i, line := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
		if line == "" {
			continue
		}
		if !json.Valid([]byte(line)) {
			t.Fatalf("line %d is not valid JSON: %s", i+1, line)
		}
	}
}

func TestDryRunDoesNotWrite(t *testing.T) {
	f := newFixture(t)
	beforeRoot, beforeProj := readFileString(t, f.rootLog), readFileString(t, f.projLog)
	out, err := runTool(t, config{dataDir: f.dataDir, harness: f.harness, out: nil})
	if err != nil {
		t.Fatal(err)
	}
	if readFileString(t, f.rootLog) != beforeRoot || readFileString(t, f.projLog) != beforeProj {
		t.Fatal("dry-run wrote a journal")
	}
	for _, want := range []string{"mode=dry-run", "files=2", "changed=2", "dry-run，没有写任何文件", "changed_lines="} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(f.rootLog + ".bak"); !os.IsNotExist(err) {
		t.Fatal("dry-run created a backup")
	}
}

func TestApplyRedactsSecretsAndKeepsJSONValid(t *testing.T) {
	f := newFixture(t)
	out, err := runTool(t, config{dataDir: f.dataDir, harness: f.harness, apply: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{f.rootLog, f.projLog} {
		body := readFileString(t, path)
		for _, secret := range []string{testMainKey, testPoolKey, testToken} {
			if strings.Contains(body, secret) {
				t.Fatalf("%s kept %q:\n%s", path, secret, body)
			}
		}
		if !strings.Contains(body, placeholder) {
			t.Fatalf("%s has no placeholder:\n%s", path, body)
		}
		assertValidJSONLines(t, body)
	}
	for _, want := range []string{"mode=apply", "changed=2"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	// The clean event is untouched.
	if !strings.Contains(readFileString(t, f.projLog), cleanLineText) {
		t.Fatal("clean event was altered")
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	f := newFixture(t)
	if _, err := runTool(t, config{dataDir: f.dataDir, harness: f.harness, apply: true}); err != nil {
		t.Fatal(err)
	}
	afterFirst := readFileString(t, f.rootLog)
	out, err := runTool(t, config{dataDir: f.dataDir, harness: f.harness, apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := readFileString(t, f.rootLog); got != afterFirst {
		t.Fatal("second run rewrote a clean journal")
	}
	if !strings.Contains(out, "changed=0") || !strings.Contains(out, "changed_lines=0") {
		t.Fatalf("second run is not idempotent:\n%s", out)
	}
}

func TestBackupKeepsOriginalAndIsNotOverwritten(t *testing.T) {
	f := newFixture(t)
	original := readFileString(t, f.rootLog)
	out, err := runTool(t, config{dataDir: f.dataDir, harness: f.harness, apply: true, backup: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "backups=2") {
		t.Fatalf("output does not report backups:\n%s", out)
	}
	backup := readFileString(t, f.rootLog+".bak")
	if backup != original {
		t.Fatal("backup is not the original journal")
	}
	if strings.Contains(backup, placeholder) {
		t.Fatal("backup was written after redaction, not before")
	}
	// A second pass changes nothing and must not replace the preserved original.
	if _, err := runTool(t, config{dataDir: f.dataDir, harness: f.harness, apply: true, backup: true}); err != nil {
		t.Fatal(err)
	}
	if again := readFileString(t, f.rootLog+".bak"); again != original {
		t.Fatal("second run overwrote the preserved backup")
	}
}

// A secret with JSON-special characters only appears escaped inside a JSON
// line; replacing it must keep the line valid.
func TestWeirdSecretStaysValidJSON(t *testing.T) {
	f := newFixture(t)
	line := jsonLine(t, map[string]any{"kind": "error", "text": "weird " + testWeird})
	if err := os.WriteFile(filepath.Join(f.dataDir, "runs", "r_weird.jsonl"), []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runTool(t, config{dataDir: f.dataDir, harness: f.harness, apply: true, extra: []string{testWeird}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "changed=3") {
		t.Fatalf("weird-secret file was not redacted:\n%s", out)
	}
	body := readFileString(t, filepath.Join(f.dataDir, "runs", "r_weird.jsonl"))
	if strings.Contains(body, testWeird) || strings.Contains(body, jsonEscapedInner(testWeird)) {
		t.Fatalf("weird secret survived:\n%s", body)
	}
	if !strings.Contains(body, placeholder) {
		t.Fatalf("no placeholder in:\n%s", body)
	}
	assertValidJSONLines(t, body)
}

// Case evidence and the ledger are outside this tool's mandate even when they
// contain the same secret.
func TestCaseEvidenceAndLedgerUntouched(t *testing.T) {
	f := newFixture(t)
	facts := filepath.Join(f.dataDir, "projects", "p1", "cases", "in_x", ".tw", "facts.jsonl")
	ledger := filepath.Join(f.dataDir, "projects", "p1", "ledger.json")
	factsBefore, ledgerBefore := readFileString(t, facts), readFileString(t, ledger)
	if _, err := runTool(t, config{dataDir: f.dataDir, harness: f.harness, apply: true}); err != nil {
		t.Fatal(err)
	}
	if readFileString(t, facts) != factsBefore {
		t.Fatal("case evidence was modified")
	}
	if readFileString(t, ledger) != ledgerBefore {
		t.Fatal("ledger was modified")
	}
}

// A missing data directory is an operator error: report it, do not panic.
func TestMissingDataDirFailsSafely(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if _, err := runTool(t, config{dataDir: missing, harness: ""}); err == nil {
		t.Fatal("missing data directory was accepted")
	}
}

// Nothing to clean is not an error; it is a no-op with a clear message.
func TestNoSecretsIsANoop(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(filepath.Join(dataDir, "runs"), 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dataDir, "runs", "r.jsonl")
	body := jsonLine(t, map[string]any{"kind": "message", "text": "hello"}) + "\n"
	if err := os.WriteFile(logPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runTool(t, config{dataDir: dataDir, harness: filepath.Join(root, "absent.yaml")})
	if err != nil {
		t.Fatalf("no secrets must be a no-op, got %v", err)
	}
	if !strings.Contains(out, "没有可清理的 secret") {
		t.Fatalf("output =\n%s", out)
	}
	if readFileString(t, logPath) != body {
		t.Fatal("no-op run wrote the journal")
	}
	if !strings.Contains(out, "harness 配置不存在") {
		t.Fatalf("missing harness config was not reported:\n%s", out)
	}
}

// A missing harness config must not stop the console-token half of the job.
func TestConsoleTokenRedactedWithoutHarness(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(filepath.Join(dataDir, "runs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "console.token"), []byte(testToken), 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dataDir, "runs", "r.jsonl")
	body := jsonLine(t, map[string]any{"kind": "error", "text": "token " + testToken}) + "\n"
	if err := os.WriteFile(logPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runTool(t, config{dataDir: dataDir, harness: filepath.Join(root, "absent.yaml"), apply: true}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(readFileString(t, logPath), testToken) {
		t.Fatal("console token was not redacted")
	}
}

func TestProfileKeysFromHarness(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cyber.yaml")
	body := `llm:
    api_key: ` + testFlatKey + `
    providers:
        - id: a
          api_key: ` + testMainKey + `
          api_keys:
              - ` + testPoolKey + `
              - ` + testMainKey + `
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	keys, err := profileKeysFromHarness(path)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(keys, "|")
	for _, want := range []string{testFlatKey, testMainKey, testPoolKey} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %v", want, keys)
		}
	}
	// Duplicates are collapsed later by buildPatterns.
	patterns := buildPatterns(keys)
	if len(patterns) != 3 {
		t.Fatalf("patterns = %v", patterns)
	}
}

func TestRedactLinesSkipsNonValidJSON(t *testing.T) {
	// A line that is not JSON at all must not be rewritten (it cannot be
	// validated), and a valid line with the secret must be.
	secret := testMainKey
	patterns := buildPatterns([]string{secret})
	body := []byte("not json " + secret + "\n" + `{"text":"` + secret + `"}` + "\n")
	out, lines, occurrences, warnings := redactLines(body, patterns)
	if lines != 1 || occurrences != 1 {
		t.Fatalf("lines=%d occurrences=%d warnings=%v", lines, occurrences, warnings)
	}
	if !strings.Contains(string(out), "not json "+secret) {
		t.Fatalf("non-JSON line was rewritten:\n%s", out)
	}
	if strings.Contains(string(out), `{"text":"`+secret) {
		t.Fatalf("valid line was not redacted:\n%s", out)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
}

func TestMissingConsoleTokenIsFine(t *testing.T) {
	// console.token is optional (deployments may use --console-token from a
	// file elsewhere); only the profile keys are required to do the job.
	f := newFixture(t)
	if err := os.Remove(filepath.Join(f.dataDir, "console.token")); err != nil {
		t.Fatal(err)
	}
	if _, err := runTool(t, config{dataDir: f.dataDir, harness: f.harness, apply: true}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(readFileString(t, f.rootLog), testMainKey) {
		t.Fatal("profile key was not redacted with console.token absent")
	}
}

// ---- truncated prefixes (task-20) ----
//
// The real leak: the harness clips tool output to 400 runes, so a key printed
// into a tool_result survives only as a prefix (`sk-magpie-key-30` out of a
// 62-character key). Full-value matching misses it; the tool replaces every
// prefix down to 12 bytes.

const truncatedKey = "sk-magpie-key-30" + "abcdefghijklmnopqrstuvwxyz0123456789"

func TestTruncatedSecretPrefixIsRedacted(t *testing.T) {
	f := newFixture(t)
	logPath := filepath.Join(f.dataDir, "runs", "r_trunc.jsonl")
	lines := make([]string, 0, 3)
	for _, n := range []int{12, 16, 30} {
		lines = append(lines, jsonLine(t, map[string]any{
			"kind": "tool_result", "text": "clipped tool_result: " + truncatedKey[:n],
		}))
	}
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(logPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runTool(t, config{dataDir: f.dataDir, harness: f.harness, apply: true, extra: []string{truncatedKey}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "changed=3") {
		t.Fatalf("truncated-prefix file was not redacted:\n%s", out)
	}
	redacted := readFileString(t, logPath)
	for _, n := range []int{12, 16, 30} {
		if strings.Contains(redacted, truncatedKey[:n]) {
			t.Fatalf("journal kept the %d-byte prefix:\n%s", n, redacted)
		}
	}
	if !strings.Contains(redacted, placeholder) {
		t.Fatalf("no placeholder in:\n%s", redacted)
	}
	assertValidJSONLines(t, redacted)

	// Idempotent: a second pass has nothing left to do.
	out, err = runTool(t, config{dataDir: f.dataDir, harness: f.harness, apply: true, extra: []string{truncatedKey}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "changed=0") {
		t.Fatalf("second pass is not idempotent:\n%s", out)
	}
	if got := readFileString(t, logPath); got != redacted {
		t.Fatal("second pass rewrote the clean journal")
	}
}

// A prefix of a secret with quotes/backslashes appears JSON-escaped; replacing
// it must leave valid JSON.
func TestTruncatedWeirdPrefixStaysValidJSON(t *testing.T) {
	weird := `sk-"weird\key-0123456789abcdefghij`
	f := newFixture(t)
	logPath := filepath.Join(f.dataDir, "runs", "r_weird_trunc.jsonl")
	lines := make([]string, 0, 3)
	for _, n := range []int{12, 16, 20} {
		lines = append(lines, jsonLine(t, map[string]any{
			"kind": "error", "text": "prefix " + weird[:n],
		}))
	}
	if err := os.WriteFile(logPath, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runTool(t, config{dataDir: f.dataDir, harness: f.harness, apply: true, extra: []string{weird}}); err != nil {
		t.Fatal(err)
	}
	redacted := readFileString(t, logPath)
	for _, n := range []int{12, 16, 20} {
		prefix := weird[:n]
		if strings.Contains(redacted, prefix) || strings.Contains(redacted, jsonEscapedInner(prefix)) {
			t.Fatalf("weird %d-byte prefix survived:\n%s", n, redacted)
		}
	}
	assertValidJSONLines(t, redacted)
}

// After apply+backup the .bak keeps the original (including the leaked prefix);
// the .bak is not scanned, so the next pass sees only the journals.
func TestBackupKeepsOriginalPrefixAndBakIsNotScanned(t *testing.T) {
	f := newFixture(t)
	logPath := filepath.Join(f.dataDir, "runs", "r_trunc.jsonl")
	body := jsonLine(t, map[string]any{"kind": "tool_result", "text": "clip " + truncatedKey[:16]}) + "\n"
	if err := os.WriteFile(logPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runTool(t, config{dataDir: f.dataDir, harness: f.harness, apply: true, backup: true, extra: []string{truncatedKey}})
	if err != nil {
		t.Fatal(err)
	}
	// 3 jsonl journals in the fixture (root, project, trunc); .bak files are
	// created but never scanned or counted.
	if !strings.Contains(out, "files=3") || !strings.Contains(out, "backups=3") {
		t.Fatalf("backups were scanned or not reported:\n%s", out)
	}
	backup := readFileString(t, logPath+".bak")
	if !strings.Contains(backup, truncatedKey[:16]) {
		t.Fatal("backup does not keep the original (expected: it is the rollback copy)")
	}
	if strings.Contains(readFileString(t, logPath), truncatedKey[:16]) {
		t.Fatal("journal still holds the leaked prefix")
	}
	if _, err := runTool(t, config{dataDir: f.dataDir, harness: f.harness, apply: true, backup: true, extra: []string{truncatedKey}}); err != nil {
		t.Fatal(err)
	}
	if again := readFileString(t, logPath+".bak"); again != backup {
		t.Fatal("second pass overwrote the preserved backup")
	}
	if _, err := os.Stat(logPath + ".bak.jsonl"); err == nil {
		t.Fatal(".bak was picked up as a journal")
	}
}

// Values shorter than the prefix floor are still replaced as whole values; the
// prefix rule only adds coverage for longer ones.
func TestShortSecretStillReplacedAsWholeValue(t *testing.T) {
	short := "sk-short-1" // 10 bytes: >= minSecretLen, < redactionPrefixLen
	f := newFixture(t)
	logPath := filepath.Join(f.dataDir, "runs", "r_short.jsonl")
	body := jsonLine(t, map[string]any{"kind": "error", "text": "key " + short + " rejected"}) + "\n"
	if err := os.WriteFile(logPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runTool(t, config{dataDir: f.dataDir, harness: f.harness, apply: true, extra: []string{short}}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(readFileString(t, logPath), short) {
		t.Fatal("short secret (whole-value replacement) survived")
	}
}

// One pattern per secret; the forms list covers full length down to the prefix
// floor, longest first.
func TestPrefixFormsCoverEveryLengthLongestFirst(t *testing.T) {
	patterns := buildPatterns([]string{truncatedKey})
	if len(patterns) != 1 {
		t.Fatalf("patterns = %d", len(patterns))
	}
	pat := patterns[0]
	want := len(truncatedKey) - redactionPrefixLen + 1
	if len(pat.escaped) != want {
		t.Fatalf("escaped forms = %d, want %d", len(pat.escaped), want)
	}
	if pat.escaped[0] != truncatedKey {
		t.Fatalf("longest form = %q, want the full value", pat.escaped[0])
	}
	if pat.probeEscaped != truncatedKey[:redactionPrefixLen] {
		t.Fatalf("probe = %q, want the shortest prefix", pat.probeEscaped)
	}
}

// ---- --delete-backups safety gate (task-27) ----
//
// The rollback copies still contain the credential; deleting them is only
// allowed when a *fresh* scan of the current run journals reports zero hits.
// Anything else is a refusal with a non-zero exit.

func TestDeleteBackupsOnlyDeletesWhenExplicit(t *testing.T) {
	f := newFixture(t)
	if _, err := runTool(t, config{dataDir: f.dataDir, harness: f.harness, apply: true, backup: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.rootLog + ".bak"); err != nil {
		t.Fatalf("prerequisite backup missing: %v", err)
	}
	// A normal --apply without the flag must never touch the rollback copies.
	if _, err := runTool(t, config{dataDir: f.dataDir, harness: f.harness, apply: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.rootLog + ".bak"); err != nil {
		t.Fatalf("backup deleted without --delete-backups: %v", err)
	}
}

func TestDeleteBackupsDryRunDoesNotDelete(t *testing.T) {
	f := newFixture(t)
	if _, err := runTool(t, config{dataDir: f.dataDir, harness: f.harness, apply: true, backup: true}); err != nil {
		t.Fatal(err)
	}
	out, err := runTool(t, config{dataDir: f.dataDir, harness: f.harness, deleteBackups: true})
	if err != nil {
		t.Fatalf("clean dry-run should be informational, got %v", err)
	}
	if _, statErr := os.Stat(f.rootLog + ".bak"); statErr != nil {
		t.Fatal("dry-run deleted a backup")
	}
	for _, want := range []string{"将删除", "加 --apply 才会真正删除", "轮换"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestDeleteBackupsRefusesWhenResidual(t *testing.T) {
	f := newFixture(t)
	// An unredactable line: invalid JSON that contains the secret and a quote,
	// so replacement would break JSON and the line is kept (with a warning).
	badPath := filepath.Join(f.dataDir, "runs", "r_bad.jsonl")
	if err := os.WriteFile(badPath, []byte(`garbage "`+testMainKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	backup := f.rootLog + ".bak"
	if err := os.WriteFile(backup, []byte("original with "+testMainKey), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := runTool(t, config{dataDir: f.dataDir, harness: f.harness, apply: true, deleteBackups: true})
	if err == nil {
		t.Fatal("residual hits must refuse --delete-backups")
	}
	if !strings.Contains(err.Error(), "拒绝删除") || !strings.Contains(err.Error(), "命中") {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(backup); statErr != nil {
		t.Fatal("backup was deleted despite residual hits")
	}
}

func TestDeleteBackupsAfterCleanScan(t *testing.T) {
	f := newFixture(t)
	if _, err := runTool(t, config{dataDir: f.dataDir, harness: f.harness, apply: true, backup: true}); err != nil {
		t.Fatal(err)
	}
	// A .bak outside the run journals is not in scope and must survive.
	outOfScope := filepath.Join(f.dataDir, "projects", "p1", "cases", "in_x", ".tw", "facts.jsonl.bak")
	if err := os.WriteFile(outOfScope, []byte("case evidence backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runTool(t, config{dataDir: f.dataDir, harness: f.harness, apply: true, deleteBackups: true})
	if err != nil {
		t.Fatalf("clean scan should delete the run-journal backups: %v", err)
	}
	if !strings.Contains(out, "deleted_backups=2") {
		t.Fatalf("deletion was not reported (want 2 run-journal backups):\n%s", out)
	}
	if !strings.Contains(out, "轮换") || !strings.Contains(out, "吊销") {
		t.Fatalf("strong rotation reminder missing:\n%s", out)
	}
	for _, path := range []string{f.rootLog + ".bak", f.projLog + ".bak"} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("backup still present: %s", path)
		}
	}
	if _, statErr := os.Stat(outOfScope); statErr != nil {
		t.Fatal("a .bak outside the run journals was deleted")
	}
}

func TestDeleteBackupsIsIdempotent(t *testing.T) {
	f := newFixture(t)
	if _, err := runTool(t, config{dataDir: f.dataDir, harness: f.harness, apply: true, backup: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := runTool(t, config{dataDir: f.dataDir, harness: f.harness, apply: true, deleteBackups: true}); err != nil {
		t.Fatal(err)
	}
	out, err := runTool(t, config{dataDir: f.dataDir, harness: f.harness, apply: true, deleteBackups: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "deleted_backups=0") {
		t.Fatalf("second delete pass should find nothing:\n%s", out)
	}
}

// Without a known secret set the tool cannot prove the journals are clean, so it
// refuses to delete anything (non-zero exit) instead of deleting on faith.
func TestDeleteBackupsRefusedWithoutSecrets(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(filepath.Join(dataDir, "runs"), 0o755); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(dataDir, "runs", "r1.jsonl.bak")
	if err := os.WriteFile(backup, []byte("old journal"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "runs", "r1.jsonl"),
		[]byte(jsonLine(t, map[string]any{"kind": "message", "text": "clean"})+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := runTool(t, config{dataDir: dataDir, harness: filepath.Join(root, "absent.yaml"),
		apply: true, deleteBackups: true})
	if err == nil || !strings.Contains(err.Error(), "无法验证") {
		t.Fatalf("error = %v, want an unverifiable refusal", err)
	}
	if _, statErr := os.Stat(backup); statErr != nil {
		t.Fatal("backup was deleted without verification")
	}
}
