// Package report builds the customer-facing artifact.
//
// The report is a directory, not a rendered string (docs/08, D26). A report run
// writes index.md and findings.json; Go validates the invariants and refuses to
// call the result ready when any of them fails. Nothing here formats prose —
// that is the whole point of the split:排版交给模型，规则留给代码.
package report

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kksty/tianwu/internal/board"
	"github.com/kksty/tianwu/internal/evidence"
	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// Layout inside <caseRoot>/reports/<id>/.
const (
	Dir         = "reports"
	MetaFile    = "report.json"
	Snapshot    = "snapshot.json"
	IndexFile   = "index.md"
	Findings    = "findings.json"
	ChecksFile  = "checks.json"
	FindingsDir = "findings"
	EvidenceDir = "evidence"
)

// Report states. incomplete is not an error: it means "not deliverable yet".
const (
	StatusDraft      = "draft"
	StatusReady      = "ready"
	StatusIncomplete = "incomplete"
	StatusStale      = "stale"
)

// Verdicts for one finding. Two states, not cyber-audit's four (D41):
//
//	candidate  the ledger has not proven it (status=unverified)
//	confirmed  a verifier rerun proved it
//
// Their "dismissed" and "inconclusive" existed because cyber-audit has no ledger
// to hold a negative result, so the report had to. Here "这个不成立" is
// fact.status=rejected — such a fact never gets a row at all — and "定不了" is
// the fact's doc plus the report's limitations section (D30).
const (
	VerdictCandidate = "candidate"
	VerdictConfirmed = "confirmed"
)

// DefaultStaleAfter is how long evidence stays reportable. Upstream cyber-audit
// defined stale_after and never compared it with now (D27); this one does.
const DefaultStaleAfter = 30 * 24 * time.Hour

// Finding is one line of findings.json: the machine-readable half of a report.
type Finding struct {
	Fact          string   `json:"fact"`
	Verdict       string   `json:"verdict"`
	Title         string   `json:"title"`
	Location      string   `json:"location"`
	Preconditions string   `json:"preconditions"`
	Impact        string   `json:"impact"`
	Trace         string   `json:"trace"`
	Evidence      []string `json:"evidence"`
}

// Check is one invariant, verified or not.
type Check struct {
	Rule   string `json:"rule"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// Meta is report.json: the index a reader (or the UI) can trust without
// parsing prose.
type Meta struct {
	PremisePolicy string     `json:"premise_policy,omitempty"`
	ID            string     `json:"id"`
	Status        string     `json:"status"`
	CreatedAt     time.Time  `json:"created_at"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
	SourceDigest  string     `json:"source_digest"`
	Findings      int        `json:"findings"`
	RunID         string     `json:"run_id,omitempty"`
	Checks        []Check    `json:"checks,omitempty"`
}

// Generator owns one report run at a time.
type Generator struct {
	ScopeFor   func() string
	Policy     func() string
	Admit      func(context.Context) (func(), error)
	Store      *store.Store
	Exec       executor.Executor
	CaseRoot   string
	Scope      string
	Timeout    time.Duration
	StaleAfter time.Duration
	Skills     []string
	// Node is where the report is written; empty means the executor's default.
	Node string
	// NodeFor overrides Node at generation time. The report is a documentation
	// pass, so it follows the project's default node, which may change between
	// generations.
	NodeFor func() string
	// Track hands the live run to the control plane, which cancels it when the
	// project is paused or deleted. The returned func is called after the
	// executor settles and reports the structured cancel cause ("" when nobody
	// canceled it). Without this the run kept writing into a project that was
	// stopping, and its row said failed when the control plane stopped it (D73).
	Track func(runID string, cancel context.CancelFunc) func() string
	// Log opens the run's event journal and returns its appender and closer. It
	// is optional: a generator without it still writes the report, it just keeps
	// no `runs/<id>.jsonl`. The control plane supplies it so the writing pass is
	// auditable from the console like any other run (D138).
	Log func(runID string) (func(executor.Event), func())

	mu      sync.Mutex
	running bool
}

func (g *Generator) Busy() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.running
}

func (g *Generator) staleAfter() time.Duration {
	if g.StaleAfter > 0 {
		return g.StaleAfter
	}
	return DefaultStaleAfter
}

// Dir returns the absolute directory of one report.
func (g *Generator) Dir(id string) string {
	return filepath.Join(g.CaseRoot, Dir, id)
}

// Generate runs one report. It refuses to start when a face is unaccounted:
// a report that cannot say "what was not tested" is not worth a model run.
func (g *Generator) Generate(ctx context.Context) (Meta, error) {
	g.mu.Lock()
	if g.running {
		g.mu.Unlock()
		return Meta{}, fmt.Errorf("已有报告在生成中")
	}
	g.running = true
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		g.running = false
		g.mu.Unlock()
	}()

	// Resolve the writing node first: a report whose node has vanished must fail
	// before this run leaves a run row or a directory behind.
	nodeID := strings.TrimSpace(g.Node)
	if g.NodeFor != nil {
		if n := strings.TrimSpace(g.NodeFor()); n != "" {
			nodeID = n
		}
	}
	if nodeID != "" && !executor.HasNode(g.Exec, nodeID) {
		return Meta{}, fmt.Errorf("报告节点 %s 已不在 nodes.json 里", nodeID)
	}
	if nodeID == "" {
		nodeID = executor.ResolveNode(g.Exec, "")
	}

	// The run row comes before the freeze because PreparePurposeInput validates
	// the run under the store lock and records the frozen instruction versions on
	// it. It is not a report yet: report.json — the file List() advertises — is
	// written only after the freeze succeeds.
	run, err := g.Store.AddRun(model.Run{Node: nodeID, Purpose: "report"})
	if err != nil {
		return Meta{}, err
	}
	fail := func(reason string) {
		now := time.Now()
		_, _ = g.Store.UpdateRun(run.ID, func(r *model.Run) {
			r.Status, r.ExitReason, r.EndedAt = model.RunFailed, reason, &now
		})
	}

	// ONE freeze for the whole report. Facts, evidence rows/bytes, goals, the
	// effective persistent instructions, the source digest and the validation
	// ledger all come from this single locked clone. Before this, Generate took
	// Snapshot() here and froze the instructions later, so a fact or instruction
	// written in between produced a report whose snapshot, source digest and
	// checks disagreed about the input the writer actually read (F3/F2).
	policy := model.PremiseStandard
	scope := ""
	if g.ScopeFor != nil {
		scope = g.ScopeFor()
	} else {
		scope = g.Scope
	}
	if g.Policy != nil {
		policy = g.Policy()
	}
	db, instructions, err := g.Store.PreparePurposeInput(run.ID, policy)
	if err != nil {
		fail(err.Error())
		return Meta{}, err
	}

	id := store.NewID("rp")
	dir := g.Dir(id)
	for _, sub := range []string{"", FindingsDir, EvidenceDir} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			fail(err.Error())
			return Meta{}, err
		}
	}
	// A failed generation must not leave a directory behind: once report.json
	// exists, List() advertises a draft that never finishes and there is no API
	// to delete it. Only a completed (ready or incomplete) report is kept.
	finished := false
	defer func() {
		if !finished {
			_ = os.RemoveAll(dir)
		}
	}()

	facts := reportable(db.Facts)
	copied, err := g.copyEvidence(dir, db, facts)
	if err != nil {
		// Nothing was produced: do not leave an empty report directory behind
		// for List() to advertise (D45). The run row is settled failed so the
		// generator does not keep looking like it is still writing.
		fail(err.Error())
		return Meta{}, err
	}
	if err := writeSnapshot(dir, db, copied, scope, instructions); err != nil {
		fail(err.Error())
		return Meta{}, err
	}

	meta := Meta{
		PremisePolicy: policy,
		ID:            id,
		Status:        StatusDraft,
		CreatedAt:     time.Now(),
		SourceDigest:  board.ContentDigest(board.BuildFromDB(db, 20)),
		Findings:      len(facts),
		RunID:         run.ID,
	}
	if err := writeMeta(dir, meta); err != nil {
		fail(err.Error())
		return Meta{}, err
	}

	// Journal the writing pass: the run row already counted its events, but with
	// no `runs/<id>.jsonl` the console showed "no log for run" and the execution
	// review was empty on the most expensive step of delivery (D138).
	emit, closeLog := func(executor.Event) {}, func() {}
	if g.Log != nil {
		emit, closeLog = g.Log(run.ID)
	}
	defer closeLog()
	// The harness reports the model it resolved in session_started. A purpose run
	// requests no saved profile (Request.Profile is empty, so the host's default
	// answers), and the generator cannot know which saved entry that was. Record
	// only what is observed; an invented Profile/ResolvedProfile/Provider would
	// be a binding claim the generator cannot make.
	modelName := ""
	observe := func(ev executor.Event) {
		if modelName == "" {
			modelName = executor.SessionModel(ev)
		}
		emit(ev)
	}

	// The project may stop this run while it is writing (pause/delete). The
	// registry gives the control plane the cancel func; settle reports the
	// structured cause back so the row settles as canceled, not failed.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	settle := func() string { return "" }
	if g.Track != nil {
		settle = g.Track(run.ID, cancel)
	}
	if g.Timeout > 0 {
		var tcancel context.CancelFunc
		ctx, tcancel = context.WithTimeout(ctx, g.Timeout)
		defer tcancel()
	}

	started := time.Now()
	var res executor.Result
	var runErr error
	if g.Admit != nil {
		var release func()
		release, runErr = g.Admit(ctx)
		if runErr == nil {
			defer release()
		}
	}
	if runErr == nil {
		res, runErr = g.Exec.Run(ctx, executor.Request{
			Prompt:  executor.InstructionSection(instructions) + BuildPrompt(len(facts), scope),
			WorkDir: dir,
			Node:    nodeID,
			Purpose: "report",
			Skills:  g.Skills,
		}, observe)
	}

	now := time.Now()
	if cause := settle(); cause != "" {
		// A canceled generation produced nothing: the deferred cleanup removes
		// the half-written report directory and no validation runs on a
		// deliverable that was never written.
		_, _ = g.Store.UpdateRun(run.ID, func(r *model.Run) {
			r.Status = model.RunCanceled
			r.CancelCause = cause
			r.StartedAt = started
			r.EndedAt = &now
			r.ExitReason = "项目停止：报告生成被取消"
			r.Events = res.Events
			r.Tokens = res.Tokens
			r.StopReason = res.StopReason
			r.Model = modelName
			r.Usage = executor.UsageOf(res)
		})
		return Meta{}, fmt.Errorf("报告生成已取消（%s）", cause)
	}

	status := model.RunDone
	reason := ""
	if runErr != nil || res.ExitCode != 0 || res.IsError || res.StopReason == "error" || ctx.Err() != nil {
		status = model.RunFailed
		reason = strings.TrimSpace(res.Output)
		if runErr != nil {
			reason = runErr.Error()
		}
	}
	_, _ = g.Store.UpdateRun(run.ID, func(r *model.Run) {
		r.Status = status
		r.StartedAt = started
		r.EndedAt = &now
		r.Events = res.Events
		r.ExitReason = reason
		// The writing pass costs tokens like any other run; leaving them out
		// hides the most expensive step of delivery. Unknown usage stays
		// unknown (Known=false), never a fabricated zero.
		r.Tokens = res.Tokens
		r.StopReason = res.StopReason
		r.Model = modelName
		r.Usage = executor.UsageOf(res)
	})

	if status == model.RunFailed {
		return Meta{}, fmt.Errorf("报告生成未成功结束：%s", reason)
	}
	checks := Validate(dir, db, g.staleAfter())
	if policy == model.PremiseVerifiedOnly {
		checks = append(checks, g.validatePremises(db, facts)...)
	}
	meta.Checks = checks
	meta.FinishedAt = &now
	meta.Status = StatusReady
	for _, c := range checks {
		if !c.OK {
			meta.Status = StatusIncomplete
			break
		}
	}
	if err := writeMeta(dir, meta); err != nil {
		return meta, err
	}
	if err := writeChecks(dir, checks); err != nil {
		return meta, err
	}
	finished = true
	return meta, nil
}

// reportable selects the facts that can appear as findings: conclusions, not
// observations. host/service/note/loot stay in the ledger.
func reportable(facts []model.Fact) []model.Fact {
	out := make([]model.Fact, 0, len(facts))
	for _, f := range facts {
		switch f.Kind {
		case "vuln", "cred":
			if f.Status != "rejected" {
				out = append(out, f)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// copyEvidence puts every referenced artifact inside the report so it can be
// handed over as one folder. Returns fact id -> report-relative evidence paths.
//
// A missing original is a hard failure, not a thinner report. The whole promise
// of the deliverable is "every conclusion opens its proof" (docs/08); quietly
// copying what is left would produce a report that looks complete and is not.
// The operator's way out is to restore the file or to reject the fact (D41).
//
// A digest conflict is its own failure, reported separately: the original is
// present but its bytes are not the admitted proof (the preserved copy was
// altered). Copying them would turn tampering into a deliverable, so the report
// refuses and names the evidence id/file (F2/D150).
func (g *Generator) copyEvidence(dir string, db store.DB, facts []model.Fact) (map[string][]string, error) {
	byID := make(map[string]model.Evidence, len(db.Evidence))
	for _, e := range db.Evidence {
		byID[e.ID] = e
	}
	out := make(map[string][]string, len(facts))
	var missing, conflicts []string
	for _, f := range facts {
		for _, eid := range f.EvidenceIDs {
			e, ok := byID[eid]
			if !ok {
				missing = append(missing, eid+"（事实意图图里没有这一行）")
				continue
			}
			name := eid + "-" + evidenceCopyName(e)
			dst := filepath.Join(dir, EvidenceDir, name)
			if err := evidence.CopyTo(g.CaseRoot, e, dst); err != nil {
				switch {
				case errors.Is(err, evidence.ErrDigestMismatch):
					conflicts = append(conflicts, eid+"（"+e.URI+"）")
				case errors.Is(err, evidence.ErrMissing), errors.Is(err, evidence.ErrOutside):
					missing = append(missing, e.URI+"（事实 "+f.ID+"）")
				default:
					missing = append(missing, e.URI+"（复制失败："+err.Error()+"）")
				}
				continue
			}
			out[f.ID] = append(out[f.ID], EvidenceDir+"/"+name)
		}
	}
	if len(conflicts) > 0 {
		return nil, digestConflictError(conflicts)
	}
	if len(missing) > 0 {
		return nil, missingEvidenceError(missing)
	}
	return out, nil
}

// evidenceCopyName picks the human-facing filename inside the report. URI is the
// immutable object's address now (<sha256>), so it must not become the file name
// shown to a client; the admitted source name is the useful one. All candidates
// are reduced to a base name because the report serves this directory over HTTP.
func evidenceCopyName(e model.Evidence) string {
	for _, candidate := range []string{e.Name, e.SourceURI, e.URI} {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		base := filepath.Base(filepath.FromSlash(candidate))
		if base != "" && base != "." && base != string(filepath.Separator) {
			return base
		}
	}
	return "evidence"
}

// snapshot is the input the report run reads. It is written by the control
// plane, never by the model: the model may describe the ledger, not extend it.
type snapshot struct {
	Scope string `json:"scope,omitempty"`
	// Instructions is the effective persistent constraint set this run obeyed
	// (the same rows whose versions the run row froze and whose text the prompt
	// carried). A report is only answerable if a later reader can tell what it
	// was told it may not do; the prose prompt is not a durable record (F3).
	Instructions []instructionView `json:"instructions,omitempty"`
	Facts        []factView        `json:"facts"`
	Goals        []model.Goal      `json:"goals"`
	// Rejected are the conclusions a verifier rejected. They have no findings
	// row, but the writer needs their negative reason for §5.2 (§"止于何处"):
	// without them the snapshot dropped the only material for that section
	// while the prompt still asked for it (D105).
	Rejected []rejectedView `json:"rejected,omitempty"`
}

// instructionView is one persistent instruction as the report run received it:
// identity, frozen version, scope and text. The version is what an auditor
// compares against the run row; the text is what the writer actually read.
type instructionView struct {
	ID       string `json:"id"`
	Version  uint64 `json:"version"`
	IntentID string `json:"intent_id,omitempty"`
	Text     string `json:"text"`
}

// reviewView is the latest verifier verdict on one fact: why it is (or is not)
// proven. The writer quotes the judgment, never the proof details.
type reviewView struct {
	Verdict     string    `json:"verdict"` // pass|needs_evidence|reject
	Notes       string    `json:"notes,omitempty"`
	VerifierRun string    `json:"verifier_run,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// rejectedView is one review-rejected conclusion. It only reaches §5.2; it
// never gets a findings row (D41/D105).
type rejectedView struct {
	ID      string      `json:"id"`
	Kind    string      `json:"kind"`
	Title   string      `json:"title"`
	Summary string      `json:"summary"`
	Review  *reviewView `json:"review,omitempty"`
}

type factView struct {
	ID       string   `json:"id"`
	Kind     string   `json:"kind"`
	Title    string   `json:"title"`
	Summary  string   `json:"summary"`
	Doc      string   `json:"doc,omitempty"`
	Surface  string   `json:"surface,omitempty"`
	Status   string   `json:"status"`
	Evidence []string `json:"evidence"`
	// Review is this fact's latest verifier verdict. §4 must say why a
	// candidate has not passed, and the reason is the verifier's, not the
	// writer's (D105).
	Review *reviewView `json:"review,omitempty"`
	// Refs are citations the ledger could not turn into bytes — a url, an
	// artifact id, or a file the run named that is gone. They cannot be
	// evidence, but the writer must put them in "scope and limitations":
	// "we saw it at this URL, we can no longer replay it" is information the
	// client is entitled to (D44).
	Refs []model.Ref `json:"refs,omitempty"`
}

// latestReview is the newest verdict per fact. The ledger stores reviews in
// write order, but a snapshot must not depend on that: take the max by time.
func latestReview(db store.DB) map[string]model.Review {
	out := make(map[string]model.Review, len(db.Reviews))
	for _, r := range db.Reviews {
		if old, ok := out[r.FactID]; !ok || r.CreatedAt.After(old.CreatedAt) {
			out[r.FactID] = r
		}
	}
	return out
}

func viewOf(r model.Review) *reviewView {
	return &reviewView{Verdict: r.Verdict, Notes: r.Notes, VerifierRun: r.VerifierRun, CreatedAt: r.CreatedAt}
}

// rejectedFacts selects the conclusions a review rejected. They are not
// reportable (no row, no page), but their negative reason is required material
// for §5.2.
func rejectedFacts(facts []model.Fact) []model.Fact {
	out := make([]model.Fact, 0, len(facts))
	for _, f := range facts {
		switch f.Kind {
		case "vuln", "cred":
			if f.Status == "rejected" {
				out = append(out, f)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func writeSnapshot(dir string, db store.DB, evidence map[string][]string, scope string, instructions []model.Instruction) error {
	reviews := latestReview(db)
	s := snapshot{Scope: scope, Goals: db.Goals}
	for _, in := range instructions {
		s.Instructions = append(s.Instructions, instructionView{
			ID: in.ID, Version: in.Version, IntentID: in.IntentID, Text: in.Text,
		})
	}
	for _, f := range reportable(db.Facts) {
		v := factView{
			ID: f.ID, Kind: f.Kind, Title: f.Title, Summary: f.Summary,
			Doc: f.Doc, Status: f.Status, Evidence: evidence[f.ID], Refs: f.Refs,
		}
		if r, ok := reviews[f.ID]; ok {
			v.Review = viewOf(r)
		}
		s.Facts = append(s.Facts, v)
	}
	for _, f := range rejectedFacts(db.Facts) {
		v := rejectedView{ID: f.ID, Kind: f.Kind, Title: f.Title, Summary: f.Summary}
		if r, ok := reviews[f.ID]; ok {
			v.Review = viewOf(r)
		}
		s.Rejected = append(s.Rejected, v)
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, Snapshot), b, 0o644)
}

func writeMeta(dir string, m Meta) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, MetaFile), b, 0o644)
}

func writeChecks(dir string, checks []Check) error {
	b, err := json.MarshalIndent(checks, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, ChecksFile), b, 0o644)
}

// SafePath resolves a report-relative path and refuses anything outside dir.
// The report directory is served over HTTP, so containment is not optional.
func SafePath(dir, rel string) (string, bool) {
	return safeJoin(dir, rel)
}

// Preflight answers whether a report could be generated at all, without
// spending a model run. The API uses it to fail fast with a useful message.
func (g *Generator) Preflight() error {
	if g.Busy() {
		return fmt.Errorf("已有报告在生成中")
	}
	db := g.Store.Snapshot()
	facts := reportable(db.Facts)
	if len(facts) == 0 {
		return fmt.Errorf("还没有可写进报告的结论（vuln / cred）")
	}
	// Checked here, synchronously, so the operator sees it as a 409 instead of
	// a line in the server log while the request returns 202 (D45).
	missing, conflicts := g.evidenceProblems(db, facts)
	if len(conflicts) > 0 {
		return digestConflictError(conflicts)
	}
	if len(missing) > 0 {
		return missingEvidenceError(missing)
	}
	return nil
}

// evidenceProblems lists the evidence originals a report would cite but cannot
// trust: files that are gone, and files whose bytes no longer match the digest
// the ledger recorded. The two are different operator problems — restore the
// file vs. the preserved proof was altered — so they are never merged.
func (g *Generator) evidenceProblems(db store.DB, facts []model.Fact) (missing, conflicts []string) {
	byID := make(map[string]model.Evidence, len(db.Evidence))
	for _, e := range db.Evidence {
		byID[e.ID] = e
	}
	for _, f := range facts {
		for _, eid := range f.EvidenceIDs {
			e, ok := byID[eid]
			if !ok {
				missing = append(missing, eid+"（事实意图图里没有这一行）")
				continue
			}
			err := evidence.Verify(g.CaseRoot, e)
			switch {
			case err == nil:
			case errors.Is(err, evidence.ErrDigestMismatch):
				conflicts = append(conflicts, eid+"（"+e.URI+"）")
			case errors.Is(err, evidence.ErrMissing), errors.Is(err, evidence.ErrOutside):
				missing = append(missing, e.URI+"（事实 "+f.ID+"）")
			default:
				missing = append(missing, e.URI+"（无法读取："+err.Error()+"）")
			}
		}
	}
	return missing, conflicts
}

// missingEvidenceError is the one sentence both the preflight and the copy path
// say, so the operator gets the same two ways out wherever they hit it.
func missingEvidenceError(missing []string) error {
	return fmt.Errorf("证据原件不在，报告不能出：%s；补回文件，或把对应事实判为不成立（review reject）后再出报告",
		strings.Join(missing, "；"))
}

// digestConflictError refuses a report whose cited bytes no longer hash to the
// ledger's digest. It names the evidence id and address, and it does not offer
// "restore the working file" as the way out: the preserved proof itself is what
// changed, so the operator must restore the original bytes (or reject the fact).
func digestConflictError(conflicts []string) error {
	return fmt.Errorf("%w，报告不能出：%s；保全副本已被改动，需从备份恢复该原件（不要用工作路径的新内容顶替），或把对应事实判为不成立（review reject）后再出报告",
		evidence.ErrDigestMismatch, strings.Join(conflicts, "；"))
}

// ReadMeta loads one report's index from disk. The directory is the record;
// there is no reports table to drift away from it (docs/08, D26).
func ReadMeta(dir string) (Meta, error) {
	b, err := os.ReadFile(filepath.Join(dir, MetaFile))
	if err != nil {
		return Meta{}, err
	}
	var m Meta
	if err := json.Unmarshal(b, &m); err != nil {
		return Meta{}, err
	}
	return m, nil
}

// List returns every report under the case root, newest first.
func List(caseRoot string) []Meta {
	root := filepath.Join(caseRoot, Dir)
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	out := make([]Meta, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if m, err := ReadMeta(filepath.Join(root, e.Name())); err == nil {
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

func head(in []string, n int) []string {
	if len(in) <= n {
		return in
	}
	return in[:n]
}
