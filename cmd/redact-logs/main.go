// Command redact-logs is the one-off, operator-run cleanup for historical run
// journals that may contain a provider key or the console token.
//
// The control plane now redacts secrets at the journal write point (task-13),
// but logs written before that fix can still hold a credential. This tool is
// deliberately explicit, not automatic: it scans only the run journals below
// one data directory, never runs at start-up, never touches case evidence or
// the ledger, and does not interpret event text. It replaces known secret
// values (the operator's profile keys, the console token, extra --secret
// values) with *** in both their raw and JSON-escaped forms.
//
// Safety contract:
//
//   - dry-run by default: without --apply nothing is written;
//   - --backup keeps the first original as <file>.bak before the first write;
//   - a line is only rewritten if the replacement keeps it valid JSON;
//   - running it twice changes nothing the second time (idempotent).
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// placeholder is what a secret becomes. It is plain ASCII inside a JSON string,
// so the surrounding line stays valid.
const placeholder = "***"

// minSecretLen keeps one- or two-character values (which are not credentials)
// from shredding every log line. It matches the control plane's redaction
// threshold (internal/api/journal.go).
const minSecretLen = 8

// redactionPrefixLen is the shortest secret prefix this tool also redacts.
//
// A secret that reached a truncated field — the harness clips tool output to
// 400 runes, and a model may print a key into a tool_result — survives as a
// fragment (`sk-magpie-key-30` out of a 62-character key). A full-value match
// cannot find that, and a leaked prefix is still key material, so the tool
// replaces every prefix of a known secret from full length down to this one
// (longest first, because a shorter prefix is contained in a longer one).
const redactionPrefixLen = 12

// config is the tool's resolved invocation.
type config struct {
	dataDir       string
	harness       string
	apply         bool
	backup        bool
	deleteBackups bool
	extra         []string
	out           io.Writer
}

// secretPattern is one credential in every form a JSONL line can contain it.
// escaped/raw run longest → shortest; probe* are the shortest forms, used as a
// cheap "can this pattern occur at all" gate.
type secretPattern struct {
	escaped      []string
	raw          []string
	probeEscaped string
	probeRaw     string
}

// report accumulates what one run saw and did.
type report struct {
	Files       int // journals scanned
	Changed     int // journals that differ after redaction
	ChangedLn   int // lines rewritten (or that would be, in dry-run)
	Occurrences int // individual secret occurrences replaced
	Secrets     int
	Backups     int
	Warnings    []string
}

func main() {
	var (
		dataDir       string
		harness       string
		apply         bool
		backup        bool
		deleteBackups bool
		extra         repeatedFlag
	)
	flag.StringVar(&dataDir, "data", "./data", "实例数据目录（只扫描 <data>/runs 与 <data>/projects/*/runs）")
	flag.StringVar(&harness, "harness-config", defaultHarnessPath(), "harness 配置（默认 $TW_HARNESS_CONFIG 或 ~/.cyber/cyber.yaml）")
	flag.Var(&extra, "secret", "额外要清理的 secret 值（可重复）")
	flag.BoolVar(&apply, "apply", false, "真正写回文件（默认只做 dry-run，不写任何东西）")
	flag.BoolVar(&backup, "backup", false, "写回前把原文件保存为 <file>.bak（已存在则不覆盖）")
	flag.BoolVar(&deleteBackups, "delete-backups", false,
		"删除 .bak 回滚副本：仅当重扫 *.jsonl 为 0 命中时允许；默认 dry-run 只报告；须先在 provider 侧轮换/吊销 key")
	flag.Parse()
	if flag.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "redact-logs: 无法识别的参数：%v\n", flag.Args())
		os.Exit(2)
	}
	cfg := config{dataDir: dataDir, harness: harness, apply: apply, backup: backup,
		deleteBackups: deleteBackups, extra: []string(extra), out: os.Stdout}
	if err := run(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "redact-logs: %v\n", err)
		os.Exit(1)
	}
}

// repeatedFlag collects a repeatable string flag.
type repeatedFlag []string

func (r *repeatedFlag) String() string { return strings.Join(*r, ",") }

func (f *repeatedFlag) Set(v string) error {
	*f = append(*f, v)
	return nil
}

// defaultHarnessPath mirrors the control plane's default: TW_HARNESS_CONFIG,
// then the harness's own user configuration.
func defaultHarnessPath() string {
	if p := strings.TrimSpace(os.Getenv("TW_HARNESS_CONFIG")); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".cyber", "cyber.yaml")
}

// run is main without the process exit, so tests can drive it directly.
func run(cfg config) error {
	if cfg.out == nil {
		cfg.out = io.Discard
	}
	dataDir, err := filepath.Abs(strings.TrimSpace(cfg.dataDir))
	if err != nil {
		return fmt.Errorf("解析 --data：%w", err)
	}
	if st, statErr := os.Stat(dataDir); statErr != nil || !st.IsDir() {
		return fmt.Errorf("数据目录不存在或不是目录：%s", dataDir)
	}
	secrets, notes := collectSecrets(cfg.harness, dataDir, cfg.extra)
	for _, note := range notes {
		fmt.Fprintf(cfg.out, "redact-logs: %s\n", note)
	}
	patterns := buildPatterns(secrets)
	mode := "dry-run"
	if cfg.apply {
		mode = "apply"
	}
	fmt.Fprintf(cfg.out, "redact-logs: data=%s harness=%s secrets=%d mode=%s backup=%t delete_backups=%t\n",
		dataDir, displayPath(cfg.harness), len(patterns), mode, cfg.backup, cfg.deleteBackups)
	if len(patterns) == 0 {
		// Deleting the rollback copies without a known secret set would mean
		// deleting them on faith: refuse rather than pretend to have verified.
		if cfg.deleteBackups {
			fmt.Fprintln(cfg.out, "redact-logs: 警告：.bak 是清理前的原始日志，仍含密钥/前缀。删除前必须先在 provider 侧轮换/吊销该 key。")
			return errors.New("没有已知 secret：无法验证 *.jsonl 已清理干净，拒绝删除 .bak")
		}
		fmt.Fprintln(cfg.out, "redact-logs: 没有可清理的 secret（没有 profile key、console.token 或 --secret）：不扫描，退出")
		return nil
	}

	files, err := discoverJournals(dataDir)
	if err != nil {
		return err
	}
	var rep report
	rep.Secrets = len(patterns)
	for _, path := range files {
		rep.Files++
		changed, lines, replacements, warnings, err := redactFile(path, patterns, cfg.apply, cfg.backup)
		if err != nil {
			return err
		}
		rep.Warnings = append(rep.Warnings, warnings...)
		if changed {
			rep.Changed++
			rep.ChangedLn += lines
			rep.Occurrences += replacements
			fmt.Fprintf(cfg.out, "  file %s: changed_lines=%d replacements=%d\n", path, lines, replacements)
		}
		if cfg.apply && cfg.backup && changed {
			if _, err := os.Stat(path + ".bak"); err == nil {
				rep.Backups++
			}
		}
	}
	for _, warning := range rep.Warnings {
		fmt.Fprintf(cfg.out, "redact-logs: 警告：%s\n", warning)
	}
	fmt.Fprintf(cfg.out, "redact-logs: files=%d changed=%d changed_lines=%d replacements=%d backups=%d mode=%s\n",
		rep.Files, rep.Changed, rep.ChangedLn, rep.Occurrences, rep.Backups, mode)
	if !cfg.apply && rep.Changed > 0 {
		fmt.Fprintln(cfg.out, "redact-logs: dry-run，没有写任何文件；确认后加 --apply（建议同时加 --backup）")
	}
	if cfg.deleteBackups {
		if err := handleDeleteBackups(cfg, dataDir, patterns); err != nil {
			return err
		}
	}
	return nil
}

// backupReminder is printed before anything destructive happens to the rollback
// copies. Redaction removes copies of a credential from logs; it never removes
// the credential itself from the provider.
const backupReminder = "警告：.bak 是清理前的原始日志，仍含完整 key/前缀。" +
	"删除前请先在 provider 侧轮换/吊销该 key（脱敏≠轮换）；删除不可恢复。"

// handleDeleteBackups is the verification-gated `.bak` cleanup.
//
// Safety contract: the backups may only be removed when a fresh scan of the
// *current* run journals reports zero secret occurrences. Any residual hit, an
// unreadable journal, or an empty secret set means "cannot prove the logs are
// clean", which is a refusal with a non-zero exit — never a best-effort delete.
func handleDeleteBackups(cfg config, dataDir string, patterns []secretPattern) error {
	fmt.Fprintf(cfg.out, "redact-logs: %s\n", backupReminder)
	files, err := discoverJournals(dataDir)
	if err != nil {
		return err
	}
	hitFiles, hits, err := scanResidual(files, patterns)
	if err != nil {
		return fmt.Errorf("无法验证 *.jsonl 是否已清理：%w；拒绝删除 .bak", err)
	}
	if hits > 0 {
		return fmt.Errorf("当前 *.jsonl 仍有 %d 处命中（%d 个文件）：拒绝删除 .bak；"+
			"先 --apply 清理并确认重扫 0 命中（残留行会打印警告）", hits, hitFiles)
	}
	backups, err := discoverBackups(dataDir)
	if err != nil {
		return err
	}
	if !cfg.apply {
		fmt.Fprintf(cfg.out, "redact-logs: dry-run：重扫 0 命中，将删除 %d 个 .bak；加 --apply 才会真正删除\n", len(backups))
		return nil
	}
	if len(backups) == 0 {
		fmt.Fprintln(cfg.out, "redact-logs: deleted_backups=0（没有 .bak 可删）")
		return nil
	}
	deleted := 0
	for _, path := range backups {
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("删除 %s：%w", path, err)
		}
		deleted++
		fmt.Fprintf(cfg.out, "  removed %s\n", path)
	}
	fmt.Fprintf(cfg.out, "redact-logs: deleted_backups=%d\n", deleted)
	return nil
}

// scanResidual re-reads the current journals and counts remaining secret
// occurrences. It is the "verify" half of --delete-backups: it must count what
// is actually still on disk, not what a rewrite would change (an unredactable
// line is skipped by apply but is still a leak).
func scanResidual(files []string, patterns []secretPattern) (hitFiles, occurrences int, err error) {
	for _, path := range files {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return 0, 0, fmt.Errorf("读 %s：%w", path, readErr)
		}
		found := 0
		for _, pattern := range patterns {
			// Every occurrence of any longer form contains the shortest form, so
			// counting that one avoids double-counting overlapping prefixes.
			count := bytes.Count(data, []byte(pattern.probeEscaped))
			if pattern.probeRaw != "" && pattern.probeRaw != pattern.probeEscaped {
				if rawCount := bytes.Count(data, []byte(pattern.probeRaw)); rawCount > count {
					count = rawCount
				}
			}
			found += count
		}
		if found > 0 {
			hitFiles++
			occurrences += found
		}
	}
	return hitFiles, occurrences, nil
}

// collectSecrets gathers every credential this data directory might have used:
// the harness profile keys (main key + api_keys pool + flat key), the console
// token, and operator-supplied extras. Notes explain a skipped source; they are
// informational, never fatal: a cleanup tool must still work on a partially
// restored data directory.
func collectSecrets(harnessPath, dataDir string, extra []string) ([]string, []string) {
	var out []string
	var notes []string
	seen := map[string]bool{}
	add := func(value string) {
		value = strings.TrimSpace(value)
		if len(value) < minSecretLen || seen[value] {
			return
		}
		seen[value] = true
		out = append(out, value)
	}

	harnessPath = strings.TrimSpace(harnessPath)
	if harnessPath == "" {
		notes = append(notes, "没有可用的 harness 配置路径（跳过 profile key）")
	} else if _, err := os.Stat(harnessPath); err != nil {
		notes = append(notes, fmt.Sprintf("harness 配置不存在，跳过 profile key：%s", harnessPath))
	} else {
		keys, err := profileKeysFromHarness(harnessPath)
		if err != nil {
			notes = append(notes, fmt.Sprintf("读 harness 配置失败，跳过 profile key：%v", err))
		}
		for _, key := range keys {
			add(key)
		}
	}

	tokenPath := filepath.Join(dataDir, "console.token")
	if data, err := os.ReadFile(tokenPath); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			notes = append(notes, fmt.Sprintf("读不了 %s：%v", tokenPath, err))
		}
	} else {
		add(string(data))
	}

	for _, value := range extra {
		add(value)
	}
	return out, notes
}

// harnessKeys is the part of the harness YAML this tool reads. Unknown fields
// are ignored: the file belongs to the harness, not to this tool.
type harnessKeys struct {
	LLM struct {
		APIKey    string `yaml:"api_key"`
		Providers []struct {
			ID      string   `yaml:"id"`
			APIKey  string   `yaml:"api_key"`
			APIKeys []string `yaml:"api_keys"`
		} `yaml:"providers"`
	} `yaml:"llm"`
}

// profileKeysFromHarness reads the main key and the whole api_keys pool of
// every profile, plus the flat llm.api_key some deployments still use.
func profileKeysFromHarness(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc harnessKeys
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(doc.LLM.Providers)+1)
	if strings.TrimSpace(doc.LLM.APIKey) != "" {
		out = append(out, doc.LLM.APIKey)
	}
	for _, provider := range doc.LLM.Providers {
		if strings.TrimSpace(provider.APIKey) != "" {
			out = append(out, provider.APIKey)
		}
		out = append(out, provider.APIKeys...)
	}
	return out, nil
}

// buildPatterns precompiles each value into its full form and every prefix down
// to redactionPrefixLen, longest first. One entry per secret.
func buildPatterns(values []string) []secretPattern {
	patterns := make([]secretPattern, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if len(value) < minSecretLen || seen[value] {
			continue
		}
		seen[value] = true
		var pattern secretPattern
		for _, prefix := range valuePrefixes(value) {
			pattern.escaped = append(pattern.escaped, jsonEscapedInner(prefix))
			if !strings.ContainsAny(prefix, "\\\"\n\r\t") {
				// A prefix with JSON-special characters can only appear escaped
				// inside a valid JSON line; replacing the raw form could break it.
				pattern.raw = append(pattern.raw, prefix)
			}
		}
		pattern.probeEscaped = lastString(pattern.escaped)
		pattern.probeRaw = lastString(pattern.raw)
		patterns = append(patterns, pattern)
	}
	return patterns
}

// valuePrefixes returns the value and every prefix down to redactionPrefixLen
// bytes, longest first. Prefixes that would split a UTF-8 rune are skipped.
func valuePrefixes(value string) []string {
	out := []string{value}
	for l := len(value) - 1; l >= redactionPrefixLen; l-- {
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

// jsonEscapedInner returns value as it appears between the quotes of a JSON
// string.
func jsonEscapedInner(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) < 2 {
		return value
	}
	return string(encoded[1 : len(encoded)-1])
}

// discoverJournals lists the run journals of one data directory. Case evidence,
// the ledger and generated host configs are intentionally left alone.
func discoverJournals(dataDir string) ([]string, error) {
	return globFiles([]string{
		filepath.Join(dataDir, "runs", "*.jsonl"),
		filepath.Join(dataDir, "projects", "*", "runs", "*.jsonl"),
	})
}

// discoverBackups lists the rollback copies of the run journals. `.bak` files
// under case evidence or anywhere else are not this tool's business: only the
// run journals it may have rewritten have a backup.
func discoverBackups(dataDir string) ([]string, error) {
	return globFiles([]string{
		filepath.Join(dataDir, "runs", "*.jsonl.bak"),
		filepath.Join(dataDir, "projects", "*", "runs", "*.jsonl.bak"),
	})
}

// globFiles expands the given patterns, keeping regular files only and sorting
// the result so output is stable between runs.
func globFiles(patterns []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, fmt.Errorf("扫描 %s：%w", pattern, err)
		}
		for _, path := range matches {
			st, err := os.Stat(path)
			if err != nil || st.IsDir() || seen[path] {
				continue
			}
			seen[path] = true
			out = append(out, path)
		}
	}
	sort.Strings(out)
	return out, nil
}

// redactFile rewrites one journal if (and only if) a secret occurs in it.
// changed reports whether the file differs; lines/occurrences count what was
// replaced. A line whose replacement would not be valid JSON is left unchanged
// and reported as a warning instead of being corrupted.
func redactFile(path string, patterns []secretPattern, apply, backup bool) (changed bool, lines, occurrences int, warnings []string, err error) {
	original, err := os.ReadFile(path)
	if err != nil {
		return false, 0, 0, nil, fmt.Errorf("读 %s：%w", path, err)
	}
	redacted, lines, occurrences, warnings := redactLines(original, patterns)
	for i, warning := range warnings {
		warnings[i] = path + "：" + warning
	}
	if lines == 0 {
		return false, 0, 0, warnings, nil
	}
	if !apply {
		return true, lines, occurrences, warnings, nil
	}
	if backup {
		backupPath := path + ".bak"
		if _, statErr := os.Stat(backupPath); errors.Is(statErr, os.ErrNotExist) {
			info, statErr := os.Stat(path)
			mode := os.FileMode(0o600)
			if statErr == nil {
				mode = info.Mode().Perm()
			}
			if writeErr := os.WriteFile(backupPath, original, mode); writeErr != nil {
				return false, 0, 0, warnings, fmt.Errorf("备份 %s：%w", path, writeErr)
			}
		}
	}
	if err := writeFileAtomic(path, redacted); err != nil {
		return false, 0, 0, warnings, err
	}
	return true, lines, occurrences, warnings, nil
}

// redactLines applies the patterns line by line. Only the JSON-escaped form is
// replaced inside a line (that is how a raw string appears in JSON); each
// candidate line is re-validated, and an invalid result is skipped with a
// warning rather than written.
func redactLines(data []byte, patterns []secretPattern) (out []byte, lines, occurrences int, warnings []string) {
	text := string(data)
	hadTrailingNewline := strings.HasSuffix(text, "\n")
	rawLines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	var buf bytes.Buffer
	for i, line := range rawLines {
		if i > 0 {
			buf.WriteByte('\n')
		}
		if line == "" || !strings.ContainsAny(line, `"`) {
			buf.WriteString(line)
			continue
		}
		next := line
		count := 0
		for _, pattern := range patterns {
			if !strings.Contains(next, pattern.probeEscaped) &&
				(pattern.probeRaw == "" || !strings.Contains(next, pattern.probeRaw)) {
				continue // no form of this secret can be in this line
			}
			for _, form := range pattern.escaped {
				if strings.Contains(next, form) {
					next = strings.ReplaceAll(next, form, placeholder)
					count++
				}
			}
			for _, form := range pattern.raw {
				if strings.Contains(next, form) {
					next = strings.ReplaceAll(next, form, placeholder)
					count++
				}
			}
		}
		if count == 0 {
			buf.WriteString(line)
			continue
		}
		if !json.Valid([]byte(next)) {
			warnings = append(warnings, fmt.Sprintf("第 %d 行替换后不是合法 JSON，已跳过该行", i+1))
			buf.WriteString(line)
			continue
		}
		lines++
		occurrences += count
		buf.WriteString(next)
	}
	out = buf.Bytes()
	if hadTrailingNewline {
		out = append(out, '\n')
	}
	return out, lines, occurrences, warnings
}

// writeFileAtomic replaces a file without leaving a half-written journal.
func writeFileAtomic(path string, data []byte) error {
	info, err := os.Stat(path)
	mode := os.FileMode(0o600)
	if err == nil {
		mode = info.Mode().Perm()
	}
	tmp := path + ".redact-logs.tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return fmt.Errorf("写 %s：%w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("替换 %s：%w", path, err)
	}
	return nil
}

// displayPath keeps the log line short: the absolute path is already printed as
// data=, and harness is often the home directory.
func displayPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return "-"
	}
	return path
}
