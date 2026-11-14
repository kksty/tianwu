package ledger

import (
	"bufio"
	"encoding/json"
	"fmt"
	"github.com/kksty/tianwu/internal/artifact"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kksty/tianwu/internal/model"
)

// HandoffDir / HandoffFile locate the executor's own account of what it found.
//
// The handoff exists because cyber-harness has no submit_fact tool: the agent
// writes prose and files. Asking it to write one JSON object per line is the
// cheapest structured channel that survives a tool wrapper change.
const (
	HandoffDir  = ".tw"
	HandoffFile = "facts.jsonl"
	// HandoffOffset marks how much of the handoff earlier runs already
	// consumed. A case directory outlives its runs — a retry, and now a steer,
	// both work in the same one — so re-reading the file from byte 0 would
	// declare last turn's facts again (D42).
	HandoffOffset = "facts.offset"
)

// MaxDeclaredFacts caps one run's handoff. A runaway loop must not be able to
// flood the ledger through the one channel we trust.
const MaxDeclaredFacts = 200

// DeclaredFact is one line of the handoff file.
type DeclaredFact struct {
	DeclarationID       string                   `json:"declaration_id,omitempty"`
	ObservedAt          *time.Time               `json:"observed_at,omitempty"`
	ExecutionContextRef string                   `json:"execution_context_ref,omitempty"`
	EntityRef           string                   `json:"entity_ref,omitempty"`
	FromFactIDs         []string                 `json:"from_fact_ids,omitempty"`
	Pattern             *model.ExperiencePattern `json:"pattern,omitempty"`
	Offset              int64                    `json:"-"`
	RequiredTags        []string                 `json:"required_tags,omitempty"`
	Location            string                   `json:"-"`
	Kind                string                   `json:"kind"`
	Title               string                   `json:"title"`
	Summary             string                   `json:"summary"`
	Doc                 string                   `json:"doc"`
	Confidence          string                   `json:"confidence"`
	// SourceRef is the writer's stable identity for this fact, when it has one
	// (an audit finding: "report:<reportID>#<findingID>"). The ledger dedupes
	// on it before falling back to (kind,title) (D74).
	SourceRef string `json:"source_ref"`
	// Proof is "dynamic" (default) or "static". Static findings are reproduced
	// by replaying the command over the same artifact, never by attacking a
	// live target (D35).
	Proof string `json:"proof"`
	// Tags are optional labels; playbooks use them for recall.
	Tags []string `json:"tags"`
	// Outcome is playbook-only: success (default) or dead_end ("this path does
	// not work unless the premise changes"). It is ignored for fact kinds.
	Outcome string `json:"outcome"`

	// EvidenceRaw is the line's own `evidence` value: a list of bare paths (the
	// original shape) or objects that also say where the bytes came from.
	EvidenceRaw json.RawMessage `json:"evidence"`
	// Evidence is the resolved list of files under the project cases tree, each
	// spelled relative to workDir: a file in the run's own case dir stays
	// "raw/x", a sibling case keeps the "../in_x/..." hop (graph.json's form).
	Evidence []string `json:"-"`
	// From maps a resolved file to its provenance, when the model declared one
	// ("url:<u>" or "artifact:<id>"). It is recorded, never guessed.
	From map[string]string `json:"-"`
	// Refs are the declared citations that produced no file: a url, an
	// artifact id, or a file that no longer exists. They do not make the fact
	// true — they make it checkable by a human, which is why the fact is kept
	// instead of dropped (D44).
	Refs []model.Ref `json:"-"`
}

// EvidenceRef is one entry of a fact's evidence list after both accepted shapes
// are folded together:
//
//	"admin-200.html"
//	{"file":"admin-200.html","url":"https://target/admin"}
//	{"url":"https://target/admin"}          // no bytes, a lead
//
// The file is what the ledger can point at. The url/artifact is where the bytes
// came from — the model's judgment about relevance, recorded but not audited
// (D44). An entry with no file has no bytes at all; it survives as a ref.
type EvidenceRef struct {
	File     string
	URL      string
	Artifact string
}

// parseEvidenceRefs accepts both shapes. An object without a file names a
// source but no artifact the ledger can open; resolveEvidenceRefs turns it into
// a ref instead of rejecting the line.
func parseEvidenceRefs(raw json.RawMessage) ([]EvidenceRef, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, true
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, false
	}
	out := make([]EvidenceRef, 0, len(items))
	for _, item := range items {
		trimmed := strings.TrimSpace(string(item))
		if strings.HasPrefix(trimmed, `"`) {
			var path string
			if err := json.Unmarshal(item, &path); err != nil {
				return nil, false
			}
			out = append(out, EvidenceRef{File: path})
			continue
		}
		var obj struct {
			File     string `json:"file"`
			Path     string `json:"path"`
			URL      string `json:"url"`
			Artifact string `json:"artifact"`
		}
		if err := json.Unmarshal(item, &obj); err != nil {
			return nil, false
		}
		file := strings.TrimSpace(obj.File)
		if file == "" {
			file = strings.TrimSpace(obj.Path)
		}
		out = append(out, EvidenceRef{
			File:     file,
			URL:      strings.TrimSpace(obj.URL),
			Artifact: strings.TrimSpace(obj.Artifact),
		})
	}
	return out, true
}

// Declared is the parsed handoff of one run.
type Declared struct {
	Facts []DeclaredFact
	Lines int // non-empty lines read
	// Bad counts lines rejected outright: broken JSON, empty title, unknown
	// kind, or a path that is absolute or escapes the project cases tree. A
	// citation that merely has no bytes is not "bad" — it becomes a ref on the
	// fact (D44).
	Bad       int
	Truncated bool
	// Warnings are the per-line reasons a declared evidence path could not be
	// registered (out of bounds, missing file, unparseable list). They are
	// collected for the run row so the next turn can correct the path instead
	// of the problem disappearing into a count (task-17).
	Warnings []string
	// Infos are non-problems worth surfacing without turning them into a
	// warning: today, one aggregated line when the writer corrected the same
	// observation by re-declaring its identity in one batch. The later line is
	// authoritative (F8), so this is a note about which line won, not a defect
	// to fix. Keeping it out of Warnings is what stops the status page from
	// counting expected corrections as handoff failures.
	Infos []string
	// Consumed is where the next run should start reading this file.
	Consumed int64
}

var factKinds = map[string]bool{
	"host": true, "service": true, "cred": true,
	"vuln": true, "loot": true, "artifact": true, "note": true,
	// playbook is a reusable technique/method the executor chose to write down.
	// It is a valid handoff line but not a fact: the ledger routes it to the
	// project playbook table (D70), and the next brief carries its text.
	"playbook": true,
}

// HandoffPath is where the executor writes its declarations.
func HandoffPath(workDir string) string {
	return filepath.Join(workDir, HandoffDir, HandoffFile)
}

// ReadDeclared parses <workDir>/.tw/facts.jsonl from the beginning. It is the
// whole-file view, used by tests and by anything that wants the raw handoff;
// a run must use ReadDeclaredSince.
//
// casesRoot is the project cases directory — the parent of every run's case
// directory — and is the real boundary for declared evidence: graph.json spells
// evidence URIs relative to the case directory, so a sibling case
// ("../in_x/file") is a legal citation.
func ReadDeclared(casesRoot, workDir string) (Declared, error) {
	return readDeclared(casesRoot, workDir, 0, true)
}

// ReadDeclaredSince parses only the lines the previous runs did not consume,
// and reports where this one stopped. That is what makes a second run in the
// same case (a retry, or a steered continuation) declare only its own facts.
func ReadDeclaredSince(casesRoot, workDir string) (Declared, error) {
	return readDeclared(casesRoot, workDir, readOffset(workDir), true)
}

// ReadDeclaredFrom parses the handoff starting at an explicit byte offset. The
// live watcher uses it with Consumed() so it only sees lines the current run
// appended, not the previous run's. It is not final: a trailing line without a
// newline is left for the next poll, because the executor may still be writing
// it.
func ReadDeclaredFrom(casesRoot, workDir string, start int64) (Declared, error) {
	return readDeclared(casesRoot, workDir, start, false)
}

// Consumed is the byte offset the finish path last committed. A live watcher
// starts here so it does not re-add facts an earlier run already settled.
func Consumed(workDir string) int64 { return readOffset(workDir) }

// CommitDeclared records how far the handoff has been consumed. A failure here
// costs duplicated facts on the next run, not a lost one, so it is best-effort.
func CommitDeclared(workDir string, consumed int64) error {
	if workDir == "" {
		return nil
	}
	path := filepath.Join(workDir, HandoffDir, HandoffOffset)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strconv.FormatInt(consumed, 10)), 0o644)
}

func readOffset(workDir string) int64 {
	if workDir == "" {
		return 0
	}
	b, err := os.ReadFile(filepath.Join(workDir, HandoffDir, HandoffOffset))
	if err != nil {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// readDeclared parses <workDir>/.tw/facts.jsonl. A missing file is not an
// error: it means the executor ignored the contract, which is reported as
// declared_facts=0 instead of failing the run.
//
// Validation is deliberately strict about the parts that make a line
// meaningless: broken JSON, an empty title, an unknown kind, or a path that
// leaves the project cases tree. It is deliberately *not* strict about missing
// bytes: a citation the ledger cannot open becomes a ref on the fact, because
// deleting the fact would delete the only trace that it ever saw anything (D44).
func readDeclared(casesRoot, workDir string, start int64, final bool) (Declared, error) {
	var d Declared
	f, err := os.Open(HandoffPath(workDir))
	if err != nil {
		if os.IsNotExist(err) {
			return d, nil
		}
		return d, err
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return d, err
	}
	// A file shorter than the recorded offset was rewritten (the agent may use
	// `write` instead of appending). Start over rather than losing the run.
	if start > st.Size() {
		start = 0
	}
	if start > 0 {
		if _, err := f.Seek(start, io.SeekStart); err != nil {
			return d, err
		}
	}
	// Consumed advances only past a line that was actually read. It used to be
	// set to the file size up front, so hitting the fact cap (or a scanner
	// error) committed the rest of the handoff as "seen" and silently dropped
	// every declaration after it (2026-11 audit).
	consumed := start
	d.Consumed = consumed

	// seen maps a declaration identity to its row in d.Facts, so a repeated
	// identity supersedes the earlier line in place instead of adding a second
	// row (F8).
	seen := map[string]int{}
	// superseded counts how many later lines replaced an earlier one, per
	// identity. The batch reports one aggregated info line instead of one
	// warning per correction: a writer that revises an observation is following
	// the documented contract, not creating a handoff problem.
	superseded := map[string]int{}
	supersededTotal := 0
	r := bufio.NewReaderSize(f, 64<<10)
	for {
		line, n, complete, readErr := readHandoffLine(r)
		if readErr != nil && readErr != io.EOF {
			return d, readErr
		}
		if n == 0 && !complete {
			break
		}
		// A trailing line with no newline is either still being written (live
		// watcher) or the last line of a finished run. The watcher leaves it
		// for the next poll; the finish path takes it.
		if !complete && !final {
			break
		}
		text := strings.TrimSpace(string(line))
		if text != "" && len(d.Facts) >= MaxDeclaredFacts {
			// Do not consume this line: the next run of the same direction
			// still sees it instead of it disappearing into the offset.
			d.Truncated = true
			break
		}
		consumed += n
		d.Consumed = consumed
		if text == "" {
			if readErr == io.EOF {
				break
			}
			continue
		}
		d.Lines++

		if len(text) > maxHandoffLine {
			// readHandoffLine kept the bounded prefix; the line is not JSON we
			// can honour. It is consumed so the same oversize line cannot block
			// every later run, but it is counted as bad rather than silently cut.
			d.Bad++
			continue
		}
		var df DeclaredFact
		if err := json.Unmarshal([]byte(text), &df); err != nil {
			d.Bad++
			continue
		}
		df.Kind = strings.ToLower(strings.TrimSpace(df.Kind))
		df.Title = strings.TrimSpace(df.Title)
		df.SourceRef = strings.TrimSpace(df.SourceRef)
		if df.Title == "" || !factKinds[df.Kind] {
			d.Bad++
			continue
		}
		if df.Confidence != "observed" && df.Confidence != "inferred" {
			df.Confidence = "observed"
		}
		// Only "static" is honoured; anything else means the conclusion is
		// reproduced dynamically, which is the stricter claim and the safe
		// default for a typo.
		df.Proof = strings.ToLower(strings.TrimSpace(df.Proof))
		if df.Proof != "static" {
			df.Proof = "dynamic"
		}
		df.Outcome = strings.ToLower(strings.TrimSpace(df.Outcome))
		if df.Outcome != model.PlaybookDeadEnd {
			df.Outcome = model.PlaybookSuccess
		}
		ev, from, citations, evidenceWarnings, ok := resolveEvidenceRefs(casesRoot, workDir, df.Title, df.EvidenceRaw)
		d.Warnings = append(d.Warnings, evidenceWarnings...)
		if !ok {
			d.Bad++
			if len(evidenceWarnings) == 0 {
				d.Warnings = append(d.Warnings, fmt.Sprintf(
					"事实「%s」的证据列表无法解析，整行已跳过：证据路径必须相对本轮 case 目录且落在项目 cases 目录内",
					clipRunes(df.Title, 80)))
			}
			continue
		}
		df.Evidence = ev
		df.From = from
		df.Refs = citations

		df.Location = fmt.Sprintf(".tw/facts.jsonl#bytes=%d-%d", consumed-n, consumed)
		df.DeclarationID = strings.TrimSpace(df.DeclarationID)
		df.ExecutionContextRef = strings.TrimSpace(df.ExecutionContextRef)
		if df.ObservedAt != nil && df.ObservedAt.IsZero() {
			df.ObservedAt = nil
		}
		if len(df.DeclarationID) > maxDeclarationID || len(df.ExecutionContextRef) > maxExecutionContextRef {
			// An identity longer than this is not a stable token anyone can
			// retype; keeping it would put unbounded writer text in the ledger.
			d.Bad++
			d.Warnings = append(d.Warnings, fmt.Sprintf(
				"事实「%s」的 declaration_id 或 execution_context_ref 过长（上限 %d/%d 字符），整行已跳过",
				clipRunes(df.Title, 80), maxDeclarationID, maxExecutionContextRef))
			continue
		}
		if df.DeclarationID == "" {
			// Default identity is the line itself. The same finish/live read of
			// the same bytes resolves to one row; a new line is a new
			// observation even when it repeats a title. Source refs, when the
			// writer has one, outrank it in FindDeclaration.
			df.DeclarationID = df.Location
		}
		// A repeated identity inside one read is the writer re-declaring the
		// same observation — the documented way to correct a summary or add
		// evidence mid-run (F8). The later line supersedes the earlier one: one
		// observation stays one fact, but the batch must carry the newest
		// declaration, otherwise the finish settlement replaying the file would
		// overwrite a live correction with the stale first line. Which line won
		// is still reported, not silent.
		identity := "decl\x00" + df.DeclarationID
		if df.SourceRef != "" {
			identity = "ref\x00" + df.SourceRef
		}
		if idx, repeated := seen[identity]; repeated {
			df.Offset = consumed - n
			d.Facts[idx] = df
			superseded[df.DeclarationID]++
			supersededTotal++
			if readErr == io.EOF {
				break
			}
			continue
		}
		seen[identity] = len(d.Facts)
		df.Offset = consumed - n
		d.Facts = append(d.Facts, df)
		if readErr == io.EOF {
			break
		}
	}
	if supersededTotal > 0 {
		ids := make([]string, 0, len(superseded))
		for id := range superseded {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		parts := make([]string, 0, len(ids))
		for _, id := range ids {
			parts = append(parts, fmt.Sprintf("%s×%d", clipRunes(id, 80), superseded[id]))
		}
		d.Infos = append(d.Infos, fmt.Sprintf(
			"本批有 %d 条同 ID 声明被后一行覆盖（同一观察的更正，非错误）：%s",
			supersededTotal, strings.Join(parts, "、")))
	}
	return d, nil
}

// Bounds on writer-supplied identity strings. They are contract limits, not
// content budgets: the text of the claim itself is never truncated here.
const (
	maxDeclarationID       = 200
	maxExecutionContextRef = 300
)

// maxHandoffLine bounds one handoff line. A longer line is not a fact we can
// parse; it is counted as bad and skipped, not allowed to eat memory.
const maxHandoffLine = 1 << 20

// readHandoffLine reads one line, returning its bytes (at most maxHandoffLine),
// the bytes consumed including the newline, and whether the newline was seen.
// A final line without a newline reports complete=false with io.EOF.
func readHandoffLine(r *bufio.Reader) (line []byte, n int64, complete bool, err error) {
	for {
		chunk, readErr := r.ReadSlice('\n')
		n += int64(len(chunk))
		if len(line) < maxHandoffLine {
			room := maxHandoffLine - len(line)
			if len(chunk) > room {
				line = append(line, chunk[:room]...)
			} else {
				line = append(line, chunk...)
			}
		}
		switch readErr {
		case nil:
			return line, n, true, nil
		case bufio.ErrBufferFull:
			continue
		default:
			// io.EOF (or a real read error) with no newline.
			return line, n, false, readErr
		}
	}
}

// evidenceOutOfBoundsWarning is the reason text for a declared path that cannot
// be a citation of this project. The wording names the real boundary — the run's
// case directory, inside the project's cases directory — because that is what
// the next round must fix, and the tests match on it.
const evidenceOutOfBoundsWarning = "路径越界，必须相对本轮 case 目录且落在项目 cases 目录内"

// resolveEvidenceRefs sorts a fact's declared citations into the two shapes the
// ledger keeps apart:
//
//   - files that exist under the project cases tree (with their declared
//     origin), and
//   - refs that have no bytes: a url, an artifact id, or a file that is gone.
//
// It returns ok=false only for a malformed declaration — invalid JSON, an empty
// object, or a path that is absolute or falls outside the project cases tree.
// Those are bugs or attacks and dropping the line is the safe answer. A citation
// that merely has no bytes is *not* malformed: dropping it would delete the fact
// itself, which is how "the proof was a live URL" used to become "nothing was
// found".
//
// Every citation that cannot be opened is also reported as a warning (with the
// fact title), so "evidence silently lost" becomes "the run row says which path
// to fix" (task-17). Warnings are deduped per path inside one declaration.
func resolveEvidenceRefs(casesRoot, workDir, title string, raw json.RawMessage) ([]string, map[string]string, []model.Ref, []string, bool) {
	refs, ok := parseEvidenceRefs(raw)
	if !ok {
		return nil, nil, nil, nil, false
	}
	out := make([]string, 0, len(refs))
	var from map[string]string
	var citations []model.Ref
	var warnings []string
	seen := map[string]bool{}
	warned := map[string]bool{}
	add := func(scheme, locator string) {
		locator = strings.TrimSpace(locator)
		if locator == "" {
			return
		}
		key := scheme + "\x00" + locator
		if seen[key] {
			return
		}
		seen[key] = true
		citations = append(citations, model.Ref{Scheme: scheme, Locator: locator})
	}
	warn := func(reason, path string) {
		path = clipRunes(strings.TrimSpace(path), 120)
		if warned[path] {
			return
		}
		warned[path] = true
		warnings = append(warnings, fmt.Sprintf(
			"事实「%s」的证据「%s」无法登记：%s", clipRunes(title, 80), path, reason))
	}

	for _, ref := range refs {
		if ref.File == "" && ref.Artifact != "" {
			if file, err := artifact.Materialize(casesRoot, workDir, ref.Artifact); err == nil {
				ref.File = file
			} else if !os.IsNotExist(err) {
				warn("原生结果无法取回："+err.Error(), ref.Artifact)
			}
		}
		if ref.File == "" {
			// A source without a file: checkable by a human, not openable here.
			if ref.URL == "" && ref.Artifact == "" {
				return nil, nil, nil, nil, false
			}
			if ref.URL != "" {
				add(model.RefURL, ref.URL)
			}
			if ref.Artifact != "" {
				add(model.RefArtifact, ref.Artifact)
			}
			continue
		}
		rel, state := resolveEvidence(casesRoot, workDir, ref.File)
		switch state {
		case evidenceInvalid:
			// Absolute, or pointing outside the project cases tree: that is
			// not a citation, it is a bug or an attack. The line is dropped,
			// but the warning names the path so the next round can correct it
			// instead of the loss being silent (task-17).
			warn(evidenceOutOfBoundsWarning, ref.File)
			return nil, nil, nil, warnings, false
		case evidenceMissing:
			warn("文件不存在（已作为引用保留）", ref.File)
			add(model.RefFile, rel)
			// The origin of a vanished file is still a lead.
			if source, loc := ref.source(); source != "" {
				add(source, loc)
			}
		default:
			out = append(out, rel)
			if source, loc := ref.source(); source != "" {
				if from == nil {
					from = make(map[string]string, len(refs))
				}
				from[rel] = source + ":" + loc
			}
		}
	}
	return out, from, citations, warnings, true
}

// source splits the declared origin into its scheme and locator. The url wins
// when both are present: it is the thing a human can open.
func (r EvidenceRef) source() (string, string) {
	if r.URL != "" {
		return model.RefURL, r.URL
	}
	if r.Artifact != "" {
		return model.RefArtifact, r.Artifact
	}
	return "", ""
}

// evidenceState says what the filesystem thinks of a declared path.
type evidenceState int

const (
	evidenceOK evidenceState = iota
	evidenceMissing
	evidenceInvalid
)

// resolveEvidence returns name as a clean slash path relative to workDir, plus
// what happened to it: clean and present (evidenceOK), clean but gone
// (evidenceMissing), or pointing outside the project cases tree
// (evidenceInvalid).
//
// The boundary is the project cases tree, not the run's case directory.
// graph.json spells every evidence URI relative to the case directory with a
// parent hop ("../in_x/file", evidence_base=case_dir), so a model that copies a
// graph evidence path into facts.jsonl is making a legitimate citation, not an
// escape. A path that lands inside a *case directory* (at least one separator
// below casesRoot) is accepted and returned relative to workDir, keeping the
// "../in_x/..." spelling for a sibling case; a path that lands directly in
// casesRoot, or outside it, is evidenceInvalid.
func resolveEvidence(casesRoot, workDir, name string) (string, evidenceState) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", evidenceInvalid
	}
	native := filepath.FromSlash(strings.ReplaceAll(name, "\\", "/"))
	if filepath.IsAbs(native) {
		return "", evidenceInvalid
	}
	abs := filepath.Join(workDir, native)
	// Operator login material is a private execution input, never report
	// evidence. Native scanner data enters only through an explicit artifact
	// citation, which materializes the chosen bytes inside the current case.
	if rel, err := filepath.Rel(casesRoot, abs); err == nil {
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) > 0 && (parts[0] == ".access" || parts[0] == ".native") {
			return "", evidenceInvalid
		}
		if len(parts) > 3 && parts[1] == ".tw" && parts[2] == "access" {
			return "", evidenceInvalid
		}
	}
	if !insideCasesTree(casesRoot, abs) {
		return "", evidenceInvalid
	}
	rel, err := filepath.Rel(workDir, abs)
	if err != nil || filepath.IsAbs(rel) {
		return "", evidenceInvalid
	}
	clean := filepath.ToSlash(rel)
	st, err := os.Stat(abs)
	if err != nil || st.IsDir() {
		// Keep the cleaned path: a dangling ref must not echo `..\..\x` back
		// into the ledger, and the report prints this string.
		return clean, evidenceMissing
	}
	return clean, evidenceOK
}

// insideCasesTree reports whether abs resolves inside a case directory under
// casesRoot. The path must be at least one level below casesRoot: a file sitting
// directly in the cases root belongs to no case, and accepting it would make the
// ledger root itself citable.
func insideCasesTree(casesRoot, abs string) bool {
	if casesRoot == "" {
		return false
	}
	rel, err := filepath.Rel(casesRoot, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return false
	}
	return strings.ContainsRune(rel, filepath.Separator)
}
