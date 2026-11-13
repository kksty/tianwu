// Package verifier re-runs the minimal proof behind a fact that would go into
// the report (docs/06, D7).
//
// A verifier run is deliberately boring: one fact in, one verdict out, written
// to review.json by the executor rather than parsed from stdout — the same
// file-not-stream contract the ledger already uses (D18).
package verifier

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kksty/tianwu/internal/evidence"
	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// VerdictPath is where a verifier run writes its conclusion, relative to its
// own work dir.
const VerdictPath = "review.json"

// Verifier runs one proof per fact.
type Verifier struct {
	ScopeFor func() string
	Policy   func() string
	Admit    func(context.Context) (func(), error)
	Store    *store.Store
	Exec     executor.Executor
	CaseRoot string
	Scope    string
	Timeout  time.Duration
	Skills   []string
	// Node is where reviews run; empty means the executor's default node.
	Node string
	// NodeFor overrides Node per fact. A review must run where the finding was
	// observed: a target reachable only from the node that found it cannot be
	// re-proven from the control plane (docs/14-nodes.md).
	NodeFor func(model.Fact) string
	// Track hands the live review to the control plane. It is called after the
	// executor settles and reports the structured cause ("" when nobody
	// canceled it); a canceled review writes no verdict (D73). The control plane
	// tracks reviews separately from report runs so the project pause does not
	// cancel an operator-triggered re-proof (D137).
	Track func(runID string, cancel context.CancelFunc) func() string
	// Log opens the review run's event journal and returns the appender and the
	// closer. It is optional: without it the review still runs, it just keeps no
	// `runs/<id>.jsonl` (the run row then has no event stream to show). The
	// control plane supplies it so a review is auditable like any other run
	// (D138).
	Log func(runID string) (func(executor.Event), func())

	mu      sync.Mutex
	running map[string]bool
}

func New(st *store.Store, ex executor.Executor, caseRoot string) *Verifier {
	return &Verifier{Store: st, Exec: ex, CaseRoot: caseRoot, running: map[string]bool{}}
}

// Eligible decides what deserves a second look: only facts that would appear in
// the report, and only while they are still unverified. Observation review is
// separately enabled by the project's verified-only policy. The default lives in the store
// because the automatic review queue enumerates the same set; keeping two copies
// is how the queue and the admission slowly disagree (F11).
func Eligible(f model.Fact) bool { return store.ReviewEligible(f) }

func (v *Verifier) CanReview(f model.Fact) bool {
	return Eligible(f) || (v.Policy != nil && v.Policy() == model.PremiseVerifiedOnly && f.Status == "unverified" && model.ReviewableKind(f.Kind))
}

func (v *Verifier) Busy(factID string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.running[factID]
}

func (v *Verifier) claim(factID string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.running[factID] {
		return false
	}
	v.running[factID] = true
	return true
}

func (v *Verifier) release(factID string) {
	v.mu.Lock()
	delete(v.running, factID)
	v.mu.Unlock()
}

// Run re-executes one proof and records the verdict.
func (v *Verifier) Run(ctx context.Context, factID string) (model.Review, error) {
	f, ok := v.Store.FactByID(factID)
	if !ok {
		return model.Review{}, fmt.Errorf("fact %s not found", factID)
	}
	if !v.CanReview(f) {
		return model.Review{}, fmt.Errorf("只复核会进报告的结论（vuln / cred），且每条只复核一次")
	}
	if !v.claim(factID) {
		return model.Review{}, fmt.Errorf("fact %s 正在复核中", factID)
	}
	defer v.release(factID)
	return v.runClaimed(ctx, f, factID)
}

// Start claims the fact synchronously and runs the review in the background.
// The claim is what makes the API's "queued" honest: Busy() followed by a
// goroutine was check-then-act, so two requests could both answer 202 and one
// silently lose the race inside Run.
func (v *Verifier) Start(ctx context.Context, factID string, onDone func()) error {
	f, ok := v.Store.FactByID(factID)
	if !ok {
		return fmt.Errorf("fact %s not found", factID)
	}
	if !v.CanReview(f) {
		return fmt.Errorf("只复核会进报告的结论（vuln / cred），且每条只复核一次")
	}
	if !v.claim(factID) {
		return fmt.Errorf("fact %s 正在复核中", factID)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		defer v.release(factID)
		if _, err := v.runClaimed(ctx, f, factID); err == nil && onDone != nil {
			onDone()
		}
	}()
	return nil
}

func (v *Verifier) runClaimed(ctx context.Context, f model.Fact, factID string) (model.Review, error) {
	nodeID := strings.TrimSpace(v.Node)
	if v.NodeFor != nil {
		if n := strings.TrimSpace(v.NodeFor(f)); n != "" {
			nodeID = n
		}
	}
	if nodeID != "" && !executor.HasNode(v.Exec, nodeID) {
		return model.Review{}, fmt.Errorf("复核节点 %s 已不在 nodes.json 里：结论只在那个网络位置可复现", nodeID)
	}
	if nodeID == "" {
		nodeID = executor.ResolveNode(v.Exec, "")
	}
	run, err := v.Store.AddRun(model.Run{IntentID: f.IntentID, Node: nodeID, Purpose: "verifier"})
	if err != nil {
		return model.Review{}, err
	}
	// Only this run can produce this receipt. Historical reviews remain intact;
	// a failed run can never consume another run's review.json.
	workDir := filepath.Join(v.CaseRoot, f.IntentID, ".verify", f.ID, run.ID)
	// The run row comes first so the store can validate it and record the frozen
	// instruction versions on it; the single freeze then supplies the evidence
	// rows the reviewer will cite, so the prompt and its proof addresses come
	// from one ledger moment.
	policy := model.PremiseStandard
	scope := ""
	if v.ScopeFor != nil {
		scope = v.ScopeFor()
	} else {
		scope = v.Scope
	}
	if v.Policy != nil {
		policy = v.Policy()
	}
	db, instructions, err := v.Store.PreparePurposeInput(run.ID, policy)
	if err != nil {
		now := time.Now()
		_, _ = v.Store.UpdateRun(run.ID, func(r *model.Run) {
			r.Status, r.ExitReason, r.EndedAt = model.RunFailed, err.Error(), &now
		})
		return model.Review{}, err
	}
	evidencePaths, err := v.verifiedEvidencePaths(db, f)
	if err != nil {
		// A cited proof whose preserved bytes no longer hash to what the ledger
		// admitted cannot be re-proven from those bytes: handing the path to the
		// model would ask it to launder tampering into a verdict. Refuse before
		// the prompt, settle this run failed with the reason, and record
		// needs_evidence so the fact cannot look proven through a changed file.
		now := time.Now()
		_, _ = v.Store.UpdateRun(run.ID, func(r *model.Run) {
			r.Status, r.ExitReason, r.EndedAt = model.RunFailed, err.Error(), &now
		})
		review := model.Review{
			Verdict: "needs_evidence",
			Notes:   "宣称的证据无法通过完整性校验，本轮不做复核：" + err.Error(),
			FactID:  factID,
		}
		review.ClaimVersion, review.ClaimHash = f.ClaimVersion, f.ClaimHash
		review.VerifierRun = run.ID
		return v.Store.ReviewFact(review)
	}
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		now := time.Now()
		_, _ = v.Store.UpdateRun(run.ID, func(r *model.Run) {
			r.Status, r.ExitReason, r.EndedAt = model.RunFailed, err.Error(), &now
		})
		return model.Review{}, err
	}

	// The review is a run too: journal its events so the console shows what it
	// executed and how much it cost instead of "no log for run" (D138).
	emit, closeLog := func(executor.Event) {}, func() {}
	if v.Log != nil {
		emit, closeLog = v.Log(run.ID)
	}
	defer closeLog()
	// The harness reports the model it resolved in session_started. A review has
	// no profile binding of its own (Request.Profile is empty, so the host's
	// default answers) and the verifier cannot know which saved entry that was:
	// record the observed model only, never an invented Profile/Provider.
	modelName := ""
	observe := func(ev executor.Event) {
		if modelName == "" {
			modelName = executor.SessionModel(ev)
		}
		emit(ev)
	}

	// The project may stop this review while it runs (pause/delete). A canceled
	// review is the control plane's decision, so it must settle as canceled and
	// must not write a needs_evidence verdict: that would turn "we stopped it"
	// into "the finding could not be reproduced".
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	settle := func() string { return "" }
	if v.Track != nil {
		settle = v.Track(run.ID, cancel)
	}
	if v.Timeout > 0 {
		var tcancel context.CancelFunc
		ctx, tcancel = context.WithTimeout(ctx, v.Timeout)
		defer tcancel()
	}

	started := time.Now()
	var res executor.Result
	var runErr error
	if v.Admit != nil {
		var release func()
		release, runErr = v.Admit(ctx)
		if runErr == nil {
			defer release()
		}
	}
	if runErr == nil {
		res, runErr = v.Exec.Run(ctx, executor.Request{
			IntentID: f.IntentID,
			Prompt:   executor.InstructionSection(instructions) + BuildPrompt(f, evidencePaths, scope),
			WorkDir:  workDir,
			Node:     nodeID,
			Purpose:  "verifier",
			Skills:   v.Skills,
		}, observe)
	}

	now := time.Now()
	if cause := settle(); cause != "" {
		_, _ = v.Store.UpdateRun(run.ID, func(r *model.Run) {
			r.Status = model.RunCanceled
			r.CancelCause = cause
			r.StartedAt = started
			r.EndedAt = &now
			r.Events = res.Events
			r.ExitReason = "复核被取消：控制面停止（原因见 cancel_cause）"
			r.Tokens = res.Tokens
			r.StopReason = res.StopReason
			r.Model = modelName
			r.Usage = executor.UsageOf(res)
		})
		return model.Review{}, fmt.Errorf("复核已取消（%s）", cause)
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
	_, _ = v.Store.UpdateRun(run.ID, func(r *model.Run) {
		r.Status = status
		r.StartedAt = started
		r.EndedAt = &now
		r.Events = res.Events
		r.ExitReason = reason
		// A review is a run too: its tokens belong in the cost picture even
		// though the decider never dispatches it (D52's accounting). Unknown
		// usage stays unknown (Known=false), not a fabricated zero.
		r.Tokens = res.Tokens
		r.StopReason = res.StopReason
		r.Model = modelName
		r.Usage = executor.UsageOf(res)
	})

	// A verifier that produced no verdict has not verified anything. Record
	// that as needs_evidence instead of leaving the fact looking proven.
	var review model.Review
	if status == model.RunDone {
		review, err = readVerdict(filepath.Join(workDir, VerdictPath))
	} else {
		err = fmt.Errorf("本轮复核未成功结束，不能采用结论")
	}
	if err != nil {
		review = model.Review{
			Verdict: "needs_evidence",
			Notes:   "复核运行没有给出可用结论：" + err.Error(),
		}
		if reason != "" {
			review.Notes += "；运行输出：" + clip(reason, 300)
		}
	}
	review.FactID = factID
	review.ClaimVersion, review.ClaimHash = f.ClaimVersion, f.ClaimHash
	review.VerifierRun = run.ID
	return v.Store.ReviewFact(review)
}

// verifiedEvidencePaths resolves the cited evidence rows from the frozen ledger
// input and refuses bytes that no longer match the admitted digest.
//
// A missing file is not a tamper finding: the reviewer is asked to re-prove the
// claim and will find the material absent, which is a legitimate needs_evidence
// conclusion drawn from the actual materials. A digest conflict is different —
// the proof object exists but its bytes are not the admitted proof, so there is
// nothing honest to hand over (F2).
func (v *Verifier) verifiedEvidencePaths(db store.DB, f model.Fact) ([]string, error) {
	byID := make(map[string]model.Evidence, len(db.Evidence))
	for _, e := range db.Evidence {
		byID[e.ID] = e
	}
	out := make([]string, 0, len(f.EvidenceIDs))
	for _, id := range f.EvidenceIDs {
		e, ok := byID[id]
		if !ok {
			continue
		}
		path, err := evidence.Path(v.CaseRoot, e)
		if err != nil {
			// Never hand the model a path outside the case root. The claim still
			// cites it; the brief shows no readable file for it.
			continue
		}
		if err := evidence.Verify(v.CaseRoot, e); err != nil &&
			(errors.Is(err, evidence.ErrDigestMismatch) || errors.Is(err, evidence.ErrCorrupt)) {
			return nil, fmt.Errorf("证据 %s 的保全字节与登记摘要不一致（URI %s）：%w", e.ID, e.URI, err)
		}
		out = append(out, path)
	}
	return out, nil
}

type verdictFile struct {
	Verdict string `json:"verdict"`
	Notes   string `json:"notes"`
	Proof   string `json:"proof"`
}

func readVerdict(path string) (model.Review, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return model.Review{}, fmt.Errorf("没写 %s", VerdictPath)
	}
	var vf verdictFile
	if err := json.Unmarshal(b, &vf); err != nil {
		return model.Review{}, fmt.Errorf("%s 不是合法 JSON", VerdictPath)
	}
	vf.Verdict = strings.ToLower(strings.TrimSpace(vf.Verdict))
	switch vf.Verdict {
	case "pass", "needs_evidence", "reject":
	default:
		return model.Review{}, fmt.Errorf("verdict 不合法：%q", vf.Verdict)
	}
	notes := strings.TrimSpace(vf.Notes)
	if proof := strings.TrimSpace(vf.Proof); proof != "" {
		if notes != "" {
			notes += "\n"
		}
		notes += "证明：" + proof
	}
	if notes == "" && vf.Verdict != "pass" {
		return model.Review{}, fmt.Errorf("非 pass 的结论必须写明 notes")
	}
	return model.Review{Verdict: vf.Verdict, Notes: notes}, nil
}

// BuildPrompt is the whole brief of a verifier run.
//
// Two kinds of proof exist (D35). A dynamic finding is reproduced by re-running
// the attack; a static finding has no live surface, so it is reproduced by
// replaying the command over the same artifact. Asking a static finding for a
// live exploit would reject it for the wrong reason.
func BuildPrompt(f model.Fact, evidence []string, scope string) string {
	static := f.Proof == "static"

	var b strings.Builder
	if static {
		b.WriteString("你是复核者。这条结论来自**静态分析**：没有可重跑的攻击面。\n")
		b.WriteString("只做一件事：用结论里写明的命令，对同一份材料重放一次，确认你看到的东西和结论一致。\n")
		b.WriteString("不要重新攻击任何目标、不要扩大范围、不要改动任何已有文件（除了写 review.json）。\n\n")
	} else {
		b.WriteString("你是复核者。只做一件事：为下面这条结论**重新跑一次最小只读证明**。\n")
		b.WriteString("不要扩大范围、不要扫描新目标、不要改动任何已有文件（除了写 review.json）。\n\n")
	}

	b.WriteString("## 待复核结论\n")
	fmt.Fprintf(&b, "类型：%s\n标题：%s\n", f.Kind, f.Title)
	if static {
		b.WriteString("证明方式：静态（重放命令，不重跑攻击）\n")
	}
	if f.Summary != "" {
		b.WriteString("摘要：" + f.Summary + "\n")
	}
	if f.Doc != "" {
		b.WriteString("详情：\n" + f.Doc + "\n")
	}
	if len(evidence) == 0 {
		b.WriteString("宣称的证据：（无）\n")
	} else {
		b.WriteString("宣称的证据：\n")
		for _, p := range evidence {
			b.WriteString("- " + p + "\n")
		}
	}
	if scope != "" {
		b.WriteString("\n授权范围：" + scope + "\n")
	}

	b.WriteString("\n## 判据\n")
	b.WriteString("核验对象是上述实体、现场与断言版本上的具体结论；文件存在、摘要一致或原观察者的说法，只证明材料来源，不能单独证明结论的含义。无法独立核对时使用 needs_evidence。\n")
	if static {
		b.WriteString("- pass：你自己重放的输出与结论一致（同样的字节/指令/结构），证据足以进报告\n")
		b.WriteString("- needs_evidence：材料缺失或命令不明确，无法重放，或输出不足以支撑结论\n")
		b.WriteString("- reject：重放结果明确否定了结论\n")
		b.WriteString("你的证明就是重放的命令和它打印了什么；不要用结论自己的说法当你的结果。\n")
	} else {
		b.WriteString("- pass：你自己重跑的结果与结论一致，证据足以进报告\n")
		b.WriteString("- needs_evidence：无法复现，或证据不足以支撑结论\n")
		b.WriteString("- reject：重跑结果明确否定了结论\n")
		b.WriteString("不要复用原证据里的结论当自己的结果——必须自己再跑一次。\n")
	}

	b.WriteString("\n## 输出\n")
	fmt.Fprintf(&b, "把结论写进当前目录的 %s：\n", VerdictPath)
	b.WriteString(`{"verdict":"pass|needs_evidence|reject","notes":"一句判断理由","proof":"你执行了什么、看到的关键输出"}` + "\n")
	return b.String()
}

func clip(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}
