package executor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestAuditCommandHasNoAgentSubcommand pins the difference that made a plain
// "bin points at cyber-audit" node impossible: the audit binary runs the audit
// directly, and its report directory must be per run (D47).
func TestAuditCommandHasNoAgentSubcommand(t *testing.T) {
	a := Audit{Extra: []string{"--debug"}, Timeout: 90 * time.Second}
	args := a.command("/cases/i-1", "audit-1", ".tw/audit-prompt.txt", []string{"src-audit"})

	if args[0] == "agent" {
		t.Fatal("audit command must not start with the aiscan `agent` subcommand")
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--workdir /cases/i-1",
		"--report-dir " + filepath.Join("/cases/i-1", "audit-1"),
		"--output-format stream-json",
		"--timeout 90",
		"-p .tw/audit-prompt.txt",
		"-s src-audit",
		"--debug",
	} {
		// filepath.Join on Windows renders /cases/i-1\audit-1, so compare the
		// report flag by its final segment instead of the full path.
		if strings.Contains(want, "/cases/i-1") {
			continue
		}
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %v", want, args)
		}
	}
	if !strings.Contains(joined, "audit-1") {
		t.Fatalf("missing report directory in %v", args)
	}
	if got := args[len(args)-1]; got != "--debug" {
		t.Fatalf("extra args must come last, got %q", got)
	}
}

// TestImportAuditFacts is the contract between cyber-audit's report and the
// ledger: confirmed/candidate become vuln facts, inconclusive becomes a note,
// dismissed is dropped, and only citations that exist are attached.
func TestImportAuditFacts(t *testing.T) {
	dir := t.TempDir()
	report := "audit-20260101-000000"
	if err := os.MkdirAll(filepath.Join(dir, report, "evidence"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, report, "evidence", "AUD-001.md"), []byte("proof"), 0o644); err != nil {
		t.Fatal(err)
	}
	findings := `[
 {"id":"AUD-001","title":"SQL injection","status":"confirmed","severity":"high",
  "location":"app.py:4","preconditions":"attacker controls name",
  "trace":["input -> SQL"],"impact":"read any row",
  "evidence":["evidence/AUD-001.md","evidence/missing.md"],
  "verification":"reproduced","reproduction":"curl ...","recommendation":"parameterize"},
 {"id":"AUD-002","title":"Maybe dangerous","status":"inconclusive","severity":"low",
  "impact":"unknown","evidence":[],"verification":"not_attempted"},
 {"id":"AUD-003","title":"Not a finding","status":"dismissed","impact":"none","evidence":[]}
]`
	if err := os.WriteFile(filepath.Join(dir, report, "findings.json"), []byte(findings), 0o644); err != nil {
		t.Fatal(err)
	}

	total, imported, err := importAuditFacts(dir, report)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || imported != 2 {
		t.Fatalf("total/imported = %d/%d, want 3/2", total, imported)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".tw", "facts.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("facts lines = %d, want 2: %s", len(lines), data)
	}

	var first struct {
		Kind       string   `json:"kind"`
		Title      string   `json:"title"`
		SourceRef  string   `json:"source_ref"`
		Confidence string   `json:"confidence"`
		Proof      string   `json:"proof"`
		Doc        string   `json:"doc"`
		Evidence   []string `json:"evidence"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	if first.Kind != "vuln" || first.Confidence != "observed" || first.Proof != "dynamic" {
		t.Fatalf("confirmed finding mapped wrong: %+v", first)
	}
	if first.Title != "SQL injection" {
		t.Fatalf("title = %q", first.Title)
	}
	if first.SourceRef != "report:"+report+"#AUD-001" {
		t.Fatalf("source_ref = %q: the finding identity must be stable and explicit", first.SourceRef)
	}
	if !strings.Contains(first.Doc, "app.py:4") || !strings.Contains(first.Doc, report) {
		t.Fatalf("doc lost location/report: %q", first.Doc)
	}
	wantEvidence := filepath.ToSlash(filepath.Join(report, "evidence", "AUD-001.md"))
	if len(first.Evidence) != 1 || first.Evidence[0] != wantEvidence {
		t.Fatalf("evidence = %v, want only %q", first.Evidence, wantEvidence)
	}

	var second struct {
		Kind      string `json:"kind"`
		SourceRef string `json:"source_ref"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatal(err)
	}
	if second.Kind != "note" {
		t.Fatalf("inconclusive finding must become a note, got %q", second.Kind)
	}
	if second.SourceRef != "report:"+report+"#AUD-002" {
		t.Fatalf("inconclusive source_ref = %q", second.SourceRef)
	}
	// The second run appends rather than replacing: a retry declares its own
	// findings only, and the ledger reads from its stored offset.
	if _, imported2, err := importAuditFacts(dir, report); err != nil || imported2 != 2 {
		t.Fatalf("re-import = %d, %v", imported2, err)
	}
	again, _ := os.ReadFile(filepath.Join(dir, ".tw", "facts.jsonl"))
	if n := len(strings.Split(strings.TrimSpace(string(again)), "\n")); n != 4 {
		t.Fatalf("append changed line count to %d, want 4", n)
	}
}

// TestImportAuditFactsWithoutReport: a failed or report-less run must not
// invent facts.
func TestImportAuditFactsWithoutReport(t *testing.T) {
	total, imported, err := importAuditFacts(t.TempDir(), "audit-none")
	if err != nil || total != 0 || imported != 0 {
		t.Fatalf("got %d/%d/%v, want 0/0/nil", total, imported, err)
	}
}

func TestAuditRelRejectsEscapes(t *testing.T) {
	for _, bad := range []string{"", "../x", "evidence/../../x", "/etc/passwd", `C:\Windows\win.ini`, "a/./b"} {
		if _, ok := auditRel(bad); ok {
			t.Fatalf("auditRel(%q) accepted an escaping path", bad)
		}
	}
	for _, good := range []string{"evidence/AUD-001.md", "raw/osv.json"} {
		if _, ok := auditRel(good); !ok {
			t.Fatalf("auditRel(%q) rejected a valid report path", good)
		}
	}
}

// Two findings that cite the same report file are still two findings. The old
// evidence-overlap guard silently dropped the second one; identity now comes
// from source_ref, not from which file the finding happened to quote.
func TestImportAuditFactsKeepsFindingsThatShareEvidence(t *testing.T) {
	dir := t.TempDir()
	report := "audit-shared"
	if err := os.MkdirAll(filepath.Join(dir, report, "evidence"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, report, "evidence", "AUD-001.md"), []byte("proof"), 0o644); err != nil {
		t.Fatal(err)
	}
	findings := `[
	 {"id":"AUD-001","title":"第一个","status":"confirmed","evidence":["evidence/AUD-001.md"]},
	 {"id":"AUD-002","title":"第二个","status":"confirmed","evidence":["evidence/AUD-001.md"]}
	]`
	if err := os.WriteFile(filepath.Join(dir, report, "findings.json"), []byte(findings), 0o644); err != nil {
		t.Fatal(err)
	}
	total, imported, err := importAuditFacts(dir, report)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || imported != 2 {
		t.Fatalf("total/imported = %d/%d, want 2/2: a shared evidence file is not a shared finding", total, imported)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".tw", "facts.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("facts lines = %d, want 2: %s", len(lines), data)
	}
	var a, b struct {
		SourceRef string `json:"source_ref"`
	}
	_ = json.Unmarshal([]byte(lines[0]), &a)
	_ = json.Unmarshal([]byte(lines[1]), &b)
	if a.SourceRef == b.SourceRef || a.SourceRef == "" || b.SourceRef == "" {
		t.Fatalf("findings must keep distinct source refs: %q vs %q", a.SourceRef, b.SourceRef)
	}
}

// A finding without evidence still has an identity when the report gives it an
// id; the ledger dedupes on that ref, not by dropping evidence-less rows.
func TestImportAuditFactsKeepsFindingsWithoutEvidence(t *testing.T) {
	dir := t.TempDir()
	report := "audit-bare"
	if err := os.MkdirAll(filepath.Join(dir, report), 0o755); err != nil {
		t.Fatal(err)
	}
	findings := `[
	 {"id":"AUD-001","title":"无证据线索","status":"inconclusive","evidence":[]},
	 {"id":"AUD-002","title":"另一条线索","status":"inconclusive","evidence":[]}
	]`
	if err := os.WriteFile(filepath.Join(dir, report, "findings.json"), []byte(findings), 0o644); err != nil {
		t.Fatal(err)
	}
	total, imported, err := importAuditFacts(dir, report)
	if err != nil || total != 2 || imported != 2 {
		t.Fatalf("total/imported/err = %d/%d/%v, want 2/2/nil", total, imported, err)
	}
}
