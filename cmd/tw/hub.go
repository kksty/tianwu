// The hub is the instance: it owns the open projects, builds one API server and
// one decider per project, and routes /p/<id>/ to the right one.
//
// Routing is a plain handler rather than a registered ServeMux tree so that a
// project created at runtime is reachable immediately, without mutating a live
// mux.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"mime"
	"net/http"
	"os"
	pathpkg "path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kksty/tianwu/internal/aopexec"
	"github.com/kksty/tianwu/internal/api"
	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/decider"
	"github.com/kksty/tianwu/internal/evidence"
	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/fleet"
	"github.com/kksty/tianwu/internal/host"
	"github.com/kksty/tianwu/internal/ioa"
	"github.com/kksty/tianwu/internal/kb"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/node"
	"github.com/kksty/tianwu/internal/pocs"
	"github.com/kksty/tianwu/internal/project"
	"github.com/kksty/tianwu/internal/report"
	"github.com/kksty/tianwu/internal/settings"
	"github.com/kksty/tianwu/internal/verifier"
	"github.com/kksty/tianwu/internal/vulnlib"
	"github.com/kksty/tianwu/internal/web"
)

type hubConfig struct {
	DataDir string
	Nodes   *node.Registry
	// APIBase is this console's own URL. The commander session calls the
	// control-plane API with it (D78).
	APIBase string
	// ConsoleToken is the operator's login token. The local host does not get
	// it: tianwu derives the scoped agent token from it and puts that in the
	// host environment instead, so no harness session (commander or executor)
	// can reach the operator-only routes (D109).
	ConsoleToken string
	// Collab issues the per-run participant tokens. It is process-local: a
	// token belongs to a live run, and a restart ends that run.
	Collab *collab.Registry
	// LocalHost is the `aiscan web` this process started. A settings change
	// restarts it so the local node picks up the new model without a tw restart.
	LocalHost *host.Host
	// ProfileHosts runs one host per saved model profile. The watchdog's key
	// rotation rebuilds a profile's host through it (host.Pool.Restart).
	ProfileHosts *host.Pool
	// IOA tails the local host's IOA space and stores v1 application records
	// for the console. It is read-only and optional (nil when not started).
	IOA          *ioa.Observer
	Timeout      time.Duration
	MaxEvidence  int
	Scope        string
	Skills       []string
	SkillsDir    string
	ReportSkills []string
	VerifyAuto   bool
	MaxRuns      int
	// MaxWorkers caps live intent runs across all projects (0 = uncapped),
	// the instance-wide counterpart of sapper's MAX_WORKERS (docs/17 §3.5).
	MaxWorkers  int
	IdleTimeout time.Duration
	KB          *kb.Library
	KBDir       string
	Debounce    time.Duration
	Heartbeat   time.Duration
	// RoundTimeout bounds one commander round's wall clock (0 = unbounded): the
	// wake loop serves rounds one at a time, so a round that never ends queues
	// every decision behind it (D91).
	RoundTimeout   time.Duration
	NoDecider      bool
	DefaultProject string
}

// kit is one project's runtime: its server, its routes, its decider and the
// switch that stops all of them when the project is deleted.
type kit struct {
	proj   *project.Project
	srv    *api.Server
	mux    *http.ServeMux
	dec    *decider.Runner
	cancel context.CancelFunc
}

type hub struct {
	auxiliaryGate *executor.Gate
	cfg           hubConfig
	mgr           *project.Manager
	ctx           context.Context
	settings      *settings.Store
	pocs          *pocs.Store
	vulns         *vulnlib.Library
	static        http.Handler
	// probe answers "is this node actually reachable" for the 态势 page; it is
	// cached for a few seconds.
	probe *nodeProber

	mu         sync.RWMutex
	kits       map[string]*kit
	dispatchMu sync.Mutex

	// ioaClients are per host: the embedded IOA server is on the same endpoint
	// as AOP, but its space/sender/target ids are resolved lazily and cached.
	ioaMu      sync.Mutex
	ioaClients map[string]*ioa.Client
	ioaSources map[string]*ioa.Observer

	// modelLimits is what the last /api/models probe learned about each model's
	// max_output_tokens. It exists so a saved max_tokens is clamped to what the
	// model actually accepts; an empty map simply means "not detected yet".
	modelLimitsMu sync.Mutex
	modelLimits   map[string]int
}

func newHub(ctx context.Context, cfg hubConfig, mgr *project.Manager,
	st *settings.Store, lib *pocs.Store, vulns *vulnlib.Library) *hub {
	return &hub{
		auxiliaryGate: executor.NewGate(1),
		cfg:           cfg,
		mgr:           mgr,
		ctx:           ctx,
		settings:      st,
		pocs:          lib,
		vulns:         vulns,
		static:        web.Static(),
		probe:         newNodeProber(cfg.Nodes),
		kits:          map[string]*kit{},
		ioaClients:    map[string]*ioa.Client{},
		modelLimits:   map[string]int{},
	}
}

// rememberModelLimits merges one /models catalogue into the cache.
func (h *hub) rememberModelLimits(limits map[string]int) {
	if len(limits) == 0 {
		return
	}
	h.modelLimitsMu.Lock()
	if h.modelLimits == nil {
		h.modelLimits = map[string]int{}
	}
	for id, n := range limits {
		h.modelLimits[id] = n
	}
	h.modelLimitsMu.Unlock()
}

// modelOutputLimit is the detected max_output_tokens for a model, or 0 when it
// has not been detected (Anthropic does not publish one).
func (h *hub) modelOutputLimit(model string) int {
	h.modelLimitsMu.Lock()
	defer h.modelLimitsMu.Unlock()
	return h.modelLimits[strings.TrimSpace(model)]
}

// clampMaxTokens applies the model's upper bound to an explicitly configured
// max_tokens. Zero means "not set" and stays zero: the harness/provider default
// then applies, which is already within the model's limit.
func (h *hub) clampMaxTokens(m settings.Model) settings.Model {
	limit := h.modelOutputLimit(m.Model)
	if limit > 0 && m.MaxTokens > limit {
		log.Printf("settings: max_tokens %d 超过模型 %s 的上限 %d，按上限使用", m.MaxTokens, m.Model, limit)
		m.MaxTokens = limit
	}
	return m
}

// start builds every project that exists at boot.
//
// A restart is an operator action, not permission to spend: every saved
// project comes up paused even when its manifest said running, and stays
// paused until the operator explicitly resumes it. The pause is written to the
// manifest so a second restart cannot silently resume an engagement either; it
// is also applied in memory when that write fails, so a read-only disk cannot
// turn a restart into an auto-start.
func (h *hub) start() {
	for _, p := range h.mgr.All() {
		h.attachBoot(p)
	}
}

// attachBoot is the boot path: it persists the default-paused state first, then
// builds the runtime from it.
func (h *hub) attachBoot(p *project.Project) *kit {
	if !p.IsPaused() {
		if err := p.SetPaused(true); err != nil {
			log.Printf("project %s: 无法持久化启动暂停状态：%v（本进程仍按暂停启动）", p.ID, err)
		} else {
			log.Printf("project %s: 重启后默认暂停，等你点「继续项目」", p.ID)
		}
	}
	return h.attachState(p, true, "重启后项目默认暂停：确认现场后点「继续项目」")
}

// attach builds a project's runtime from the manifest pause state. Runtime
// creation (create project, re-open after edits) keeps that state; only the
// boot path (attachBoot) forces a pause.
func (h *hub) attach(p *project.Project) *kit {
	reason := ""
	if p.IsPaused() {
		reason = "操作员暂停了项目：等你点「继续项目」"
	}
	return h.attachState(p, p.IsPaused(), reason)
}

func (h *hub) attachState(p *project.Project, paused bool, pauseReason string) *kit {
	// Each project gets its own context so a delete can stop its decider and
	// watchers without touching its siblings.
	ctx, cancel := context.WithCancel(h.ctx)

	srv := api.New(p.Store, h.cfg.Nodes.Executor(h.cfg.Timeout), p.Dir, p.CaseDir)
	srv.Ctx = ctx
	srv.Project = p
	srv.SetPaused(paused)
	// The manifest's tier defaults are a project-level pointer into the
	// instance profile catalogue: worker for intents that name no profile,
	// strong for the commander's preferred tier. nil when the project has
	// none, which keeps the old "instance active" behaviour.
	srv.ProjectProfiles = projectProfileDefaults(p)
	if h.cfg.Nodes != nil {
		srv.LocalNode = h.cfg.Nodes.LocalID()
	}
	// Degraded start (D101): the console is allowed to run while the local
	// host's provider never initialized; dispatch to that host is refused until
	// it is ready (a settings save revives it).
	if h.cfg.LocalHost != nil {
		srv.LocalHostStatus = h.cfg.LocalHost.Status
	}
	srv.Collab = h.cfg.Collab
	// Model profiles are instance-level configuration, shown without their
	// credentials. The API freezes the resolved binding on each run; it does not
	// re-read the settings file while a run is live. The catalogue is probed
	// against each endpoint (cached 5 min) so a model the gateway does not serve
	// is visible here and refused at dispatch (task-13).
	srv.Profiles = func() ([]api.ProfileInfo, string, error) {
		if h.settings == nil {
			return nil, "", nil
		}
		cat, err := h.settings.ProfilesProbed()
		if err != nil {
			return nil, "", err
		}
		out := make([]api.ProfileInfo, 0, len(cat.Profiles))
		for _, p := range cat.Profiles {
			out = append(out, api.ProfileInfo{
				ID: p.ID, Name: p.Name, Provider: p.Provider, Model: p.Model,
				ContextWindow: p.ContextWindow, Revision: p.Revision,
				Runnable: p.Runnable, Paused: p.Paused, Active: p.Active,
				Tier:     p.Tier,
				Fallback: p.Fallback,
				Probed:   p.Probed, Available: p.Available, ProbeError: p.ProbeError,
				Local: p.Local,
				// MaxConcurrency is capacity metadata (serialised in the view, but
				// stripped from the host config); Running is filled per request by
				// the API from the live ledger, not here.
				MaxConcurrency: p.MaxConcurrency,
			})
		}
		return out, cat.ActiveProfile, nil
	}
	// Secret redaction backstop (task-13): every profile key (pool included) and
	// the console token are replaced with *** at the journal/pulse write points.
	// The harness reads provider config from the environment (host env is needed
	// for that), so the secret does reach the host process; the run journal and
	// ledger must not keep a copy of it.
	srv.Secrets = func() []string {
		var out []string
		if token := strings.TrimSpace(h.cfg.ConsoleToken); token != "" {
			// The agent token is derived, but once it is in the host env a
			// session can echo it; both stay out of run journals.
			out = append(out, token, agentAPIToken(token))
		}
		if h.settings != nil {
			out = append(out, h.settings.SecretValues()...)
		}
		return out
	}
	// Key rotation is the watchdog's only credential action (docs/17 §3.5,
	// watchdog-liveness §6): a provider auth/quota failure rotates this profile's
	// effective key and rebuilds its host so the next attempt uses the new one.
	// Rotating does not change the binding revision, hence the explicit restart.
	if h.settings != nil {
		srv.RotateProfileKey = func(profile string) (bool, error) {
			_, rotated, err := h.settings.RotateProfileKey(profile)
			if err == nil && rotated {
				// The rotation audit trail docs/17 §3.5 asks for: which profile,
				// how big the pool is, and its new position. Never the key itself.
				if count, index := h.settings.CurrentProfileKey(profile); count > 0 {
					log.Printf("provider_event=key_rotated profile=%s pool=%d index=%d", profile, count, index)
				}
			}
			return rotated, err
		}
	}
	if h.cfg.ProfileHosts != nil {
		srv.RestartProfileHost = h.cfg.ProfileHosts.Restart
	}
	// One-hop profile fallback: the settings store owns the operator-configured
	// target and its validation (runnable, same tier, not self). The API layer
	// only asks "where do I go from here?" and treats an error as a visible
	// stop, never as "stay put" (D95).
	if h.settings != nil {
		srv.ProfileFallback = h.settings.FallbackProfile
		// Credential generations and the rotation/failure-episode state belong to
		// the settings store: the key pool and its cursor are instance-wide, so
		// every project's Server must share one answer to "which generation is
		// current, is a rotation in flight, is the profile cooling down" (F6).
		srv.Credentials = h.settings
	}
	srv.SetContextWindow(h.modelContext())
	srv.Timeout = h.cfg.Timeout
	srv.MaxRuns = h.cfg.MaxRuns
	srv.MaxWorkers = h.cfg.MaxWorkers
	srv.GlobalRunning = h.globalRunning
	srv.InstanceDispatch = &h.dispatchMu
	srv.GlobalProfileRunning = h.globalProfileRunning
	srv.IdleTimeout = h.cfg.IdleTimeout
	srv.KBDir = h.cfg.KBDir
	srv.KB = h.cfg.KB
	srv.Description = p.DescriptionText()
	srv.Scope = h.scopeOf(p)
	srv.APIBase = h.cfg.APIBase
	// say is a host-side delivery: the harness already has the inbox, IOA is
	// just its HTTP face. An audit-only or IOA-less host leaves this nil and
	// the API returns 503 instead of pretending it can interrupt.
	srv.Say = func(ctx context.Context, in model.Intent, sess aopexec.SessionInfo, text string, interrupt bool) (string, error) {
		return h.say(ctx, p, in, sess, text, interrupt, "", false, nil)
	}
	// SayEvent carries the message's stable event id into the v1 envelope, so
	// a retried delivery is deduped by identity instead of by retry count.
	srv.SayEvent = func(ctx context.Context, in model.Intent, sess aopexec.SessionInfo, text string, interrupt bool, eventID string) (string, error) {
		return h.say(ctx, p, in, sess, text, interrupt, eventID, false, nil)
	}
	// A result message with a chain becomes a task.result record instead of a
	// plain message: the chain is what lets the harness reject cycles and what
	// the observer uses for cross-restart dedupe.
	srv.SayResult = func(ctx context.Context, in model.Intent, sess aopexec.SessionInfo, text, eventID string, chain []string) (string, error) {
		return h.say(ctx, p, in, sess, text, false, eventID, true, chain)
	}
	srv.Skills = h.cfg.Skills
	srv.SkillsDir = h.cfg.SkillsDir
	srv.POCs = h.pocs
	if h.cfg.MaxEvidence > 0 {
		srv.Ingest.MaxEvidence = h.cfg.MaxEvidence
	}

	// The verifier and the report generator both re-run a harness in the
	// project's own workspace, so they are per project: a report must never
	// read another engagement's evidence (D7, D26). They go through the same
	// node router as intents — the run records Purpose, not a fake node id.
	if h.cfg.Nodes != nil {
		admitAuxiliary := func(ctx context.Context) (func(), error) {
			release, err := h.auxiliaryGate.Acquire(ctx)
			if err != nil {
				return nil, err
			}
			if err := srv.CheckLocalHost(); err != nil {
				release()
				return nil, err
			}
			return release, nil
		}
		vExec := executor.Executor(h.cfg.Nodes.Executor(h.cfg.Timeout))
		vf := verifier.New(p.Store, vExec, p.CaseDir)
		vf.Policy = p.PremisePolicyView
		vf.ScopeFor = srv.Briefing
		vf.Admit = admitAuxiliary
		vf.Scope = srv.Briefing()
		vf.Timeout = h.cfg.Timeout
		vf.Skills = h.cfg.Skills
		vf.Track = srv.TrackReviewRun
		// The review is a run like any other: it writes the same event journal,
		// so the console can show what it executed and what it cost (D138).
		vf.Log = srv.OpenPurposeRunLog
		// A review must run where the finding was seen; the default node is a
		// fallback for facts whose origin run is gone (docs/14).
		vf.NodeFor = func(f model.Fact) string { return h.factNode(p, f) }
		srv.Verifier = vf
		srv.SetAutoVerify(h.projectVerifyAuto(p))
		srv.SchedulePendingReviews()
		srv.Reports = &report.Generator{
			ScopeFor: srv.Briefing,
			Policy:   p.PremisePolicyView,
			Admit:    admitAuxiliary,
			Store:    p.Store,
			Exec:     h.cfg.Nodes.Executor(h.cfg.Timeout),
			CaseRoot: p.CaseDir,
			Scope:    srv.Briefing(),
			Timeout:  h.cfg.Timeout,
			Skills:   h.cfg.ReportSkills,
			Track:    srv.TrackPurposeRun,
			// The writing pass is journaled too; it is the most expensive step of
			// delivery, so it deserves the same audit trail (D138).
			Log: srv.OpenPurposeRunLog,
			// Writing the report is a control-plane task; it follows the
			// project default rather than a single fact's origin.
			NodeFor: func() string {
				if h.cfg.Nodes == nil {
					return ""
				}
				if id := h.cfg.Nodes.LocalID(); id != "" {
					return id
				}
				return h.cfg.Nodes.Default()
			},
		}
	}

	// Every vuln fact is mirrored into the instance library the moment it is
	// written (D34). The hook also fires on review, so a verdict reaches the
	// library without a second path.
	p.Store.OnFact(func(f model.Fact, uris []string) {
		h.recordVuln(p, f, uris)
	})
	h.backfill(p)

	mux := http.NewServeMux()
	srv.Register(mux)
	mux.HandleFunc("GET /{$}", web.Index())

	k := &kit{proj: p, srv: srv, mux: mux, cancel: cancel}

	// One decider per project. It reads only its own ledger, so two
	// engagements cannot see each other's facts or intents (D33).
	if !h.cfg.NoDecider {
		r := decider.NewRunner(p.Store, srv.State, p.DecisionsPath())
		r.Scope = srv.Briefing()
		r.ModelName = h.modelName()
		r.ContextWindow = srv.ModelContext()
		if name, window := srv.CommanderModelInfo(); name != "" {
			r.ModelName = name
			r.ContextWindow = window
		}
		r.Debounce = h.cfg.Debounce
		r.Heartbeat = h.cfg.Heartbeat
		r.RoundTimeout = h.commanderRoundTimeout(p)
		r.DispatchReady = func() error {
			if h.cfg.Nodes == nil || h.cfg.Nodes.LocalID() == "" {
				return errors.New("本机执行宿主未配置，无法派发")
			}
			// A degraded local host (D101) would fail every run in
			// milliseconds; don't let the decider dispatch into it.
			return srv.CheckCommanderHost()
		}
		// The commander round runs inside the harness, in a session that keeps
		// its own context (D78). The round hands it the new board and how full
		// that session is; at 85% it becomes a handoff round (D80). The session
		// acts through the control-plane API.
		r.Commander = func(ctx context.Context, round string, resume bool) (decider.RoundResult, error) {
			res, acts, err := srv.StartCommander(ctx, round, resume)
			return decider.RoundResult{
				Tokens:        res.Tokens,
				Actions:       acts,
				ContextTokens: res.FinalContextTokens,
				ContextWindow: res.ContextWindow,
				Compacted:     res.Compacted,
			}, err
		}
		// The runner cannot know whether the AOP session still exists; the API
		// answers with the same executor.CanResume StartCommander uses, so the
		// prompt only injects the whole note when the memory is really gone
		// (D89).
		r.Session = srv.CommanderRoundContext
		// A finished engagement pauses itself. Waiting for the operator to notice
		// an idle commander kept the header button on 「暂停项目」 after every goal
		// was met (and let heartbeats keep waking for nothing); this flips the
		// project to paused with a reason that says it was completion, not the
		// operator. New work still needs an explicit 「继续项目」.
		r.Settled = func() {
			srv.AutoPause("目标已完成：项目自动暂停；新增工作后点「继续项目」重新派发")
		}
		srv.Decider = r
		if paused {
			if pauseReason == "" {
				pauseReason = "项目已暂停：等你点「继续项目」"
			}
			r.PauseFor(pauseReason)
		}
		k.dec = r
	}

	h.mu.Lock()
	h.kits[p.ID] = k
	h.mu.Unlock()
	srv.Recover()
	go srv.Watch(ctx)
	if k.dec != nil {
		k.dec.Start(ctx)
	}

	return k
}

// factNode is where the fact was observed: the node of the run behind its
// evidence, else the intent's last run. Empty means the origin is unknown, and
// the verifier falls back to the default node.
func (h *hub) factNode(p *project.Project, f model.Fact) string {
	for _, id := range f.EvidenceIDs {
		e, ok := p.Store.EvidenceByID(id)
		if !ok || e.RunID == "" {
			continue
		}
		if r, ok := p.Store.GetRun(e.RunID); ok && r.Node != "" {
			return r.Node
		}
	}
	if in, ok := p.Store.GetIntent(f.IntentID); ok && in.RunID != "" {
		if r, ok := p.Store.GetRun(in.RunID); ok {
			return r.Node
		}
	}
	return ""
}

// recordVuln copies the evidence into the library and upserts the entry. It is
// called from the store hook, outside the store lock.
func (h *hub) recordVuln(p *project.Project, f model.Fact, uris []string) {
	if f.Kind != "vuln" {
		return
	}
	kept, dropped := h.vulns.KeepEvidence(p.ID, p.CaseDir, uris)
	in, _ := p.Store.GetIntent(f.IntentID)
	// The library outlives the project, so it carries the proof mode and the
	// latest verdict: after a delete there is no ledger left to ask (D105).
	// Reviews are newest first; an entry with no review is left nil rather than
	// given an empty verdict.
	var review *vulnlib.Review
	if rs := p.Store.Reviews(f.ID); len(rs) > 0 {
		r := rs[0]
		review = &vulnlib.Review{
			Verdict:     r.Verdict,
			Notes:       r.Notes,
			VerifierRun: r.VerifierRun,
			CreatedAt:   r.CreatedAt,
		}
	}
	_, err := h.vulns.Upsert(vulnlib.Entry{
		ProjectID:  p.ID,
		Project:    p.Name,
		SourceID:   f.ID,
		IntentID:   f.IntentID,
		Intent:     in.Title,
		Title:      f.Title,
		Summary:    f.Summary,
		Doc:        f.Doc,
		Confidence: f.Confidence,
		Proof:      f.Proof,
		Status:     f.Status,
		Review:     review,
		Evidence:   kept,
		Dropped:    dropped,
		CreatedAt:  f.CreatedAt,
	})
	if err != nil {
		log.Printf("vulnlib: 记录 %s 失败：%v", f.ID, err)
	}
}

// backfill mirrors vulns that were declared before the library existed (or
// before this process started). Upsert is idempotent, so running it at every
// start is safe.
func (h *hub) backfill(p *project.Project) {
	db := p.Store.Snapshot()
	for _, f := range db.Facts {
		if f.Kind != "vuln" {
			continue
		}
		uris := make([]string, 0, len(f.EvidenceIDs))
		for _, id := range f.EvidenceIDs {
			if e, ok := p.Store.EvidenceByID(id); ok {
				uris = append(uris, e.URI)
			}
		}
		h.recordVuln(p, f, uris)
	}
}

func (h *hub) kit(id string) *kit {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.kits[id]
}

// globalRunning sums live intent runs across all projects for --max-workers.
// It snapshots the kit list under the hub lock and asks each server with its
// own store lock. Admission holds dispatchMu until the new run is durable, so
// another project cannot consume the same capacity between count and claim.
func (h *hub) globalRunning() int {
	h.mu.RLock()
	kits := make([]*kit, 0, len(h.kits))
	for _, k := range h.kits {
		kits = append(kits, k)
	}
	h.mu.RUnlock()
	n := 0
	for _, k := range kits {
		if k != nil && k.srv != nil {
			n += k.srv.RunningIntentCount()
		}
	}
	return n
}

func (h *hub) globalProfileRunning(profileID string) int {
	h.mu.RLock()
	kits := make([]*kit, 0, len(h.kits))
	for _, k := range h.kits {
		kits = append(kits, k)
	}
	h.mu.RUnlock()
	n := 0
	for _, k := range kits {
		if k == nil || k.srv == nil {
			continue
		}
		for _, r := range k.srv.Store.Runs() {
			if r.Status == model.RunRunning && r.Purpose == "" && r.ResolvedProfile == profileID {
				n++
			}
		}
	}
	return n
}

func (h *hub) scopeOf(p *project.Project) string {
	if scope := p.ScopeText(); scope != "" {
		return scope
	}
	return h.cfg.Scope
}

// say delivers one operator/commander message into the live AOP session of an
// intent. It refuses a marker that points at another host or agent: sending the
// note to the wrong machine is worse than failing loudly (D69).
func (h *hub) say(ctx context.Context, p *project.Project, in model.Intent, sess aopexec.SessionInfo, text string, interrupt bool, eventID string, taskResult bool, chain []string) (string, error) {
	if h.cfg.Nodes == nil {
		return "", api.ErrSayUnavailable
	}
	// A result message with a chain is a task.result record; everything else is
	// a v1 message event.
	send := func(spec node.Spec) (string, error) {
		client := h.ioaFor(spec)
		if taskResult {
			return client.SendResult(ctx, sess.Session, text, eventID, chain)
		}
		return client.SendEvent(ctx, sess.Session, text, interrupt, eventID)
	}
	// A profile run's session lives on its own profile host, not on the node's
	// default endpoint. Resolve that host first; otherwise every message to a
	// profile executor would be refused as "session belongs to another host".
	if profile := strings.TrimSpace(in.Profile); profile != "" {
		base, token, agent, ok := h.cfg.Nodes.LocalHostForSession(profile, sess.URL)
		if !ok {
			return "", fmt.Errorf("模型配置 %s 的本机宿主不在线", profile)
		}
		if sess.URL != "" && !sameEndpoint(base, sess.URL) {
			return "", fmt.Errorf("会话属于 %s，不是模型配置 %s 的宿主 %s", sess.URL, profile, base)
		}
		if agent != "" && sess.Agent != "" && agent != sess.Agent {
			return "", fmt.Errorf("会话跑在 agent %s 上，不是 %s", sess.Agent, agent)
		}
		spec := node.Spec{ID: "profile:" + profile, AOP: base, Token: token, Agent: agent}
		msgID, err := send(spec)
		if err != nil {
			return "", fmt.Errorf("%w：%v", api.ErrSayUnavailable, err)
		}
		return msgID, nil
	}
	nodeID := strings.TrimSpace(in.Node)
	if nodeID == "" && h.cfg.Nodes != nil {
		nodeID = h.cfg.Nodes.LocalID()
	}
	if nodeID == "" {
		nodeID = h.cfg.Nodes.Default()
	}
	spec, ok := h.cfg.Nodes.Spec(nodeID)
	if !ok {
		return "", fmt.Errorf("意图的节点 %s 不在清单里", nodeID)
	}
	if spec.Audit {
		return "", fmt.Errorf("意图跑在 audit 节点 %s，没有可插话的 AOP 会话", nodeID)
	}
	if spec.AOP == "" {
		return "", api.ErrSayUnavailable
	}
	if sess.URL != "" && !sameEndpoint(spec.AOP, sess.URL) {
		return "", fmt.Errorf("会话属于 %s，不是意图节点 %s", sess.URL, spec.AOP)
	}
	if spec.Agent != "" && sess.Agent != "" && spec.Agent != sess.Agent {
		return "", fmt.Errorf("会话跑在 agent %s 上，不是 %s", sess.Agent, spec.Agent)
	}
	msgID, err := send(spec)
	if err != nil {
		return "", fmt.Errorf("%w：%v", api.ErrSayUnavailable, err)
	}
	return msgID, nil
}

func (h *hub) ioaFor(spec node.Spec) *ioa.Client {
	key := spec.AOP + "\x00" + spec.Token + "\x00" + spec.Agent
	h.ioaMu.Lock()
	defer h.ioaMu.Unlock()
	if h.ioaClients == nil {
		h.ioaClients = map[string]*ioa.Client{}
	}
	if c := h.ioaClients[key]; c != nil {
		return c
	}
	c := ioa.New(spec.AOP, spec.Token, spec.Agent)
	h.ioaClients[key] = c
	return c
}

func sameEndpoint(a, b string) bool {
	return strings.EqualFold(strings.TrimRight(strings.TrimSpace(a), "/"), strings.TrimRight(strings.TrimSpace(b), "/"))
}

// modelName is the console-visible model from the active profile. The client
// itself lives in the harness host; the control plane only needs the name.
func (h *hub) modelName() string {
	if h.settings != nil {
		return h.settings.Effective().Model
	}
	return ""
}

// modelContext is the context budget the console measures live progress
// against before the harness reports the model's resolved window: the active
// harness profile's context_window, or the unknown-model default. Once a run
// reports usage.detail.context_window, the run's own value wins (D80).
func (h *hub) modelContext() int {
	if h.settings != nil {
		if n := h.settings.Effective().ContextWindow; n > 0 {
			return n
		}
	}
	return 128000
}

// defaultID is where "/" sends the browser.
func (h *hub) defaultID() string {
	if h.cfg.DefaultProject != "" && h.mgr.Get(h.cfg.DefaultProject) != nil {
		return h.cfg.DefaultProject
	}
	if p := h.mgr.First(); p != nil {
		return p.ID
	}
	return ""
}

func (h *hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/healthz":
		// Liveness only, no instance data: a probe must not leak the ledger or
		// the build. Version sits behind auth on /api/version.
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case r.URL.Path == "/api/version":
		writeJSON(w, http.StatusOK, map[string]any{"version": buildVersion})
	case r.URL.Path == "/api/settings":
		h.serveSettings(w, r)
	case r.URL.Path == "/api/models":
		h.serveModels(w, r)
	case r.URL.Path == "/api/model-catalogue":
		h.serveModelCatalogue(w, r)
	case r.URL.Path == "/api/model-profiles":
		h.serveModelProfiles(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/model-profiles/"):
		h.serveModelProfile(w, r)
	case r.URL.Path == "/api/fleet":
		h.serveFleet(w, r)
	case r.URL.Path == "/api/ioa/events":
		h.serveIOAEvents(w, r)
	case r.URL.Path == "/api/pocs":
		h.servePOCs(w, r)
	case r.URL.Path == "/api/pocs/import":
		h.importPOCs(w, r)
	case r.URL.Path == "/api/kb":
		h.serveKB(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/kb/"):
		h.serveKBManagement(w, r)
	// The global shell: projects, the vulnerability library, POCs and settings.
	// It is an instance view, so it lives outside /p/<id>/.
	case r.URL.Path == "/g" || strings.HasPrefix(r.URL.Path, "/g/"):
		web.Index()(w, r)
		return
	case strings.HasPrefix(r.URL.Path, "/agent/"):
		h.serveAgent(w, r)
	case strings.HasPrefix(r.URL.Path, "/p/"):
		h.serveProject(w, r)
	case r.URL.Path == "/api/projects":
		h.serveProjects(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/projects/"):
		h.serveProjectItem(w, r)
	case r.URL.Path == "/api/vulns":
		h.serveVulns(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/vulns/evidence/"):
		h.serveVulnEvidence(w, r)
	case strings.HasPrefix(r.URL.Path, "/static/"):
		h.static.ServeHTTP(w, r)
	case r.URL.Path == "/":
		// The landing page is the instance view; a project is entered from it.
		http.Redirect(w, r, "/g/", http.StatusFound)
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": "接口在项目下：/p/<项目>/api/...；跨项目接口是 /api/projects 和 /api/vulns",
		})
	}
}

// serveSettings is the instance-level model configuration. It edits the active
// profile of the harness's own file (~/.cyber/cyber.yaml), so the value the
// console shows is the value the harness, the REPL and every local run use.
func (h *hub) serveSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, h.settings.View())
	case http.MethodPut:
		var in settings.View
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求体不是 JSON"})
			return
		}
		// Merge over the effective config, not just the file: a key that came
		// from the environment must survive a save that leaves the field alone.
		next := settings.ApplyView(h.settings.Effective(), in)
		next = h.clampMaxTokens(next)
		if err := next.Validate(); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		if err := h.settings.Save(settings.File{Model: next}); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		h.applyModel(next)
		writeJSON(w, http.StatusOK, h.settings.View())
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "只支持 GET / PUT"})
	}
}

// serveIOAEvents returns the observer's append-only v1 record log. It is an
// instance view: IOA messages are addressed to sessions/tasks, not to a tianwu
// project. Runtime lifecycle records are observations, never project facts.
func (h *hub) serveIOAEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "只支持 GET"})
		return
	}
	records, err := h.nativeIOARecords()
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errNoIOAObserver) {
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, map[string]any{"error": err.Error()})
		return
	}
	const limit = 500
	if len(records) > limit {
		records = records[len(records)-limit:]
	}
	cursor := ""
	if h.cfg.IOA != nil {
		cursor = h.cfg.IOA.Cursor()
	}
	// The instance view is exactly where a record from another project shows
	// up, so label every project's sessions; otherwise the console can only
	// print an opaque session id next to the message.
	sessions := make(map[string]string)
	h.mu.RLock()
	kits := make([]*kit, 0, len(h.kits))
	for _, k := range h.kits {
		kits = append(kits, k)
	}
	h.mu.RUnlock()
	for _, k := range kits {
		for id, label := range sessionLabels(k) {
			sessions[id] = label
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"cursor":   cursor,
		"records":  records,
		"sessions": sessions,
	})
}

// serveFleet is the 态势 page's single snapshot across every project. It is
// built from the kits the hub already owns, so the page never fans out one
// board request per project (and never shows N different "now"s).
func (h *hub) serveFleet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "只支持 GET"})
		return
	}
	h.mu.RLock()
	kits := make([]*kit, 0, len(h.kits))
	for _, k := range h.kits {
		kits = append(kits, k)
	}
	h.mu.RUnlock()

	sources := make([]fleet.Source, 0, len(kits))
	for _, k := range kits {
		st := decider.Status{Enabled: false}
		if k.dec != nil {
			st = k.dec.Status()
		}
		sources = append(sources, fleet.Source{
			ID: k.proj.ID, Name: k.proj.Name, Scope: h.scopeOf(k.proj),
			Store: k.srv.Store, Decider: st,
		})
	}
	writeJSON(w, http.StatusOK, fleet.Build(time.Now(), sources, h.cfg.Nodes.Infos(), h.probe.Health(r.Context())))
}

// applyModel rebuilds the decider's client, pushes the harness env/flags to the
// node registry, and restarts the local host so it reads the new profile. A
// remote AOP host keeps its own host-side configuration.
//
// A restart is a window in which the local host has no provider yet. Dispatching
// a commander round into that window used to burn a failed round ("harness 报告
// 这一轮失败 / agent provider is nil"): the wake is deferred until the host is
// ready again, which AddOnReady reports (D111). A save that changes nothing does
// not restart and wakes immediately as before.
func (h *hub) applyModel(m settings.Model) {
	m = h.clampMaxTokens(m)
	if h.cfg.Nodes != nil {
		h.cfg.Nodes.SetLLM(m.Env(), m.Args())
	}
	restarting := false
	if h.cfg.LocalHost != nil {
		// The host is a child process: it read the old profile at start. The
		// scoped agent token (never the operator's login token) rides along so
		// harness sessions keep their battlefield API access after a settings
		// save restarts the host (D109).
		env := hostEnvWithAgentToken(m.Env(), h.cfg.ConsoleToken)
		restarting = h.cfg.LocalHost.Update(env, m.Args())
	}
	// Every project's live progress bar measures against the same profile.
	cw := m.ContextWindow
	if cw <= 0 {
		cw = 128000
	}
	h.mu.RLock()
	for _, k := range h.kits {
		k.srv.SetContextWindow(cw)
		if k.dec != nil {
			name, window := k.srv.CommanderModelInfo()
			if name == "" {
				name, window = m.Model, cw
			}
			k.dec.SetModelName(name)
			k.dec.SetContextWindow(window)
		}
	}
	h.mu.RUnlock()
	if !m.Configured() {
		log.Printf("settings: 模型未配置，指挥官关闭")
	} else {
		log.Printf("settings: model=%s endpoint=%s context=%d max_tokens=%d",
			m.Model, m.BaseURL, m.ContextWindow, m.MaxTokens)
	}
	if restarting {
		log.Printf("settings: 本机宿主按新配置重启中；就绪后再唤醒各项目（避免在重启窗口开轮）")
		return
	}
	h.wakeAll("settings")
}

func (h *hub) wakeAll(reason string) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, k := range h.kits {
		if k.dec != nil {
			k.dec.Wake(reason)
		}
	}
}

// servePOCs and importPOCs are instance-level: a template library is tooling,
// not an engagement, so the global shell talks to /api/pocs directly.
func (h *hub) servePOCs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "只支持 GET"})
		return
	}
	if h.pocs == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "POC 库未配置"})
		return
	}
	// The library is thousands of templates. Search and paging happen here so
	// opening the page does not ship/render the whole library: total is the
	// whole library, matched is what q keeps (ignoring offset/limit). No
	// parameters keeps the old whole-library response for callers that want it.
	q := r.URL.Query().Get("q")
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit > 500 {
		limit = 500
	}
	all := h.pocs.List()
	page, matched := pocs.Page(all, q, offset, limit)
	writeJSON(w, http.StatusOK, map[string]any{
		"dir":     h.pocs.Dir,
		"pocs":    page,
		"total":   len(all),
		"matched": matched,
	})
}

func (h *hub) importPOCs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "只支持 POST"})
		return
	}
	if h.pocs == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "POC 库未配置"})
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求体不是 JSON"})
		return
	}
	p := strings.TrimSpace(req.Path)
	if p == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "需要 path：模板文件或目录（相对 <data>）"})
		return
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(h.cfg.DataDir, filepath.FromSlash(p))
	}
	res, err := h.pocs.Import(p)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// serveKB is the instance-level offline library: world knowledge shared by
// every project and read by both roles. The console reads it whole (a few
// hundred KB at most) and filters client-side, so a bare request returns every
// entry; q/tags/limit switch to the scored search, which is what the commander
// needs — pulling the whole library per lookup is tens of KB of context for one
// fact. The executor does not call this endpoint: it reads the case copy.
func (h *hub) serveKB(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "只支持 GET"})
		return
	}
	if h.cfg.KB == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "知识库未配置"})
		return
	}
	query := r.URL.Query()
	q := strings.TrimSpace(query.Get("q"))
	var tags []string
	for _, raw := range query["tags"] {
		for _, part := range strings.Split(raw, ",") {
			if tag := strings.TrimSpace(part); tag != "" {
				tags = append(tags, tag)
			}
		}
	}
	limit, _ := strconv.Atoi(query.Get("limit"))

	entries := h.cfg.KB.Entries()
	if q != "" || len(tags) > 0 || query.Has("limit") {
		entries = h.cfg.KB.Search(q, tags, limit)
	}
	if entries == nil {
		entries = []kb.Entry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"dir":               h.cfg.KBDir,
		"entries":           entries,
		"revisions":         kb.Revisions(entries),
		"deleted_revisions": h.cfg.KB.DeletedRevisions(),
	})
}

// serveAgent mounts the per-project participant API under
// /agent/<project>/api/. Authentication is the run's collab token, resolved to
// a project+participant+run; the console token is deliberately not accepted
// here, and the project in the URL must match the token.
func (h *hub) serveAgent(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/agent/")
	projectID, tail, ok := strings.Cut(rest, "/")
	if !ok || strings.TrimSpace(projectID) == "" || !strings.HasPrefix(tail, "api/") {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "协作接口路径是 /agent/<项目>/api/..."})
		return
	}
	if h.cfg.Collab == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "协作身份未启用"})
		return
	}
	token := ""
	if hdr := r.Header.Get("Authorization"); strings.HasPrefix(hdr, "Bearer ") {
		token = strings.TrimSpace(strings.TrimPrefix(hdr, "Bearer "))
	}
	ident, ok := h.cfg.Collab.Resolve(token)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "协作令牌无效或已过期"})
		return
	}
	if ident.ProjectID != projectID {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "协作令牌不属于这个项目"})
		return
	}
	k := h.kit(projectID)
	if k == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "没有这个项目：" + projectID})
		return
	}
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/" + tail
	r2.URL.RawPath = ""
	r2 = r2.WithContext(api.WithIdentity(r2.Context(), ident))
	k.srv.AgentHandler().ServeHTTP(w, r2)
}

func (h *hub) serveProject(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/p/")
	id, tail, _ := strings.Cut(rest, "/")
	k := h.kit(id)
	if k == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "没有这个项目：" + id})
		return
	}
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/" + tail
	r2.URL.RawPath = ""
	if r2.URL.Path == "/api/ioa/events" {
		h.serveProjectIOA(w, r2, k)
		return
	}
	k.mux.ServeHTTP(w, r2)
}

type projectInfo struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Scope       string    `json:"scope,omitempty"`
	Description string    `json:"description,omitempty"`
	Created     time.Time `json:"created"`
	Intents     int       `json:"intents"`
	Facts       int       `json:"facts"`
	Evidence    int       `json:"evidence"`
	Running     int       `json:"running"`
	// MaxRuns is this project's own override; null means "inherit the instance
	// default". MaxRunsEffective is what dispatch enforces tonight (0 = 不限).
	MaxRuns          *int `json:"max_runs"`
	MaxRunsEffective int  `json:"max_runs_effective"`
	// RoundTimeout is this project's own commander round wall clock (a duration
	// string like "15m"); empty means "inherit the instance --round-timeout"
	// and "0" means unlimited (D91).
	RoundTimeout string `json:"round_timeout,omitempty"`
	// WorkerProfile / StrongProfile are the project's tier defaults, empty when
	// it inherits the instance active profile (D89).
	WorkerProfile string `json:"worker_profile,omitempty"`
	StrongProfile string `json:"strong_profile,omitempty"`
	// VerifyAuto is this project's own review policy; null means "inherit the
	// instance --verify-auto". VerifyAutoEffective is what runs started now
	// actually use (D100).
	VerifyAuto          *bool `json:"verify_auto"`
	VerifyAutoEffective bool  `json:"verify_auto_effective"`
}

func (h *hub) info(p *project.Project) projectInfo {
	db := p.Store.Snapshot()
	out := projectInfo{
		ID:               p.ID,
		Name:             p.Name,
		Scope:            p.ScopeText(),
		Description:      p.DescriptionText(),
		Created:          p.Created,
		Intents:          len(db.Intents),
		Facts:            len(db.Facts),
		Evidence:         len(db.Evidence),
		MaxRunsEffective: h.cfg.MaxRuns,
	}
	out.WorkerProfile, out.StrongProfile = p.ProfileDefaults()
	out.RoundTimeout = p.RoundTimeoutRaw()
	out.VerifyAutoEffective = h.projectVerifyAuto(p)
	if v, ok := p.VerifyAutoOverride(); ok {
		out.VerifyAuto = &v
	}
	if v, ok := p.MaxRunsOverride(); ok {
		out.MaxRuns = &v
		out.MaxRunsEffective = v
	}
	for _, r := range db.Runs {
		if r.Status == "running" {
			out.Running++
		}
	}
	return out
}

// commanderRoundTimeout is the cap this project's decider runs rounds under:
// the manifest override when it has a valid one, else the instance
// --round-timeout. A hand-edited invalid value is reported and ignored here
// rather than silently becoming "inherit" (D91).
func (h *hub) commanderRoundTimeout(p *project.Project) time.Duration {
	if p == nil {
		return h.cfg.RoundTimeout
	}
	d, ok, err := project.ParseRoundTimeout(p.RoundTimeoutRaw())
	if err != nil {
		log.Printf("project %s: round_timeout 非法（%v）：用实例默认 %s", p.ID, err, h.cfg.RoundTimeout)
		return h.cfg.RoundTimeout
	}
	if ok {
		return d
	}
	return h.cfg.RoundTimeout
}

// projectVerifyAuto is the review policy a run started now uses: the
// project's own override when the manifest has one, else the instance
// --verify-auto. It is read on every kit save and on every intent settle, so a
// settings edit applies to the next finished run without a restart (D100).
func (h *hub) projectVerifyAuto(p *project.Project) bool {
	if p == nil {
		return h.cfg.VerifyAuto
	}
	if v, ok := p.VerifyAutoOverride(); ok {
		return v
	}
	return h.cfg.VerifyAuto
}

func (h *hub) serveProjects(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		all := h.mgr.All()
		out := make([]projectInfo, 0, len(all))
		for _, p := range all {
			out = append(out, h.info(p))
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"projects": out, "max_runs": h.cfg.MaxRuns,
			// The instance default for --round-timeout rides along so the
			// per-project form can show "inherit …" instead of guessing.
			"round_timeout_seconds": int(h.cfg.RoundTimeout / time.Second),
			// Same for --verify-auto: the review form shows what "inherit"
			// means before the operator changes it (D100).
			"verify_auto_default": h.cfg.VerifyAuto,
		})
	case http.MethodPost:
		var req struct {
			Name  string `json:"name"`
			Scope string `json:"scope"`
			// Description is the operator's prose: boundaries, forbidden
			// actions, platform rules, how to treat target output. The program
			// carries it into every brief and never parses it (D59).
			Description string `json:"description"`
			// A project is created together with its first goal: an engagement
			// without a statement has nothing to judge "done" against.
			Goal             string `json:"goal"`
			QuantifiedTarget string `json:"quantified_target"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求体不是 JSON"})
			return
		}
		// The simplified console asks for one paragraph and no name: derive a
		// readable project name from it instead of rejecting the request. An
		// explicit name still wins, so programmatic callers are unchanged.
		name := strings.TrimSpace(req.Name)
		if name == "" {
			name = projectNameFromBrief(req.Scope)
		}
		p, err := h.mgr.Create(name, req.Scope)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		if desc := strings.TrimSpace(req.Description); desc != "" {
			if err := h.mgr.SetDescription(p.ID, desc); err != nil {
				log.Printf("project %s: 写描述失败：%v", p.ID, err)
			}
		}
		h.attach(p)
		if goal := strings.TrimSpace(req.Goal); goal != "" {
			if _, err := p.Store.AddGoal(model.Goal{
				Statement:        goal,
				QuantifiedTarget: strings.TrimSpace(req.QuantifiedTarget),
			}); err != nil {
				log.Printf("project %s: 建目标失败：%v", p.ID, err)
			}
		}
		log.Printf("project: 新建 %s（%s）", p.ID, p.Name)
		writeJSON(w, http.StatusCreated, h.info(p))
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "只支持 GET / POST"})
	}
}

func (h *hub) serveProjectItem(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/projects/")
	if id == "" || strings.Contains(id, "/") {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "没有这个项目"})
		return
	}
	switch r.Method {
	case http.MethodPut:
		h.updateProject(w, r, id)
	case http.MethodDelete:
		h.deleteProject(w, r, id)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "只支持 PUT / DELETE"})
	}
}

// updateProject edits the manifest fields the console owns. Every field is
// optional: an omitted one keeps its value, so the concurrency form can save
// without resending the scope (and vice versa). max_runs additionally accepts
// null, which restores "inherit the instance --max-runs". worker_profile /
// strong_profile accept "" to clear the project default (inherit the instance
// active profile). verify_auto accepts null (inherit --verify-auto) or a bool
// (D100).
func (h *hub) updateProject(w http.ResponseWriter, r *http.Request, id string) {
	var req struct {
		Scope         *string         `json:"scope"`
		Description   *string         `json:"description"`
		MaxRuns       json.RawMessage `json:"max_runs"`
		RoundTimeout  *string         `json:"round_timeout"`
		WorkerProfile *string         `json:"worker_profile"`
		StrongProfile *string         `json:"strong_profile"`
		VerifyAuto    json.RawMessage `json:"verify_auto"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求体不是 JSON"})
		return
	}
	p := h.mgr.Get(id)
	if p == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "项目不存在"})
		return
	}
	// A settings card submits several fields together. Validate the complete
	// request before any setter persists it, so a bad later field cannot leave
	// an earlier setting applied behind a 400 response.
	var limit *int
	var auto *bool
	var err error
	if len(req.MaxRuns) > 0 {
		limit, err = parseMaxRuns(req.MaxRuns)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
	}
	if req.RoundTimeout != nil {
		if _, _, err := project.ParseRoundTimeout(*req.RoundTimeout); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
	}
	if len(req.VerifyAuto) > 0 {
		auto, err = parseVerifyAuto(req.VerifyAuto)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
	}
	worker, strong := p.ProfileDefaults()
	if req.WorkerProfile != nil {
		worker = strings.TrimSpace(*req.WorkerProfile)
	}
	if req.StrongProfile != nil {
		strong = strings.TrimSpace(*req.StrongProfile)
	}
	if req.WorkerProfile != nil || req.StrongProfile != nil {
		for role, profileID := range map[string]string{"worker_profile": worker, "strong_profile": strong} {
			if err := h.validateProfileDefault(role, profileID); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
				return
			}
		}
	}
	if req.Scope != nil {
		if err := h.mgr.SetScope(id, *req.Scope); err != nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
			return
		}
	}
	if req.Description != nil {
		if err := h.mgr.SetDescription(id, *req.Description); err != nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
			return
		}
	}
	if len(req.MaxRuns) > 0 {
		if err := h.mgr.SetMaxRuns(id, limit); err != nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
			return
		}
	}
	if req.RoundTimeout != nil {
		if err := h.mgr.SetRoundTimeout(id, *req.RoundTimeout); err != nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
			return
		}
	}
	if len(req.VerifyAuto) > 0 {
		if err := h.mgr.SetVerifyAuto(id, auto); err != nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
			return
		}
	}
	if req.WorkerProfile != nil || req.StrongProfile != nil {
		if err := h.mgr.SetProfileDefaults(id, worker, strong); err != nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
			return
		}
	}
	if k := h.kit(id); k != nil {
		k.srv.Scope = h.scopeOf(k.proj)
		k.srv.Description = k.proj.DescriptionText()
		// The tier defaults are read on every dispatch; refresh the API's
		// snapshot now so the next intent already uses the new default.
		k.srv.ProjectProfiles = projectProfileDefaults(k.proj)
		// The review policy is read when a run settles; refresh the snapshot
		// now so the very next finished run already follows the new setting.
		k.srv.SetAutoVerify(h.projectVerifyAuto(k.proj))
		k.srv.SchedulePendingReviews()
		if k.dec != nil {
			k.dec.Scope = k.srv.Briefing()
			if name, window := k.srv.CommanderModelInfo(); name != "" {
				k.dec.SetModelName(name)
				k.dec.SetContextWindow(window)
			}
			// Round wall clock follows the manifest: the edit applies from the
			// next round, no restart, and no running executor is interrupted.
			k.dec.SetRoundTimeout(h.commanderRoundTimeout(k.proj))
		}
		if k.srv.Verifier != nil {
			k.srv.Verifier.Scope = k.srv.Briefing()
		}
		if k.srv.Reports != nil {
			k.srv.Reports.Scope = k.srv.Briefing()
		}
	}
	writeJSON(w, http.StatusOK, h.info(h.mgr.Get(id)))
}

// parseMaxRuns decodes the optional cap: null clears the override, a
// non-negative integer sets it. 0 is legal and means uncapped.
func parseMaxRuns(raw json.RawMessage) (*int, error) {
	if strings.TrimSpace(string(raw)) == "null" {
		return nil, nil
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil {
		return nil, errors.New("max_runs 必须是整数或 null")
	}
	if n < 0 {
		return nil, errors.New("max_runs 不能是负数（0 = 不限）")
	}
	return &n, nil
}

// parseVerifyAuto decodes the optional review policy: null restores "inherit
// the instance --verify-auto", true/false set the project's own choice. A
// string is a 400 rather than a silent coercion.
func parseVerifyAuto(raw json.RawMessage) (*bool, error) {
	if strings.TrimSpace(string(raw)) == "null" {
		return nil, nil
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, errors.New("verify_auto 必须是 true / false / null")
	}
	return &b, nil
}

// projectProfileDefaults is the API's snapshot of the manifest's tier defaults.
// nil means "no project default": dispatch falls back to the instance active
// profile, which is the pre-tier behaviour old projects keep.
func projectProfileDefaults(p *project.Project) *api.ProfileDefaults {
	if p == nil {
		return nil
	}
	worker, strong := p.ProfileDefaults()
	if worker == "" && strong == "" {
		return nil
	}
	return &api.ProfileDefaults{Worker: worker, Strong: strong}
}

// validateProfileDefault refuses a tier default the instance cannot run. A
// project pointing at a deleted or unfinished profile would otherwise make
// every dispatch fail with one opaque error; refusing the save is where the
// typo is still visible.
func (h *hub) validateProfileDefault(role, id string) error {
	id = strings.TrimSpace(id)
	if id == "" || h.settings == nil {
		return nil
	}
	cat, err := h.settings.Profiles()
	if err != nil {
		return fmt.Errorf("%s：读模型配置：%w", role, err)
	}
	for _, p := range cat.Profiles {
		if p.ID != id {
			continue
		}
		if !p.Runnable {
			return fmt.Errorf("%s：模型配置 %s 还不能执行（base_url、model、api_key 要齐全）", role, id)
		}
		return nil
	}
	return fmt.Errorf("%s：没有模型配置 %s", role, id)
}

// deleteProject removes the engagement: everything below projects/<id>/ goes,
// the process stops touching it first, and the vulnerability library keeps what
// it mirrored (D34). There is no recycle bin on purpose; the confirm parameter
// is the only guard, and it asks for the id you are about to destroy.
func (h *hub) deleteProject(w http.ResponseWriter, r *http.Request, id string) {
	if r.URL.Query().Get("confirm") != id {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "删除会删掉整个项目，需要 confirm=<项目 id>",
		})
		return
	}
	p := h.mgr.Get(id)
	if p == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "没有这个项目：" + id})
		return
	}

	// Stop writing before removing: a live run's finish() would recreate files
	// in a directory this call is about to delete. Close the dispatch gate
	// first, cancel runs, and only stop the decider/watchdog once nothing is
	// left; a timeout must leave the project working instead of permanently
	// half-stopped (the old order canceled the kit before checking).
	if k := h.kit(id); k != nil {
		k.srv.SetDeleting(true)
		if n := k.srv.CancelAll(); n > 0 {
			log.Printf("project: %s 有 %d 个运行在收尾", id, n)
		}
		if !k.srv.WaitIdle(15 * time.Second) {
			k.srv.SetDeleting(false)
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": "还有运行没收尾，稍后重试",
			})
			return
		}
		if err := k.srv.CheckBrowserReleaseForDeletion(); err != nil {
			k.srv.SetDeleting(false)
			writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
			return
		}
		k.cancel()
	}

	if err := os.RemoveAll(p.Dir); err != nil {
		// The directory is still here, so the project must stay usable: an
		// operator can retry the delete or keep working. The old kit's context
		// is already canceled (decider, watchdog, purpose runs), so reopening
		// only the gate would leave a live-looking project with a dead brain.
		// Rebuild the runtime instead; the new kit starts with the delete gate
		// open and a fresh project context (D75).
		if nk := h.attach(p); nk != nil {
			nk.srv.SetDeleting(false)
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": "目录没删干净：" + err.Error(),
		})
		return
	}
	h.mu.Lock()
	delete(h.kits, id)
	h.mu.Unlock()
	h.mgr.Remove(id)

	kept, err := h.vulns.MarkOrphaned(id)
	if err != nil {
		log.Printf("vulnlib: 标记 %s 的条目失败：%v", id, err)
	}
	log.Printf("project: 删除 %s（漏洞库保留 %d 条）", id, kept)
	writeJSON(w, http.StatusOK, map[string]any{
		"deleted":    id,
		"vulns_kept": kept,
		"projects":   h.mgr.Len(),
	})
}

func (h *hub) serveVulns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "只支持 GET"})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	project := r.URL.Query().Get("project")
	q := r.URL.Query().Get("q")
	list := h.vulns.List(project, q, limit)
	writeJSON(w, http.StatusOK, map[string]any{
		"vulns": list,
		// total is the whole library; matched is what the current filter hits,
		// including rows beyond this page's limit.
		"total":   h.vulns.Len(),
		"matched": h.vulns.Count(project, q),
	})
}

// serveVulnEvidence streams one evidence copy from the vulnerability library.
//
// The library copies attacker-influenced bytes (a captured body, a dumped
// binary) and serves them from the console's own origin, so the default is a
// preview, not a download: inline, never sniffed, and CSP-sandboxed when the
// browser would render the file as a document. Saving the original is the
// explicit ?download=1 action. This mirrors the project evidence endpoint on
// the same rule; the document-type decision is shared through the evidence
// package instead of being copied per handler.
func (h *hub) serveVulnEvidence(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/api/vulns/evidence/")
	path, ok := h.vulns.EvidencePath(rel)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "没有这份证据"})
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.URL.Query().Get("download") == "1" {
		// FormatMediaType is the single writer of this header: it emits filename*
		// (RFC 2231/5987) for non-ASCII names and quotes specials.
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": pathpkg.Base(rel)}))
	} else {
		w.Header().Set("Content-Disposition", "inline")
		// The console's preview sheet frames PDFs; the instance-wide
		// X-Frame-Options: DENY (withSecurityHeaders) would block that
		// same-origin frame. Only this endpoint relaxes it, and document types
		// are CSP-sandboxed below.
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		if ct, ok := evidence.DocumentType("", path); ok {
			w.Header().Set("Content-Type", ct)
			w.Header().Set("Content-Security-Policy", "sandbox")
		}
	}
	http.ServeFile(w, r, path)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// projectNameFromBrief derives a display name from the one-paragraph project
// brief. The simplified console does not ask for a name; an empty paragraph is
// still a legal (if useless) engagement, so the fallback is explicit instead of
// an error from Manager.Create.
func projectNameFromBrief(brief string) string {
	for _, line := range strings.Split(brief, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			r := []rune(line)
			if len(r) > 40 {
				return string(r[:40]) + "…"
			}
			return line
		}
	}
	return "未命名项目"
}
