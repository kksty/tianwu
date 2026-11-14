package ledger

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/model"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadDeclaredValidation(t *testing.T) {
	casesRoot := t.TempDir()
	dir := filepath.Join(casesRoot, "in_1")
	writeFile(t, filepath.Join(dir, "admin-200.html"), "<html></html>")
	writeFile(t, filepath.Join(dir, HandoffDir, HandoffFile), `
{"kind":"vuln","title":"/admin 越权","summary":"普通用户可读","doc":"GET /admin","evidence":["admin-200.html"],"confidence":"observed"}
{"kind":"host","title":"10.0.0.5"}
{"kind":"banana","title":"不在枚举里"}
not json at all
{"kind":"note","title":"证据不存在","evidence":["nope.txt"]}
{"kind":"note","title":"路径逃逸","evidence":["../../etc/passwd"]}
{"kind":"vuln","title":"/admin 越权","summary":"重复"}
{"kind":"playbook","title":"ThinkPHP RCE","summary":"5.0.x","doc":"curl ...","tags":["product:thinkphp","technique:rce"]}
`)
	d, err := ReadDeclared(casesRoot, dir)
	if err != nil {
		t.Fatalf("ReadDeclared: %v", err)
	}
	if len(d.Facts) != 5 {
		t.Fatalf("want 5 kept facts, got %d: %+v", len(d.Facts), d.Facts)
	}
	if d.Bad != 3 {
		t.Fatalf("want 3 rejected lines, got %d", d.Bad)
	}
	if got := d.Facts[0].Evidence; len(got) != 1 || got[0] != "admin-200.html" {
		t.Fatalf("evidence not kept: %v", got)
	}
	if d.Facts[1].Confidence != "observed" {
		t.Fatalf("default confidence not applied: %q", d.Facts[1].Confidence)
	}
	if len(d.Facts[4].Tags) != 2 || d.Facts[4].Tags[0] != "product:thinkphp" {
		t.Fatalf("playbook tags not carried: %+v", d.Facts[4].Tags)
	}
	// Same title is not the same observation: each declared line has its own
	// stable declaration identity, and the ledger no longer merges them.
	if d.Facts[0].Title != d.Facts[3].Title || d.Facts[0].DeclarationID == d.Facts[3].DeclarationID {
		t.Fatalf("two same-titled declarations must stay distinct: %+v", d.Facts)
	}
	// The missing file no longer deletes the fact: it becomes a dangling ref.
	missing := d.Facts[2]
	if missing.Title != "证据不存在" || len(missing.Evidence) != 0 {
		t.Fatalf("dangling fact = %+v", missing)
	}
	if len(missing.Refs) != 1 || missing.Refs[0].Scheme != model.RefFile || missing.Refs[0].Locator != "nope.txt" {
		t.Fatalf("dangling refs = %+v", missing.Refs)
	}
}

func TestReadDeclaredMissingFileIsNotAnError(t *testing.T) {
	casesRoot := t.TempDir()
	d, err := ReadDeclared(casesRoot, filepath.Join(casesRoot, "in_1"))
	if err != nil {
		t.Fatalf("missing handoff must not error: %v", err)
	}
	if len(d.Facts) != 0 {
		t.Fatalf("want 0 facts, got %d", len(d.Facts))
	}
}

func TestReadDeclaredArtifactAndProof(t *testing.T) {
	casesRoot := t.TempDir()
	dir := filepath.Join(casesRoot, "in_1")
	writeFile(t, filepath.Join(dir, "fw.bin"), "\x7fELF")
	writeFile(t, filepath.Join(dir, HandoffDir, HandoffFile), `
{"kind":"artifact","title":"固件 fw.bin","summary":"MIPS 路由器固件","evidence":["fw.bin"]}
{"kind":"vuln","title":"httpd 栈溢出","doc":"rizin -q -c 'pdf @ sym.handler' fw.bin","proof":"static","evidence":["fw.bin"]}
{"kind":"vuln","title":"拼错证明方式","proof":"statc","evidence":["fw.bin"]}
`)
	d, err := ReadDeclared(casesRoot, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Facts) != 3 {
		t.Fatalf("want 3 facts, got %d: %+v", len(d.Facts), d.Facts)
	}
	if d.Facts[0].Kind != "artifact" {
		t.Fatalf("artifact kind rejected: %+v", d.Facts[0])
	}
	if d.Facts[1].Proof != "static" {
		t.Fatalf("static proof not kept: %q", d.Facts[1].Proof)
	}
	// A typo must fall back to the stricter proof, never to the looser one.
	if d.Facts[0].Proof != "dynamic" || d.Facts[2].Proof != "dynamic" {
		t.Fatalf("proof default wrong: %q / %q", d.Facts[0].Proof, d.Facts[2].Proof)
	}
}

// The boundary for a declared evidence path is the project cases tree, not the
// run's own case directory: graph.json writes every evidence URI relative to the
// case directory ("../in_x/file", evidence_base=case_dir), so a sibling case is
// a legitimate citation. A file that sits directly in the cases root belongs to
// no case and is still refused.
func TestResolveEvidenceClassifies(t *testing.T) {
	casesRoot := t.TempDir()
	dir := filepath.Join(casesRoot, "in_a")
	writeFile(t, filepath.Join(dir, "ok.txt"), "x")
	writeFile(t, filepath.Join(dir, HandoffDir, "ok.txt"), "x")
	writeFile(t, filepath.Join(casesRoot, "in_b", "proof.txt"), "x")
	writeFile(t, filepath.Join(filepath.Dir(casesRoot), "outside.txt"), "x")
	writeFile(t, filepath.Join(casesRoot, "direct.txt"), "x")

	// Absolute paths, paths that leave the project cases tree, and files
	// sitting directly in the cases root are not citations: dropping the line
	// is the only safe reading of a claim that points outside a case. (An
	// absolute path is spelled differently per platform, so the test builds
	// one.)
	for _, name := range []string{
		"",
		"../../outside.txt",
		"../direct.txt",
		casesRoot,
		filepath.Join(dir, "ok.txt"),
	} {
		if got, state := resolveEvidence(casesRoot, dir, name); state != evidenceInvalid {
			t.Fatalf("resolveEvidence(%q) = %q/%v, want invalid", name, got, state)
		}
	}
	// A sibling case, spelled the way graph.json spells it, is inside the
	// project cases tree and therefore a real citation.
	if got, state := resolveEvidence(casesRoot, dir, "../in_b/proof.txt"); state != evidenceOK || got != "../in_b/proof.txt" {
		t.Fatalf("sibling case = %q/%v, want ok", got, state)
	}
	if got, state := resolveEvidence(casesRoot, dir, "../in_b/gone.txt"); state != evidenceMissing || got != "../in_b/gone.txt" {
		t.Fatalf("missing sibling = %q/%v, want missing", got, state)
	}
	// A clean path that is gone is a dangling citation, not a malformed one:
	// the fact survives, and the report can say what it claimed (D44).
	got, state := resolveEvidence(casesRoot, dir, "missing.txt")
	if state != evidenceMissing || got != "missing.txt" {
		t.Fatalf("missing = %q/%v", got, state)
	}
	if got, state := resolveEvidence(casesRoot, dir, "ok.txt"); state != evidenceOK || got != "ok.txt" {
		t.Fatalf("ok = %q/%v", got, state)
	}
	// The auto-collector skips .tw/ and .cyber/ so control files never become
	// evidence by accident. A *declared* path is different: the ledger only
	// asks whether it opens, so a file under .tw counts when it is named
	// exactly. The dispatch prompt therefore tells the model to keep evidence
	// at the case root, and to spell the prefix when it does not.
	if got, state := resolveEvidence(casesRoot, dir, ".tw/ok.txt"); state != evidenceOK || got != ".tw/ok.txt" {
		t.Fatalf("declared control-dir file = %q/%v, want ok", got, state)
	}
}

// A case directory outlives its runs: a retry and a steered continuation both
// append to the same handoff. Each run must declare only what it appended,
// otherwise the ledger doubles every fact the first turn found (D42).
func TestReadDeclaredSinceOnlyNewLines(t *testing.T) {
	casesRoot := t.TempDir()
	dir := filepath.Join(casesRoot, "in_1")
	path := filepath.Join(dir, HandoffDir, HandoffFile)
	writeFile(t, path, `{"kind":"note","title":"第一轮"}`+"\n")

	first, err := ReadDeclaredSince(casesRoot, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Facts) != 1 || first.Facts[0].Title != "第一轮" {
		t.Fatalf("first run = %+v", first.Facts)
	}
	if err := CommitDeclared(dir, first.Consumed); err != nil {
		t.Fatal(err)
	}

	// The continuation appends one fact; it must not see the first one again.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"kind":"note","title":"第二轮"}` + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	second, err := ReadDeclaredSince(casesRoot, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Facts) != 1 || second.Facts[0].Title != "第二轮" {
		t.Fatalf("second run = %+v, want only the appended line", second.Facts)
	}
}

// The agent may rewrite the handoff with `write` instead of appending. A file
// shorter than the recorded offset is a restart, not a run with no facts.
func TestReadDeclaredSinceRewrittenFile(t *testing.T) {
	casesRoot := t.TempDir()
	dir := filepath.Join(casesRoot, "in_1")
	path := filepath.Join(dir, HandoffDir, HandoffFile)
	writeFile(t, path, `{"kind":"note","title":"很长的第一轮"}`+"\n")
	if err := CommitDeclared(dir, 4096); err != nil {
		t.Fatal(err)
	}
	d, err := ReadDeclaredSince(casesRoot, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Facts) != 1 || d.Facts[0].Title != "很长的第一轮" {
		t.Fatalf("rewritten file was skipped: %+v", d.Facts)
	}
}

// A fact may cite a live URL or a tool artifact instead of a file. That used to
// drop the whole fact; now the citation is kept as a ref so the report can put
// it in "scope and limitations" — and the fact stays candidate, because a ref
// is not proof (D44).
func TestReadDeclaredEvidenceWithSource(t *testing.T) {
	casesRoot := t.TempDir()
	dir := filepath.Join(casesRoot, "in_1")
	writeFile(t, filepath.Join(dir, "admin-200.html"), "<html>ok</html>")
	writeFile(t, filepath.Join(dir, "scan.json"), "{}")
	writeFile(t, filepath.Join(dir, HandoffDir, HandoffFile), `
{"kind":"vuln","title":"带出处","evidence":[{"file":"admin-200.html","url":"https://target/admin"}]}
{"kind":"loot","title":"裸路径仍然等价","evidence":["scan.json"]}
{"kind":"vuln","title":"只有地址没有文件","evidence":[{"url":"https://target/no-snapshot"}]}
{"kind":"vuln","title":"artifact 等库","evidence":[{"artifact":"sha256:abc"}]}
`)
	d, err := ReadDeclared(casesRoot, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Facts) != 4 {
		t.Fatalf("want 4 kept facts, got %d: %+v", len(d.Facts), d.Facts)
	}
	if d.Bad != 0 {
		t.Fatalf("want 0 rejected lines, got %d", d.Bad)
	}
	if got := d.Facts[0].From["admin-200.html"]; got != "url:https://target/admin" {
		t.Fatalf("source = %q", got)
	}
	if d.Facts[1].From != nil {
		t.Fatalf("a bare path has no source, got %v", d.Facts[1].From)
	}
	// A file with an origin has bytes; the origin stays provenance, not a ref.
	if len(d.Facts[0].Refs) != 0 {
		t.Fatalf("resolved file must not add refs: %+v", d.Facts[0].Refs)
	}
	// The two file-less facts keep their citation and no evidence.
	for i, want := range []model.Ref{
		{Scheme: model.RefURL, Locator: "https://target/no-snapshot"},
		{Scheme: model.RefArtifact, Locator: "sha256:abc"},
	} {
		f := d.Facts[2+i]
		if len(f.Evidence) != 0 {
			t.Fatalf("%s has evidence %v", f.Title, f.Evidence)
		}
		if len(f.Refs) != 1 || f.Refs[0] != want {
			t.Fatalf("%s refs = %+v, want %+v", f.Title, f.Refs, want)
		}
	}
}

// A vanished file keeps both the file it named and the lead it came from.
func TestReadDeclaredDanglingFileKeepsOrigin(t *testing.T) {
	casesRoot := t.TempDir()
	dir := filepath.Join(casesRoot, "in_1")
	writeFile(t, filepath.Join(dir, HandoffDir, HandoffFile), `
{"kind":"vuln","title":"文件被删了","evidence":[{"file":"gone.html","url":"https://target/admin"}]}
`)
	d, err := ReadDeclared(casesRoot, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Facts) != 1 {
		t.Fatalf("facts = %+v", d.Facts)
	}
	want := []model.Ref{
		{Scheme: model.RefFile, Locator: "gone.html"},
		{Scheme: model.RefURL, Locator: "https://target/admin"},
	}
	got := d.Facts[0].Refs
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("refs = %+v, want %+v", got, want)
	}
}

func TestParseEvidenceRefsShapes(t *testing.T) {
	refs, ok := parseEvidenceRefs([]byte(`["a.txt",{"path":"b.txt","artifact":"sha256:x"}]`))
	if !ok || len(refs) != 2 {
		t.Fatalf("refs = %+v ok=%v", refs, ok)
	}
	if refs[0].File != "a.txt" || refs[1].File != "b.txt" || refs[1].Artifact != "sha256:x" {
		t.Fatalf("refs = %+v", refs)
	}
	if _, ok := parseEvidenceRefs([]byte(`{"file":"x"}`)); ok {
		t.Fatal("evidence must be a list")
	}
}

// The per-run fact cap is a budget, not a delete: the lines past it stay in the
// handoff and the next run of the same direction reads them. The offset used to
// be the file size before parsing, so everything past the cap vanished.
func TestTruncatedHandoffLeavesItsTailForTheNextRun(t *testing.T) {
	casesRoot := t.TempDir()
	dir := filepath.Join(casesRoot, "in_1")
	var b strings.Builder
	for i := 0; i <= MaxDeclaredFacts; i++ {
		fmt.Fprintf(&b, `{"kind":"note","title":"f%03d"}`+"\n", i)
	}
	writeFile(t, filepath.Join(dir, HandoffDir, HandoffFile), b.String())

	first, err := ReadDeclaredSince(casesRoot, dir)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Truncated || len(first.Facts) != MaxDeclaredFacts {
		t.Fatalf("truncated=%v facts=%d, want true/%d", first.Truncated, len(first.Facts), MaxDeclaredFacts)
	}
	if err := CommitDeclared(dir, first.Consumed); err != nil {
		t.Fatal(err)
	}

	second, err := ReadDeclaredSince(casesRoot, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Facts) != 1 || second.Facts[0].Title != fmt.Sprintf("f%03d", MaxDeclaredFacts) {
		t.Fatalf("tail after the cap was lost: %+v", second.Facts)
	}
}

// A line without a trailing newline may still be in flight while the run is
// live: the watcher must leave it for the next poll, while the finish path takes
// the last complete line.
func TestLiveWatchLeavesTheUnterminatedLine(t *testing.T) {
	casesRoot := t.TempDir()
	dir := filepath.Join(casesRoot, "in_1")
	path := filepath.Join(dir, HandoffDir, HandoffFile)
	writeFile(t, path, `{"kind":"note","title":"done"}`+"\n")
	first, err := ReadDeclaredFrom(casesRoot, dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Facts) != 1 || first.Consumed != int64(len(`{"kind":"note","title":"done"}`)+1) {
		t.Fatalf("facts=%+v consumed=%d", first.Facts, first.Consumed)
	}

	writeFile(t, path, `{"kind":"note","title":"done"}`+"\n"+`{"kind":"note","title":"typing`)
	live, err := ReadDeclaredFrom(casesRoot, dir, first.Consumed)
	if err != nil {
		t.Fatal(err)
	}
	if len(live.Facts) != 0 || live.Consumed != first.Consumed {
		t.Fatalf("the half-written line must wait: facts=%+v consumed=%d", live.Facts, live.Consumed)
	}
}

// Declaration identity is writer-supplied: an explicit id lets the same
// observation be re-declared (for example to add evidence or correct a summary)
// while two different declarations keep two rows even with one title. A repeated
// identity inside a batch is still one fact — the later line supersedes the
// earlier one, which is the writer's correction channel (F8); it is reported,
// never silently merged, and never becomes a second row.
func TestDeclaredIdentityAndObservationMetadata(t *testing.T) {
	casesRoot := t.TempDir()
	dir := filepath.Join(casesRoot, "in_1")
	writeFile(t, filepath.Join(dir, HandoffDir, HandoffFile),
		`{"kind":"vuln","title":"同一观察","declaration_id":"obs-1","observed_at":"2026-11-01T09:00:00Z","execution_context_ref":"browser:ac_1","summary":"第一次"}`+"\n"+
			`{"kind":"vuln","title":"同一观察","declaration_id":"obs-1","observed_at":"2026-11-01T09:00:00Z","execution_context_ref":"browser:ac_1","summary":"第二次"}`+"\n"+
			`{"kind":"vuln","title":"同一观察","declaration_id":"obs-2"}`+"\n"+
			`{"kind":"vuln","title":"坏的观察","declaration_id":"obs-3","observed_at":"not-a-time"}`+"\n")
	d, err := ReadDeclared(casesRoot, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Facts) != 2 {
		t.Fatalf("want the two distinct declarations, got %d: %+v", len(d.Facts), d.Facts)
	}
	if d.Bad != 1 {
		t.Fatalf("want only the bad timestamp rejected, got %d", d.Bad)
	}
	first := d.Facts[0]
	if first.DeclarationID != "obs-1" || first.ObservedAt == nil || first.ObservedAt.Format(time.RFC3339) != "2026-11-01T09:00:00Z" {
		t.Fatalf("observation metadata lost: %+v", first)
	}
	if first.ExecutionContextRef != "browser:ac_1" {
		t.Fatalf("execution context reference lost: %q", first.ExecutionContextRef)
	}
	if first.Summary != "第二次" {
		t.Fatalf("repeated identity did not supersede the earlier line: summary = %q", first.Summary)
	}
	joined := strings.Join(d.Infos, "\n")
	if !strings.Contains(joined, "同 ID") || !strings.Contains(joined, "obs-1") {
		t.Fatalf("supersession was not reported as info: %v", d.Infos)
	}
	if len(d.Warnings) != 0 {
		t.Fatalf("an expected in-batch correction became a warning: %v", d.Warnings)
	}
	if d.Facts[1].DeclarationID != "obs-2" || d.Facts[1].DeclarationID == first.DeclarationID {
		t.Fatalf("two declarations with one title collapsed: %+v", d.Facts)
	}
}
