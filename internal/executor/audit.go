package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Audit runs cyber-audit, the source/binary audit harness, for one intent
// (docs/06, D47).
//
// It is a local one-shot process (the only node form that is not a host, D64),
// and the contract is different in two ways that cannot be papered over by a
// config line:
//
//   - The command line has no `agent` subcommand: a one-shot audit is
//     `--workdir … --report-dir … -p … --output-format stream-json`.
//   - The product is a report directory (findings.json, coverage.json, index.md,
//     evidence/…), not a `.tw/facts.jsonl` handoff. The ledger only reads the
//     handoff, so this executor translates the report the same way a human
//     would: confirmed/candidate findings become vuln facts, inconclusive ones
//     become notes, dismissed ones are dropped, and every cited evidence file
//     that exists inside the report is attached by its case-relative path.
//
// A failed run imports nothing: cyber-audit marks an unfinished report
// incomplete, and half-written findings are not a conclusion.
type Audit struct {
	Bin     string
	Extra   []string
	Timeout time.Duration
	// Env is appended to the process environment ("K=V").
	Env []string
}

func (a Audit) Name() string { return "audit" }

// AuditReportPrefix names the per-run report directory inside the case. It must
// be new every run: cyber-audit refuses to reuse a report directory, and a retry
// must not overwrite the previous run's evidence.
const AuditReportPrefix = "audit-"

// AuditPromptFile keeps the task off argv for the same reason as PromptFile.
const AuditPromptFile = ".tw/audit-prompt.txt"

func (a Audit) Run(ctx context.Context, req Request, emit func(Event)) (Result, error) {
	if strings.TrimSpace(a.Bin) == "" {
		return Result{ExitCode: -1}, errors.New("未找到 cyber-audit：在 nodes.json 里给 audit 节点配 bin")
	}
	if strings.TrimSpace(req.WorkDir) == "" {
		return Result{ExitCode: -1}, errors.New("audit 节点需要一个 case 目录作为 --workdir")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if a.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, a.Timeout)
		defer cancel()
	}

	report, err := newAuditReportDir(req.WorkDir)
	if err != nil {
		return Result{ExitCode: -1}, err
	}

	// The prompt goes to a file so the task is not on argv (and does not hit the
	// Windows command-line limit). A failure here fails the run: silently moving
	// a whole brief back onto argv is how an unrelated "path too long" error
	// hides the real cause.
	path, err := writePromptFile(req.WorkDir, AuditPromptFile, req.Prompt)
	if err != nil {
		return Result{ExitCode: -1}, fmt.Errorf("写 audit 提示词失败：%w", err)
	}

	args := a.command(req.WorkDir, report, path, req.Skills)
	res, err := runProcess(ctx, a.Bin, args, req.WorkDir, a.Env, emit)
	if err != nil {
		return res, err
	}
	// Exit code and turn verdict are different things (executor.go): a harness
	// can exit 0 after a failed turn. A failed audit imports nothing — half a
	// report is not a conclusion.
	if res.ExitCode != 0 || res.IsError || res.StopReason == "error" {
		if strings.TrimSpace(res.Output) == "" {
			res.Output = "audit 回合失败（stop_reason=" + res.StopReason + "），报告未导入"
		}
		return res, nil
	}
	total, imported, err := importAuditFacts(req.WorkDir, report)
	if err != nil {
		return res, fmt.Errorf("audit 报告无法导入事实意图图：%w", err)
	}
	res.Text = fmt.Sprintf("audit: %d 条 finding → %d 条事实（%s）", total, imported, report)
	return res, nil
}

// command is the one-shot audit command line. It has no `agent` subcommand:
// cyber-audit runs the audit directly, and its --output-format is the same
// stream-json the ledger already parses.
func (a Audit) command(workDir, report, prompt string, skills []string) []string {
	args := []string{
		"--workdir", workDir,
		"--report-dir", filepath.Join(workDir, report),
		"--output-format", "stream-json",
		"--timeout", strconv.Itoa(int(a.Timeout / time.Second)),
		"-p", prompt,
	}
	for _, sk := range skills {
		if sk = strings.TrimSpace(sk); sk != "" {
			args = append(args, "-s", sk)
		}
	}
	return append(args, a.Extra...)
}

// newAuditReportDir picks an unused report directory name. Only "does not
// exist" means the name is free: a permission or path-length error used to be
// treated the same way and sent the lookup into an unbounded loop.
func newAuditReportDir(workDir string) (string, error) {
	base := AuditReportPrefix + time.Now().Format("20060102-150405")
	name := base
	for i := 2; i <= 1000; i++ {
		_, err := os.Stat(filepath.Join(workDir, name))
		switch {
		case os.IsNotExist(err):
			return name, nil
		case err != nil:
			return "", fmt.Errorf("检查 audit 报告目录失败：%w", err)
		}
		name = base + "-" + strconv.Itoa(i)
	}
	return "", errors.New("audit 报告目录名连续 1000 个都已被占用：先清理旧的 audit-* 目录")
}

// auditFinding mirrors the findings.json contract of cyber-audit
// (cmd/audit/pkg/exts/audit/skills/report.md).
type auditFinding struct {
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	Status         string   `json:"status"`
	Severity       string   `json:"severity"`
	Location       string   `json:"location"`
	Preconditions  string   `json:"preconditions"`
	Trace          []string `json:"trace"`
	Impact         string   `json:"impact"`
	Evidence       []string `json:"evidence"`
	Verification   string   `json:"verification"`
	Reproduction   string   `json:"reproduction"`
	Recommendation string   `json:"recommendation"`
}

// importAuditFacts turns findings.json into the handoff the ledger already
// reads. It appends, so a retried run declares only its own findings. Each line
// carries its source ref ("report:<report>#<finding>"), which is the finding's
// identity: two findings that cite the same file are two findings, and the
// ledger dedupes on the ref instead of guessing from evidence overlap (D74).
func importAuditFacts(workDir, report string) (total, imported int, err error) {
	data, readErr := os.ReadFile(filepath.Join(workDir, report, "findings.json"))
	if readErr != nil {
		return 0, 0, nil // no report: nothing to import, not an error
	}
	var findings []auditFinding
	if json.Unmarshal(data, &findings) != nil {
		return 0, 0, nil
	}
	total = len(findings)

	lines := make([]string, 0, len(findings))
	for _, f := range findings {
		status := strings.ToLower(strings.TrimSpace(f.Status))
		if status == "dismissed" {
			continue // an excluded candidate is not a conclusion
		}
		title := strings.TrimSpace(f.Title)
		if title == "" {
			continue
		}
		kind := "vuln"
		if status == "inconclusive" {
			kind = "note" // a lead with missing evidence, not a finding
		}
		confidence := "inferred"
		if status == "confirmed" {
			confidence = "observed"
		}
		proof := "static"
		if strings.EqualFold(strings.TrimSpace(f.Verification), "reproduced") {
			proof = "dynamic"
		}
		evidence := auditEvidence(workDir, report, f.Evidence)
		sourceRef := ""
		if id := strings.TrimSpace(f.ID); id != "" {
			sourceRef = "report:" + report + "#" + id
		}
		line, marshalErr := json.Marshal(map[string]any{
			"kind":       kind,
			"title":      title,
			"summary":    truncate(f.Impact, 300),
			"doc":        auditDoc(f, report),
			"confidence": confidence,
			"proof":      proof,
			"source_ref": sourceRef,
			"evidence":   evidence,
		})
		if marshalErr != nil {
			continue
		}
		lines = append(lines, string(line))
	}
	if len(lines) == 0 {
		return total, 0, nil
	}

	dir := filepath.Join(workDir, ".tw")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return total, 0, err
	}
	fh, err := os.OpenFile(filepath.Join(dir, "facts.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return total, 0, err
	}
	defer fh.Close()
	for _, line := range lines {
		if _, err := fh.WriteString(line + "\n"); err != nil {
			return total, 0, err
		}
	}
	return total, len(lines), nil
}

// auditDoc is what the console and the report prompt show for one finding: the
// audit's own fields, flattened, plus a pointer back to the report.
func auditDoc(f auditFinding, report string) string {
	var b strings.Builder
	head := []string{}
	if s := strings.TrimSpace(f.Severity); s != "" {
		head = append(head, "严重度 "+s)
	}
	if loc := strings.TrimSpace(f.Location); loc != "" {
		head = append(head, "位置 `"+loc+"`")
	}
	if v := strings.TrimSpace(f.Verification); v != "" {
		head = append(head, "验证 "+v)
	}
	if len(head) > 0 {
		b.WriteString(strings.Join(head, " · ") + "\n\n")
	}
	add := func(label, value string) {
		if v := strings.TrimSpace(value); v != "" {
			b.WriteString(label + "：" + v + "\n\n")
		}
	}
	add("前置条件", f.Preconditions)
	if len(f.Trace) > 0 {
		var trace []string
		for _, t := range f.Trace {
			if t = strings.TrimSpace(t); t != "" {
				trace = append(trace, t)
			}
		}
		if len(trace) > 0 {
			b.WriteString("路径：" + strings.Join(trace, " → ") + "\n\n")
		}
	}
	add("影响", f.Impact)
	add("复现", f.Reproduction)
	add("建议", f.Recommendation)
	fmt.Fprintf(&b, "报告：`%s/index.md`", report)
	if id := strings.TrimSpace(f.ID); id != "" {
		fmt.Fprintf(&b, "（%s）", id)
	}
	return strings.TrimSpace(b.String())
}

// auditEvidence keeps only the citations that are real files inside the report,
// rewritten relative to the case root so the ledger can open them.
func auditEvidence(workDir, report string, cited []string) []string {
	out := make([]string, 0, len(cited))
	for _, p := range cited {
		rel, ok := auditRel(p)
		if !ok {
			continue
		}
		full := filepath.Join(workDir, report, filepath.FromSlash(rel))
		if st, err := os.Stat(full); err != nil || st.IsDir() {
			continue
		}
		out = append(out, filepath.ToSlash(filepath.Join(report, filepath.FromSlash(rel))))
	}
	return out
}

// auditRel accepts a report-relative citation and refuses everything that could
// leave the report: absolute paths, drive letters and ".." segments.
func auditRel(p string) (string, bool) {
	rel := filepath.ToSlash(strings.TrimSpace(p))
	if rel == "" || strings.HasPrefix(rel, "/") || filepath.IsAbs(rel) {
		return "", false
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", false
		}
	}
	return rel, true
}
