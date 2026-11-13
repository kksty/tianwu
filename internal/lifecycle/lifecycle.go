// Package lifecycle owns the non-HTTP run lifecycle of a project: admission
// gates, the single claim of a direction, the live-run registries and their
// cancellation causes, settlement transitions, crash recovery and the
// watchdog reapers.
//
// The HTTP layer keeps what is HTTP — decoding, identity/ownership mapping,
// prompt and journal assembly, views — and supplies it as Options seams. There
// is exactly one implementation of the lifecycle: api handlers call this
// service rather than keeping a second copy of the state machine.
//
// The service is per project. The atomics in Gates are process/project lifetime
// flags that other API handlers already read (pause, delete, shutdown, review
// pause); the service is the one component that decides what those gates mean
// for dispatch and cancellation.
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"github.com/kksty/tianwu/internal/collab"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// Gates are the project's admission switches. They are shared with the API
// handlers that display or flip them; the lifecycle service is the writer for
// pause/delete/shutdown/review pause dispatch semantics.
type Gates struct {
	Closing       *atomic.Bool
	Paused        *atomic.Bool
	Deleting      *atomic.Bool
	ReviewsPaused *atomic.Bool
}

// Options are the dependencies the API supplies. A nil required callback is a
// programming error and New panics: a silently missing admission check would be
// a security hole, not a degraded mode.
type Options struct {
	Store *store.Store
	Gates Gates

	// InstanceDispatch returns the hub's cross-project admission mutex. It is a
	// getter rather than a value because the hub assigns the field after New.
	// A nil getter (or nil result) keeps the per-project dispatchMu alone, which
	// is what bare tests use.
	InstanceDispatch func() *sync.Mutex

	// Capacity reads are callbacks, not copied values: the console and tests
	// edit the project cap/profile catalogue at runtime.
	ProjectLimit         func() int
	RunningIntentCount   func() int
	MaxWorkers           func() int
	GlobalRunning        func() int
	CheckProfileCapacity func(model.Run) error
	// BlockedPremises lists from_facts whose premise no longer stands.
	BlockedPremises func(model.Intent) []string

	// PlanRun resolves node and model binding and returns the run row whose
	// frozen versions the claim will persist. It must not claim the intent.
	PlanRun func(in model.Intent, continuedFrom, profileOverride, fallbackFrom string) (model.Run, string, error)
	// Dispatch runs one admitted run to settlement. The service owns the
	// goroutine and the cancel handle; Dispatch owns prompt/session/journal.
	Dispatch func(ctx context.Context, in model.Intent, runID, nodeID string, resume bool, prompt string, keepPrompt bool)

	// Settlement seams keep journal/identity/ingest/prompt in the API.
	RevokeRun            func(runID string)
	RemoveRunIdentity    func(intentID, runID string)
	Redact               func(string) string
	AfterSettle          func(intentID, runID, status string)
	WakeDecider          func(reason string)
	AutoVerify           func(intentID string)
	AfterProviderFailure func(runID, intentID, status string, res executor.Result)

	// Pump is the API scheduler; Recover and the reapers wake it after a fix.
	Pump func()
	// PauseDecider stops the commander during BeginShutdown; nil is allowed.
	PauseDecider func(reason string)
	// BrowserActiveCount reports in-flight native browser commands for WaitIdle.
	BrowserActiveCount func() int
}

// Service is the per-project lifecycle implementation.
type Service struct {
	o Options

	// mu guards the run registries. It is deliberately separate from the API
	// server's generic mutex so registry state has one owner.
	mu            sync.Mutex
	dispatchMu    sync.Mutex
	cancels       map[string]context.CancelFunc
	canceled      map[string]bool
	cancelReasons map[string]string
	cancelCauses  map[string]string
	purposeRuns   map[string]*purposeRun
	reviewRuns    map[string]*purposeRun

	// restarted is consumed by the commander's first round after a crash sweep.
	restarted atomic.Bool
}

// purposeRun is one report or verifier run: the service only needs its cancel
// func and the structured cause the owner reports when it unregisters.
type purposeRun struct {
	cancel context.CancelFunc
	cause  string
}

// New builds the service. Required callbacks are validated by mustOptions.
func New(o Options) *Service {
	mustOptions(o)
	return &Service{
		o:             o,
		cancels:       map[string]context.CancelFunc{},
		canceled:      map[string]bool{},
		cancelReasons: map[string]string{},
		cancelCauses:  map[string]string{},
		purposeRuns:   map[string]*purposeRun{},
		reviewRuns:    map[string]*purposeRun{},
	}
}

func mustOptions(o Options) {
	missing := func(name string) {
		panic(fmt.Sprintf("lifecycle.New: missing required option %s", name))
	}
	switch {
	case o.Store == nil:
		// The API only builds a service for a real project store; a nil store is
		// a bare test server, which has no lifecycle and must not construct one.
		missing("Store")
	case o.Gates.Closing == nil || o.Gates.Paused == nil || o.Gates.Deleting == nil || o.Gates.ReviewsPaused == nil:
		missing("Gates")
	case o.ProjectLimit == nil:
		missing("ProjectLimit")
	case o.RunningIntentCount == nil:
		missing("RunningIntentCount")
	case o.CheckProfileCapacity == nil:
		missing("CheckProfileCapacity")
	case o.BlockedPremises == nil:
		missing("BlockedPremises")
	case o.PlanRun == nil:
		missing("PlanRun")
	case o.Dispatch == nil:
		missing("Dispatch")
	case o.RevokeRun == nil || o.RemoveRunIdentity == nil || o.Redact == nil:
		missing("settlement seams")
	case o.AfterSettle == nil || o.WakeDecider == nil:
		missing("settlement seams")
	}
}

// ---- admission flags ----

// SetPaused arms/disarms the project-level dispatch gate.
func (s *Service) SetPaused(paused bool) { s.o.Gates.Paused.Store(paused) }

// SetDeleting closes dispatch for a project that is being removed.
func (s *Service) SetDeleting(deleting bool) { s.o.Gates.Deleting.Store(deleting) }

// SetReviewsPaused arms/disarms the review kill switch (D137).
func (s *Service) SetReviewsPaused(paused bool) { s.o.Gates.ReviewsPaused.Store(paused) }

// Paused reports the project pause flag.
func (s *Service) Paused() bool { return s.o.Gates.Paused.Load() }

// Deleting reports the project deletion flag.
func (s *Service) Deleting() bool { return s.o.Gates.Deleting.Load() }

// Closing reports the process shutdown flag.
func (s *Service) Closing() bool { return s.o.Gates.Closing.Load() }

// ReviewsPaused reports the review kill switch.
func (s *Service) ReviewsPaused() bool { return s.o.Gates.ReviewsPaused.Load() }

// Restarted reports whether a crash sweep settled leftover runs. The commander
// consumes it once so its first round knows the note may name cancelled runs.
func (s *Service) Restarted() bool { return s.restarted.Load() }

// TakeRestarted consumes the crash-recovery flag exactly once.
func (s *Service) TakeRestarted() bool { return s.restarted.CompareAndSwap(true, false) }

// ---- run registry: cancellation and tracking ----

// CancelRun stops a live run on the operator's behalf.
func (s *Service) CancelRun(runID string) error {
	return s.CancelRunWithCause(runID, model.CancelCauseOperator, "已取消（人工或重派）")
}

// CancelRunWithCause stops a live run and records the structured cause so the
// console can tell a continuation from a failure.
func (s *Service) CancelRunWithCause(runID, cause, reason string) error {
	return s.cancelWithCause(runID, cause, reason)
}

// CancelRunWithReason is the watchdog's version: the run log says why the
// control plane stopped it, not "a human did it".
func (s *Service) CancelRunWithReason(runID, reason string) error {
	return s.cancelWithCause(runID, model.CancelCauseWatchdog, reason)
}

func (s *Service) cancelWithCause(runID, cause, reason string) error {
	s.mu.Lock()
	if p, ok := s.reviewRuns[runID]; ok {
		// A verifier run has no intent to return to pending; it only needs the
		// cancel func and the structured cause when it settles.
		p.cause = cause
		s.mu.Unlock()
		p.cancel()
		return nil
	}
	if p, ok := s.purposeRuns[runID]; ok {
		// A purpose run has no intent to return to pending either.
		p.cause = cause
		s.mu.Unlock()
		p.cancel()
		return nil
	}
	cancel, ok := s.cancels[runID]
	if ok {
		s.canceled[runID] = true
		s.cancelReasons[runID] = reason
		s.cancelCauses[runID] = cause
	}
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("run %s 不在运行", runID)
	}
	cancel()
	return nil
}

// CancelReason is the human-readable reason the control plane recorded for a
// run's stop, "" when none was recorded. It is the run log's "why", not a
// verdict.
func (s *Service) CancelReason(runID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cancelReasons[runID]
}

// CancelCause is the structured reason a canceled run ended, "" when it did not
// go through the registry.
func (s *Service) CancelCause(runID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cancelCauses[runID]
}

// SetCancelCause records why a run ended for paths that do not go through
// cancelWithCause (today: the context deadline in dispatch).
func (s *Service) SetCancelCause(runID, cause string) {
	s.mu.Lock()
	s.cancelCauses[runID] = cause
	s.mu.Unlock()
}

// WasCanceled reports whether the control plane asked this run to stop.
func (s *Service) WasCanceled(runID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.canceled[runID]
}

// RunActive reports whether any registry still owns a cancel handle for the run.
// It is the browser lease's "holder is still exiting" check.
func (s *Service) RunActive(runID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cancels[runID] != nil || s.purposeRuns[runID] != nil || s.reviewRuns[runID] != nil
}

// HasReviewRun reports whether a verifier run is registered. It closes the gap
// between the verifier marking a fact busy and registering its cancel func; the
// review switch and delete/shutdown use the same registration snapshot.
func (s *Service) HasReviewRun() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reviewRuns) > 0
}

// BeginShutdown seals admission before canceling runs. A child settling to
// pending must not be picked up by the pump during process exit.
func (s *Service) BeginShutdown() int {
	s.o.Gates.Closing.Store(true)
	if s.o.PauseDecider != nil {
		s.o.PauseDecider("控制台正在关闭")
	}
	return s.CancelAll()
}

// CancelAll cancels the current run snapshot without sealing new admission.
func (s *Service) CancelAll() int {
	return s.CancelAllWithCause(model.CancelCauseRestart, "项目停止：取消所有运行")
}

// CancelAllWithCause is CancelAll with a structured reason recorded on every
// stopped run. Purpose runs settle as canceled with that cause.
func (s *Service) CancelAllWithCause(cause, reason string) int {
	s.mu.Lock()
	ids := make([]string, 0, len(s.cancels)+len(s.purposeRuns)+len(s.reviewRuns))
	for id := range s.cancels {
		ids = append(ids, id)
	}
	for id := range s.purposeRuns {
		ids = append(ids, id)
	}
	for id := range s.reviewRuns {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		_ = s.cancelWithCause(id, cause, reason)
	}
	return len(ids)
}

// CancelProjectWorkWithCause stops what the project pause owns: intent runs and
// report runs. Verifier runs are deliberately not in this snapshot — a review is
// an operator-triggered re-proof and has its own switch (D137).
func (s *Service) CancelProjectWorkWithCause(cause, reason string) int {
	s.mu.Lock()
	ids := make([]string, 0, len(s.cancels)+len(s.purposeRuns))
	for id := range s.cancels {
		ids = append(ids, id)
	}
	for id := range s.purposeRuns {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		_ = s.cancelWithCause(id, cause, reason)
	}
	return len(ids)
}

// CancelReviewsWithCause stops the live reviews only; project work is untouched.
func (s *Service) CancelReviewsWithCause(cause, reason string) int {
	s.mu.Lock()
	ids := make([]string, 0, len(s.reviewRuns))
	for id := range s.reviewRuns {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		_ = s.cancelWithCause(id, cause, reason)
	}
	return len(ids)
}

// TrackPurposeRun hands one report run to the lifecycle. The returned func
// unregisters it and reports the structured cancel cause ("" when nobody
// stopped it); report.Generate calls it exactly once after the executor returns
// and before the run row is settled.
func (s *Service) TrackPurposeRun(runID string, cancel context.CancelFunc) func() string {
	s.mu.Lock()
	p := &purposeRun{cancel: cancel}
	// Close the gap between the preflight pause check and registration: pause
	// may already have taken its CancelAll snapshot by the time we get here.
	if s.o.Gates.Closing.Load() {
		p.cause = model.CancelCauseRestart
		cancel()
	} else if s.o.Gates.Paused.Load() || s.o.Gates.Deleting.Load() {
		p.cause = model.CancelCausePause
		cancel()
	}
	s.purposeRuns[runID] = p
	s.mu.Unlock()
	return s.untrackRun(s.purposeRuns, runID)
}

// TrackReviewRun hands one verifier run to the lifecycle. The project pause does
// not cancel it: only delete/shutdown, or the explicit review switch (D137).
func (s *Service) TrackReviewRun(runID string, cancel context.CancelFunc) func() string {
	s.mu.Lock()
	p := &purposeRun{cancel: cancel}
	// Close the gap between the preflight check and registration.
	switch {
	case s.o.Gates.Closing.Load():
		p.cause = model.CancelCauseRestart
		cancel()
	case s.o.Gates.Deleting.Load():
		p.cause = model.CancelCausePause
		cancel()
	case s.o.Gates.ReviewsPaused.Load():
		p.cause = model.CancelCauseReviewPause
		cancel()
	}
	s.reviewRuns[runID] = p
	s.mu.Unlock()
	return s.untrackRun(s.reviewRuns, runID)
}

// untrackRun returns the idempotent unregister func shared by both trackers.
func (s *Service) untrackRun(registry map[string]*purposeRun, runID string) func() string {
	var once sync.Once
	var cause string
	return func() string {
		once.Do(func() {
			s.mu.Lock()
			if p := registry[runID]; p != nil {
				cause = p.cause
			}
			delete(registry, runID)
			s.mu.Unlock()
		})
		return cause
	}
}

// WaitIdle reports whether every run of this project has finished, waiting up
// to d. A run that ignores its cancellation keeps the directory busy, and a
// delete must not race with it.
func (s *Service) WaitIdle(d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		s.mu.Lock()
		n := len(s.cancels) + len(s.purposeRuns) + len(s.reviewRuns)
		s.mu.Unlock()
		if s.o.BrowserActiveCount != nil {
			n += s.o.BrowserActiveCount()
		}
		if n == 0 && !s.hasPurposeRun() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// hasPurposeRun reports a live verifier/report run. It is a read of the run
// table, not a registry: those runs have no cancel func to consult.
func (s *Service) hasPurposeRun() bool {
	for _, r := range s.o.Store.Runs() {
		if r.Status == model.RunRunning && r.Purpose != "" {
			return true
		}
	}
	return false
}

// HasCapacity reports whether the project has a free intent slot. The scheduler
// and the spawn precheck use it so "can I start another worker" has one answer.
func (s *Service) HasCapacity() bool {
	limit := s.o.ProjectLimit()
	return limit <= 0 || s.o.RunningIntentCount() < limit
}

// ProjectCapacity returns the live intent count and the project cap (0 = no
// cap) for messages that explain a refusal without guessing values.
func (s *Service) ProjectCapacity() (running, limit int) {
	return s.o.RunningIntentCount(), s.o.ProjectLimit()
}

// ---- start ----

// Start claims one direction and launches its run. It is the only admission
// entry point: handlers and the scheduler both call it, so the closing/deleting/
// paused gates, project/instance/profile capacity, blocked premises, CAS claim
// and cancel-handle publication cannot drift between callers.
//
// resumed/prompt semantics are the caller's: a fresh brief, a continued turn, a
// wake or a replacement session all arrive here with the prompt already built
// (keeping prompt assembly in the API).
func (s *Service) Start(id, prompt, continuedFrom string, resume, keepPrompt bool, profileOverride, fallbackFrom, owner string) (string, error) {
	if s.o.Gates.Closing.Load() {
		return "", errors.New("控制台正在关闭：不能再派发")
	}
	if s.o.Gates.Deleting.Load() {
		return "", errors.New("项目正在删除：不能再派发")
	}
	if s.o.Gates.Paused.Load() {
		return "", errors.New("项目已暂停：点「继续」后再派发")
	}
	// Concurrent starts are serialized so the cap is a real cap: two API calls
	// arriving together must not both observe "one slot free".
	if s.o.InstanceDispatch != nil {
		if lock := s.o.InstanceDispatch(); lock != nil {
			lock.Lock()
			defer lock.Unlock()
		}
	}
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()

	in, ok := s.o.Store.GetIntent(id)
	if !ok {
		return "", fmt.Errorf("intent %s 不存在", id)
	}
	if limit := s.o.ProjectLimit(); limit > 0 && s.o.RunningIntentCount() >= limit {
		return "", fmt.Errorf("并发已到上限 %d：等一个 run 结束，或在「设置 → 项目并发」/ --max-runs 调大", limit)
	}
	if maxWorkers := s.o.MaxWorkers(); maxWorkers > 0 && s.o.GlobalRunning != nil {
		if running := s.o.GlobalRunning(); running >= maxWorkers {
			return "", fmt.Errorf("实例并发已到上限 %d（--max-workers）：等一个 run 结束再派发", maxWorkers)
		}
	}
	// The graph is the memory: a direction whose from_facts are gone or were
	// rejected is dead work, not queued work (D52).
	if blocked := s.o.BlockedPremises(in); len(blocked) > 0 {
		return "", fmt.Errorf("意图 %s 的前提事实不成立：%s", id, strings.Join(blocked, "；"))
	}
	if in.Status == model.StatusRunning || in.Status == model.StatusClaimed {
		return "", fmt.Errorf("intent %s 已在运行", id)
	}
	run, nodeID, err := s.o.PlanRun(in, continuedFrom, profileOverride, fallbackFrom)
	if err != nil {
		return "", err
	}
	if err := s.o.CheckProfileCapacity(run); err != nil {
		return "", err
	}
	// Publish a cancel handle before returning the admitted run. A pause/cancel
	// racing goroutine startup must not miss a run already in the ledger.
	s.mu.Lock()
	if s.o.Gates.Paused.Load() || s.o.Gates.Deleting.Load() || s.o.Gates.Closing.Load() {
		s.mu.Unlock()
		return "", errors.New("项目已暂停、正在删除或控制台关闭：不能再派发")
	}
	claimed, run, err := s.o.Store.StartIntentRun(id, nodeID, owner, run)
	if err != nil {
		s.mu.Unlock()
		return "", err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancels[run.ID] = cancel
	s.mu.Unlock()
	claimed.Profile = run.Profile
	go func() {
		defer cancel()
		defer func() {
			s.mu.Lock()
			delete(s.cancels, run.ID)
			delete(s.canceled, run.ID)
			delete(s.cancelReasons, run.ID)
			delete(s.cancelCauses, run.ID)
			s.mu.Unlock()
		}()
		s.o.Dispatch(ctx, claimed, run.ID, nodeID, resume, prompt, keepPrompt)
	}()
	return run.ID, nil
}

// ---- settlement ----

// Finish applies one run's terminal status. It is the one place that turns
// "turn ended" into a run row and an intent state; callers must not settle
// directly.
//
// Settlement also drops the run's collaboration credential before the status is
// published: a finished run must not keep a resolvable token.
func (s *Service) Finish(runID, intentID, status, reason string, res executor.Result, declared int) {
	if run, ok := s.o.Store.GetRun(runID); !ok || run.IntentID != intentID {
		log.Printf("finish %s: run 不存在或不属于意图 %s", runID, intentID)
		return
	}
	if s.o.RevokeRun != nil {
		s.o.RevokeRun(runID)
	}
	s.o.RemoveRunIdentity(intentID, runID)
	now := time.Now()
	cause := ""
	if status == model.RunCanceled {
		cause = s.CancelCause(runID)
		if cause == "" {
			// Every cancellation path records a cause; an unknown one is the
			// operator (the historical fallback).
			cause = model.CancelCauseOperator
		}
	}
	_, runErr := s.o.Store.UpdateRun(runID, func(r *model.Run) {
		r.Status = status
		r.EndedAt = &now
		r.ExitReason = reason
		r.CancelCause = cause
		r.Events = res.Events
		r.DeclaredFacts = declared
		r.StopReason = res.StopReason
		if s.o.Redact != nil {
			r.LastState = s.o.Redact(res.LastState)
		} else {
			r.LastState = res.LastState
		}
		r.Compacted = res.Compacted
		r.Tokens = res.Tokens
		// One usage mapper for every run that reports cost: worker, commander and
		// purpose runs (executor.UsageOf). Unknown stays unknown (Known=false),
		// not a fabricated zero.
		r.Usage = executor.UsageOf(res)
		if res.ContextTokens > r.ContextTokens {
			r.ContextTokens = res.ContextTokens
		}
		// The harness may have compacted the session during the turn. Keep its
		// post-compaction report separately so a continuation budgets against
		// what the session actually holds, not the pre-compaction peak.
		if res.FinalContextTokens > 0 {
			r.FinalContextTokens = res.FinalContextTokens
		}
		if res.ContextWindow > 0 {
			r.ContextWindow = res.ContextWindow
		}
	})
	if runErr != nil {
		log.Printf("finish %s: 保存运行结果失败：%v", runID, runErr)
		return
	}
	outcome, settleErr := s.o.Store.SettleIntentForRun(intentID, runID, status, cause)
	if settleErr != nil {
		log.Printf("finish %s: 结算意图失败：%v", runID, settleErr)
		return
	}
	if outcome.Stale {
		return
	}
	if status == model.RunFailed {
		// Retry-with-memory: put messages that arrived after the last turn
		// start back on the queue so the retry carries them.
		s.redeliverPossiblyUnread(intentID, runID)
	}
	go s.o.AfterSettle(intentID, runID, status)
	if cause != model.CancelCauseActionWait && cause != model.CancelCauseChildWait && cause != model.CancelCauseOperatorWait {
		s.o.WakeDecider("intent_" + status)
	}
	if s.o.AutoVerify != nil {
		go s.o.AutoVerify(intentID)
	}
	// A provider auth/quota failure on a local profile run may rotate its key and
	// re-dispatch once. It never changes this run's settled status and runs off
	// the finish path because rebuilding a host takes seconds.
	if s.o.AfterProviderFailure != nil {
		go s.o.AfterProviderFailure(runID, intentID, status, res)
	}
}

// redeliverPossiblyUnread is the failure-path message requeue. It is a ledger
// read plus one store call; there is no end-to-end receipt (D87), so the
// structured proxy is the run's last turn_started.
func (s *Service) redeliverPossiblyUnread(intentID, runID string) {
	run, ok := s.o.Store.GetRun(runID)
	if !ok {
		return
	}
	var since time.Time
	if run.LastTurnStartedAt != nil {
		since = *run.LastTurnStartedAt
	}
	if n, err := s.o.Store.RedeliverPossiblyUnread(participantID(intentID), runID, since); err != nil {
		log.Printf("finish %s: 重投可能未读消息失败：%v", runID, err)
	} else if n > 0 {
		log.Printf("run %s（%s）失败：%d 条已投递未确认的消息回队，等待重试注入", runID, intentID, n)
	}
}

// ---- crash recovery and watchdog reapers ----

// Recover repairs what a crash leaves behind. At startup no goroutine owns any
// run, so a run recorded as running is necessarily an orphan.
func (s *Service) Recover() {
	if err := s.o.Store.RecoverMessageDeliveries(); err != nil {
		log.Printf("恢复消息投递失败：%v", err)
		return
	}
	if err := s.o.Store.RecoverHandoffs(); err != nil {
		log.Printf("恢复交接失败：%v", err)
		return
	}
	now := time.Now()
	recovered := false
	for _, r := range s.o.Store.Runs() {
		if r.Status != model.RunRunning {
			continue
		}
		recovered = true
		_, _ = s.o.Store.UpdateRun(r.ID, func(x *model.Run) {
			x.Status = model.RunCanceled
			x.CancelCause = model.CancelCauseRestart
			x.EndedAt = &now
			x.ExitReason = "进程重启前未结束"
		})
		if r.Purpose == "commander" {
			if _, err := s.o.Store.RedeliverPossiblyUnread(collab.CommanderID, r.ID, time.Time{}); err != nil {
				log.Printf("恢复指挥官未确认输入失败：%v", err)
			}
			continue
		}
		if in, ok := s.o.Store.GetIntent(r.IntentID); ok && in.RunID == r.ID {
			if in.Status == model.StatusRunning || in.Status == model.StatusClaimed {
				if _, _, err := s.recoverInterruptedIntent(in, true); err != nil {
					log.Printf("恢复意图 %s 失败：%v", in.ID, err)
				}
			}
		}
	}
	// Waiting directions survive a restart: their condition is in the ledger.
	if recovered {
		s.restarted.Store(true)
	}
	if s.o.Pump != nil {
		go s.o.Pump()
	}
}

// ReapOrphans returns a running intent run whose dispatcher vanished to
// pending. Purpose runs are not in the registry and may legitimately outlive the
// watchdog window, so they are left to Recover.
func (s *Service) ReapOrphans() {
	s.mu.Lock()
	active := make(map[string]bool, len(s.cancels))
	for id := range s.cancels {
		active[id] = true
	}
	s.mu.Unlock()

	now := time.Now()
	for _, r := range s.o.Store.Runs() {
		if r.Purpose != "" {
			continue
		}
		// The young-run window covers the gap between AddRun and the moment
		// dispatch registers its cancel func.
		if r.Status != model.RunRunning || active[r.ID] || now.Sub(r.StartedAt) < 2*time.Minute {
			continue
		}
		_, _ = s.o.Store.UpdateRun(r.ID, func(x *model.Run) {
			x.Status = model.RunCanceled
			x.CancelCause = model.CancelCauseWatchdog
			x.EndedAt = &now
			x.ExitReason = "调度丢失，守卫回收"
		})
		if in, ok := s.o.Store.GetIntent(r.IntentID); ok && in.RunID == r.ID {
			if in.Status == model.StatusRunning || in.Status == model.StatusClaimed {
				updated, applied, err := s.recoverInterruptedIntent(in, true)
				if err != nil {
					log.Printf("回收意图 %s 失败：%v", in.ID, err)
				}
				if !applied {
					continue
				}
				if delegatedIntent(updated) && updated.Wait == nil {
					go s.o.AfterSettle(in.ID, r.ID, model.RunCanceled)
				}
				s.o.WakeDecider("watchdog")
			}
		}
	}
}

// ReapIdle cancels a run that has emitted nothing for idleTimeout. It holds the
// intent before cancelling, so "canceled -> pending" (D19) does not immediately
// restart the stuck direction.
func (s *Service) ReapIdle(idleTimeout time.Duration) {
	if idleTimeout <= 0 {
		return
	}
	now := time.Now()
	for _, r := range s.o.Store.Runs() {
		if r.Status != model.RunRunning || r.Purpose != "" {
			continue
		}
		last := r.StartedAt
		if r.LastEventAt != nil {
			last = *r.LastEventAt
		}
		idle := now.Sub(last)
		if idle < idleTimeout {
			continue
		}
		in, ok := s.o.Store.GetIntent(r.IntentID)
		if !ok || in.RunID != r.ID || (in.Status != model.StatusRunning && in.Status != model.StatusClaimed) {
			continue
		}
		if _, applied, err := s.o.Store.UpdateIntentIfCurrent(in.ID, in.Status, r.ID, func(i *model.Intent) { i.Hold = true }); err != nil || !applied {
			continue
		}
		reason := fmt.Sprintf("静默 %s（疑似卡死）：守卫取消并 hold", idleTimeout)
		if err := s.CancelRunWithReason(r.ID, reason); err == nil {
			log.Printf("watchdog: run %s idle %s, canceled", r.ID, idle.Round(time.Second))
		}
	}
}

// ReapClaims returns a claimed intent whose dispatcher vanished (AddRun or the
// goroutine failed after the claim) to pending.
func (s *Service) ReapClaims() {
	for _, in := range s.o.Store.ListIntents() {
		if in.Status != model.StatusClaimed {
			continue
		}
		if in.RunID != "" {
			if r, ok := s.o.Store.GetRun(in.RunID); ok && r.Status == model.RunRunning {
				continue
			}
		}
		updated, applied, err := s.recoverInterruptedIntent(in, false)
		if err != nil {
			log.Printf("回收认领意图 %s 失败：%v", in.ID, err)
		}
		if !applied {
			continue
		}
		log.Printf("watchdog: claimed intent %s lost its dispatcher", in.ID)
		if delegatedIntent(updated) && updated.Wait == nil {
			go s.o.AfterSettle(in.ID, in.RunID, model.RunFailed)
			continue
		}
		s.o.WakeDecider("watchdog")
	}
}

// ReapDanglingIntents repairs the other half of a crash between finish's two
// writes: a run reached a terminal state, but its intent stayed running.
func (s *Service) ReapDanglingIntents() {
	now := time.Now()
	for _, in := range s.o.Store.ListIntents() {
		if in.Status != model.StatusRunning {
			continue
		}
		run, ok := model.Run{}, false
		if in.RunID != "" {
			run, ok = s.o.Store.GetRun(in.RunID)
		}
		if ok && run.Status == model.RunRunning {
			continue
		}
		if ok && run.EndedAt != nil && now.Sub(*run.EndedAt) < time.Minute {
			continue
		}
		if !ok && now.Sub(in.UpdatedAt) < time.Minute {
			continue
		}

		status := model.StatusFailed
		updated, applied, err := s.o.Store.UpdateIntentIfCurrent(in.ID, in.Status, in.RunID, func(i *model.Intent) {
			switch run.Status {
			case model.RunDone:
				status = model.StatusDone
			case model.RunCanceled:
				status = model.StatusPending
			}
			// Derive wait/stop semantics from the current row under the lock:
			// either may have changed since the sweep inspected the direction.
			heldCancel := run.Status == model.RunCanceled && (i.Aborted || model.CancelHoldsIntent(run.CancelCause))
			if heldCancel {
				status, i.Hold, i.Wait = model.StatusFailed, true, nil
			} else if i.Wait != nil {
				status = model.StatusWaiting
			} else if delegatedIntent(*i) && status == model.StatusPending {
				// Interrupted helpers return a result to their coordinator.
				status = model.StatusFailed
			}
			i.Status = status
			i.ClaimedBy = ""
			if status == model.StatusPending || heldCancel {
				// The same rule as finish: our cancellation is not an attempt.
				uncountAttempt(i)
			} else if status != model.StatusWaiting {
				i.Hold = false
			}
		})
		if err != nil {
			log.Printf("watchdog: 对账意图 %s 失败：%v", in.ID, err)
			continue
		}
		if !applied {
			continue
		}
		log.Printf("watchdog: 意图 %s 是 running，但 run %s 已是 %s；对账为 %s", in.ID, in.RunID, run.Status, status)
		if delegatedIntent(updated) && updated.Wait == nil {
			go s.o.AfterSettle(in.ID, run.ID, run.Status)
			continue
		}
		s.o.WakeDecider("watchdog")
	}
}

// Apply recovery only to the run and state inspected by the sweep. Dispatch,
// handoff and cancellation may replace either before the store lock is taken.
func (s *Service) recoverInterruptedIntent(in model.Intent, uncount bool) (model.Intent, bool, error) {
	return s.o.Store.UpdateIntentIfCurrent(in.ID, in.Status, in.RunID, func(i *model.Intent) {
		i.ClaimedBy = ""
		if i.Wait != nil {
			i.Status = model.StatusWaiting
		} else if delegatedIntent(*i) {
			i.Status = model.StatusFailed
			i.Hold = false
		} else {
			i.Status = model.StatusPending
			if uncount {
				uncountAttempt(i)
			}
		}
	})
}

// A delegated task has a local result owner even when it was spawned without
// a parent edge. Recovery must not turn an interrupted helper into fresh work.
func delegatedIntent(in model.Intent) bool {
	_, created := collab.IntentIDFromParticipant(in.CreatedBy)
	return in.ParentID != "" || created
}
