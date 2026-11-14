package decider

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

	"github.com/kksty/tianwu/internal/board"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/retrieval"
	"github.com/kksty/tianwu/internal/state"
	"github.com/kksty/tianwu/internal/store"
)

// Runner owns the wake/debounce loop and writes every round to the decision
// log. A round is one harness commander session (D78): this type schedules it,
// gates it, and records the result; it never calls a model itself.
type Runner struct {
	Store *store.Store
	State *state.File
	// ModelName is the configured model, shown on the console's commander card.
	// The actual client lives in the harness host, not here.
	ModelName string
	// ContextWindow is the model's context budget. It turns the session's
	// reported context size into the 85% handoff line; 0 disables the pressure
	// section (the harness still compacts, the prompt just cannot say when).
	ContextWindow int
	Scope         string
	Debounce      time.Duration
	Heartbeat     time.Duration
	// RoundTimeout caps one commander round's wall clock. A round is a
	// decision: while it runs, every wake it would answer is queued behind it
	// (the loop calls Commander synchronously), so a round that never ends
	// stalls decisions no matter how healthy the rest of the campaign is.
	// 0 disables the cap. A cut round is canceled with ErrRoundTimeout, not
	// failed: its actions already count, and the next wake decides again on a
	// fresh board with the same session (D91).
	RoundTimeout time.Duration
	LogPath      string
	// DispatchReady gates rounds before spending tokens, preserving the actual
	// execution/model failure for the operator. nil disables the gate.
	DispatchReady func() error
	// Commander runs one round in the harness. It returns the round's token
	// total so the decision log records token consumption; resume asks to continue
	// the project's existing session (the caller resolves it).
	Commander func(ctx context.Context, round string, resume bool) (RoundResult, error)
	// Session tells the runner whether this round continues the harness session
	// (resume/restart). It is resolved by the caller because only the executor
	// can answer CanResume; nil means the zero RoundContext, which injects the
	// note in full (the safe side). Set before Start.
	Session func() RoundContext
	// Settled is called once the project has nothing left to attend to: every
	// goal is met and no intent is still waiting on the decider. The hub uses it
	// to pause the project automatically, so a finished engagement stops
	// spending heartbeats and the console shows the paused state instead of
	// pretending it is still working. It runs on the runner goroutine and must
	// not block.
	Settled func()

	mu              sync.Mutex
	runMu           sync.Mutex
	cancelRound     context.CancelCauseFunc
	round           int
	last            *Decision
	lastSupervision map[string]supervisionMark
	tokens          int
	stalled         int // consecutive rounds without progress
	lastFacts       int
	// lastContext / lastCompacted describe the previous commander round's
	// ending context. They are the one thing the model cannot see for itself,
	// so every round hands them back in the prompt (D80).
	lastContext   int
	lastCompacted bool
	// observedWindow is the window the harness resolved for the model (usage
	// detail). It wins over the profile's context_window: the harness knows the
	// model's real window, the control plane would only be guessing (D80).
	observedWindow int
	// handoffDone is "the note was rewritten in this pressure cycle". Without
	// it every round above 85% would be a handoff round and nothing would ever
	// be dispatched.
	handoffDone bool
	// handoffRound is "the previous round was the handoff round". It is a
	// one-round flag, not a cycle flag: the full note is re-injected exactly
	// once after the handoff, and the ordinary pointer contract takes over from
	// the round after that. Overloading handoffDone (which stays true until
	// compaction) re-pasted the note on every pressure round and made the
	// prompt claim "the previous round was the handoff" long after it was.
	handoffRound bool
	// now is the clock retry scheduling reads. nil means time.Now; tests
	// replace it to walk the heartbeat/backoff timeline without sleeping. It is
	// not a business clock and must not be used for decision timestamps.
	now     func() time.Time
	wake    chan string
	started bool
	// activity is what the console's live card shows: the phase of the round
	// in flight, not a guess reconstructed from the decision log.
	activity     Activity
	activityWake string
	// paused is the operator's kill switch: wakes are dropped and the active
	// round is canceled until an explicit Resume. selfPaused records that the
	// stop came from the commander's own round (StandbyFor): that round is not
	// canceled, so its answer and delivery checkpoint still belong to it.
	paused      bool
	selfPaused  bool
	pauseReason string
	startedAt   time.Time
	retryAfter  time.Time // transport recovery backoff; never a strategy/fee gate
}

// ErrRoundTimeout is the cancellation cause of a commander round cut by
// Runner.RoundTimeout. It is a scheduler decision, not the model's verdict and
// not a project stop: the control plane records the round as canceled, keeps
// the session, and lets the next wake decide again on the newest board (D91).
var ErrRoundTimeout = errors.New("本轮超过墙钟上限")

var ErrPaused = errors.New("操作员已暂停指挥官")

// ErrCommanderStalled is a commander round stopped by the model_stall liveness
// detector: the model kept sending empty replies and requesting the LLM again
// instead of calling a tool or ending the turn. It is neither a project stop
// nor a scheduler cut — the session survives — but the round made no progress,
// so it must reach the decider as the round's error instead of a normal
// completion. Otherwise Decision.Error stays empty and the console's commander
// card hides why the round ended (D107). Changing the tier is an operator
// action: the commander's long session cannot move hosts mid-flight (docs/17
// §3.5.1), so no automatic switch is faked here.
var ErrCommanderStalled = errors.New("指挥官模型连续空回复（model_stall）：本轮被存活检测取消")

func NewRunner(st *store.Store, mem *state.File, logPath string) *Runner {
	return &Runner{
		Store:        st,
		State:        mem,
		LogPath:      logPath,
		Debounce:     3 * time.Second,
		Heartbeat:    5 * time.Minute,
		RoundTimeout: 15 * time.Minute,
		wake:         make(chan string, 1),
	}
}

// SetModelName updates the console-visible model when the settings page saves.
func (r *Runner) SetModelName(name string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.ModelName = name
	r.mu.Unlock()
}

// SetContextWindow follows the settings page: the handoff line moves with the
// model profile, so a window change must not wait for a process restart. The
// harness-reported window is dropped with it: the next round reports the window
// of the new profile.
func (r *Runner) SetContextWindow(n int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.ContextWindow = n
	r.observedWindow = 0
	r.mu.Unlock()
}

// SetRoundTimeout follows the settings page's per-project override: the cap a
// commander round runs under changes from the next round on, without a restart.
// 0 disables the cap (D91).
func (r *Runner) SetRoundTimeout(d time.Duration) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.RoundTimeout = d
	r.mu.Unlock()
}

// Status is what the console shows about the decider itself.
type Status struct {
	Enabled            bool                    `json:"enabled"`
	Model              string                  `json:"model,omitempty"`
	Rounds             int                     `json:"rounds"`
	Tokens             int                     `json:"tokens_total"`
	Stalled            int                     `json:"stalled"`
	LastRoundAt        *time.Time              `json:"last_round_at,omitempty"`
	LastWake           string                  `json:"last_wake,omitempty"`
	LastDigest         string                  `json:"last_digest,omitempty"`
	LastError          string                  `json:"last_error,omitempty"`
	LastFallback       string                  `json:"last_fallback,omitempty"`
	LastActions        []Action                `json:"last_actions,omitempty"`
	LastNotes          []string                `json:"last_notes,omitempty"`
	LastAttention      []store.AttentionReason `json:"last_attention,omitempty"`
	PendingAttention   []store.AttentionReason `json:"pending_attention,omitempty"`
	AttentionDelivered bool                    `json:"attention_delivered,omitempty"`
	// Activity is the live phase of the in-flight round. It is present even
	// when the decider is idle: the card then says what it is waiting for.
	Activity Activity `json:"activity"`
	// HeartbeatSeconds is the idle cadence, so the card can say how long it
	// will stay quiet rather than showing a frozen 0.
	HeartbeatSeconds int `json:"heartbeat_seconds,omitempty"`
	// Paused is the operator's kill switch; UptimeSeconds answers "how long has
	// this been running unattended".
	Paused        bool   `json:"paused,omitempty"`
	PausedReason  string `json:"paused_reason,omitempty"`
	UptimeSeconds int    `json:"uptime_seconds,omitempty"`
	// RunTokensHour is what runs spent in the last hour (intent runs and
	// commander rounds alike). Recorded for the operator, never enforced.
	RunTokensHour int `json:"run_tokens_last_hour,omitempty"`
	// SessionRounds / SessionDropped belonged to the local decision cache (D56,
	// retired by D78). Kept because the console's bundle reads them; always 0.
	SessionRounds  int `json:"session_rounds,omitempty"`
	SessionDropped int `json:"session_dropped,omitempty"`
}

// Activity is the "what is it doing right now" record behind the graph's live
// card: phase, current round, the wake that started it and one short detail.
type Activity struct {
	Phase  string     `json:"phase"`
	Round  int        `json:"round,omitempty"`
	Wake   string     `json:"wake,omitempty"`
	Detail string     `json:"detail,omitempty"`
	Tool   string     `json:"tool,omitempty"`
	Since  *time.Time `json:"since,omitempty"`
}

// setActivity records a phase change. Re-setting the same phase and detail is a
// no-op, so the bar does not restart on every heartbeat.
func (r *Runner) setActivity(phase, detail string) {
	if r == nil {
		return
	}
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.paused && phase != "paused" {
		return
	}
	if r.activity.Phase == phase && r.activity.Detail == detail {
		return
	}
	r.activity = Activity{Phase: phase, Round: r.round, Wake: r.activityWake, Detail: detail, Since: &now}
}

func (r *Runner) Status() Status {
	if r == nil {
		return Status{Enabled: false}
	}
	pending := store.AttentionBatch{}
	if r.Store != nil {
		pending = r.Store.CommanderAttention()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	st := Status{
		Enabled:       r.ModelName != "" && r.Commander != nil,
		Model:         r.ModelName,
		Rounds:        r.round,
		Tokens:        r.tokens,
		Stalled:       r.stalled,
		Paused:        r.paused,
		PausedReason:  r.pauseReason,
		Activity:      r.activity,
		RunTokensHour: hourlyRunTokens(r.Store, time.Now().Add(-time.Hour)),
	}
	if r.Heartbeat > 0 {
		st.HeartbeatSeconds = int(r.Heartbeat / time.Second)
	}
	if !r.startedAt.IsZero() {
		st.UptimeSeconds = int(time.Since(r.startedAt).Seconds())
	}
	if r.last != nil {
		at := r.last.At
		st.LastRoundAt = &at
		st.LastWake = r.last.Wake
		st.LastDigest = r.last.Digest
		st.LastError = r.last.Error
		st.LastFallback = r.last.Fallback
		st.LastActions = r.last.Actions
		st.LastNotes = r.last.Notes
		st.LastAttention = append([]store.AttentionReason(nil), r.last.Attention...)
		st.AttentionDelivered = r.last.AttentionDelivered
	}
	st.PendingAttention = pending.Reasons
	return st
}

// Pause stops commander rounds without stopping the process. A long unattended
// run needs a kill switch that keeps the ledger, the watchdog and the console
// alive.
func (r *Runner) Pause() {
	r.PauseFor("操作员暂停了指挥官：等你点「继续」")
}

// PauseFor is Pause with a reason the console can show.
func (r *Runner) PauseFor(reason string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.paused = true
	r.selfPaused = false
	r.pauseReason = reason
	if r.cancelRound != nil {
		r.cancelRound(ErrPaused)
	}
	// Queued work is not permission to spend after a later resume. Resume's
	// explicit wake will read the then-current board instead.
drain:
	for {
		select {
		case <-r.wake:
		default:
			break drain
		}
	}
	r.mu.Unlock()
	if reason != "" {
		r.setActivity("paused", reason)
	}
}

// StandbyFor is the commander's own stop request: the wake loop pauses, but the
// round that asked for it is NOT canceled. PauseFor is the operator's kill
// switch — it cuts an in-flight round immediately because a human wants the
// spending to end now. A commander round that judges "this engagement should
// stop" must instead be allowed to finish its answer and its bookkeeping; the
// project pause it requested is already visible to the console while that
// happens (the API sets the dispatch gate before calling this).
//
// Queued wakes are dropped for the same reason PauseFor drops them: after a
// pause, only an explicit operator resume may buy the next round.
func (r *Runner) StandbyFor(reason string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.paused = true
	r.selfPaused = true
	r.pauseReason = reason
drain:
	for {
		select {
		case <-r.wake:
		default:
			break drain
		}
	}
	r.mu.Unlock()
	if reason != "" {
		r.setActivity("paused", reason)
	}
}

// Resume re-arms the loop; it is explicit, never automatic.
func (r *Runner) Resume() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.paused = false
	r.selfPaused = false
	r.retryAfter = time.Time{}
	r.pauseReason = ""
	r.mu.Unlock()
}

// Paused reports the kill switch.
func (r *Runner) Paused() bool {
	if r == nil {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.paused
}

// Wake asks for a round after the debounce window. Calls coalesce.
func (r *Runner) Wake(reason string) {
	if r == nil {
		return
	}
	if reason == "" {
		reason = "change"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.paused {
		return
	}
	select {
	case r.wake <- reason:
		return
	default:
	}
	if !isForced(reason) {
		return
	}
	// A forced wake (the operator asked for this round) must not be dropped just
	// because a normal change is already queued. Replace that pending reason;
	// if the queued one is forced too, keep it — either way the next round is
	// forced.
	select {
	case old := <-r.wake:
		if isForced(old) {
			select {
			case r.wake <- old:
			default:
			}
			return
		}
	default:
	}
	select {
	case r.wake <- reason:
	default:
	}
}

func (r *Runner) Start(ctx context.Context) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return
	}
	r.started = true
	r.startedAt = time.Now()
	r.mu.Unlock()
	r.seedContext()
	r.setActivity("idle", "等待第一次唤醒")
	go r.loop(ctx)
}

// windowLocked is the window pressure is measured against: the harness's
// resolved value wins over the profile setting. Callers hold r.mu.
func (r *Runner) windowLocked() int {
	if r.observedWindow > 0 {
		return r.observedWindow
	}
	return r.ContextWindow
}

// clock is time.Now unless a test walks the retry timeline.
func (r *Runner) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

// attention is the durable global demand as of the runner's clock. The retry
// schedule is the only time-dependent part of it.
func (r *Runner) attention() store.AttentionBatch {
	if r.Store == nil {
		return store.AttentionBatch{Reasons: []store.AttentionReason{}}
	}
	return r.Store.CommanderAttentionAt(r.clock())
}

// seedContext recovers the session's pressure across a process restart: the AOP
// session is resumed, so its harness context is not empty just because this
// Runner is new. The latest commander run's peak is the best estimate the store
// has; a stale pre-compaction peak can only buy one extra, harmless handoff
// round.
func (r *Runner) seedContext() {
	if r.Store == nil {
		return
	}
	var latest model.Run
	found := false
	for _, run := range r.Store.Runs() {
		if run.Purpose != "commander" {
			continue
		}
		if !found || run.StartedAt.After(latest.StartedAt) {
			latest, found = run, true
		}
	}
	if !found {
		return
	}
	r.mu.Lock()
	if r.lastContext == 0 {
		r.lastContext = latest.ContextTokens
	}
	if latest.ContextWindow > 0 {
		r.observedWindow = latest.ContextWindow
	}
	r.mu.Unlock()
}

func (r *Runner) loop(ctx context.Context) {
	hb := r.Heartbeat
	if hb <= 0 {
		hb = 5 * time.Minute
	}
	tick := time.NewTicker(hb)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if r.Paused() {
				continue
			}
			if r.needsAttention() && r.heartbeatWorthwhile() {
				r.Run(ctx, "heartbeat", false)
			} else {
				r.setActivity("idle", "心跳：没有需要处理的变化")
			}
			r.settleIfDone()
		case reason := <-r.wake:
			r.setActivity("debounce", "去抖：等这一阵变化安静下来")
			reason, ok := r.debounce(ctx, reason)
			if !ok {
				return
			}
			r.Run(ctx, reason, isForced(reason))
			r.settleIfDone()
		}
	}
}

func isForced(reason string) bool {
	return reason == "operator" || reason == "manual" || reason == "hint"
}

// supervisionIdle is how long a running intent may be silent before a
// heartbeat is worth a round even when the board digest is unchanged. A quiet
// run is not the same as a quiet board.
const supervisionIdle = 5 * time.Minute

type supervisionMark struct {
	failures    int
	silentSince time.Time
}

func supervisionMarks(b board.View) map[string]supervisionMark {
	marks := make(map[string]supervisionMark, len(b.Supervision))
	for _, p := range b.Supervision {
		m := supervisionMark{failures: p.ToolFailures}
		if time.Duration(p.IdleSeconds)*time.Second >= supervisionIdle {
			m.silentSince = p.StartedAt
			if p.LastEventAt != nil {
				m.silentSince = *p.LastEventAt
			}
		}
		marks[p.Run] = m
	}
	return marks
}

// A failed tool can keep emitting events forever. Supervision notices new
// structured failures as well as new silence episodes, without repeatedly
// waking for an unchanged idle run or for the commander's own bookkeeping.
func (r *Runner) heartbeatWorthwhile() bool {
	a := r.attention()
	if a.More || len(a.Reasons) > 0 {
		return true
	}
	b := board.Build(r.Store, 20)
	return r.supervisionWorthwhile(b)
}

func (r *Runner) supervisionWorthwhile(b board.View) bool {
	// A live coordinator handles its own descendants. Root supervision remains
	// available for silence/failures the execution mechanism cannot resolve.
	coordinated := map[string]bool{}
	for _, p := range b.Supervision {
		if run, ok := r.Store.GetRun(p.Run); ok {
			coordinated[p.Run] = r.Store.IntentCoordinator(run.IntentID) != ""
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, m := range supervisionMarks(b) {
		if coordinated[id] {
			continue
		}
		seen := r.lastSupervision[id]
		if m.failures > seen.failures || (!m.silentSince.IsZero() && !m.silentSince.Equal(seen.silentSince)) {
			return true
		}
	}
	return false
}

// debounce coalesces bursts, with a maximum of five quiet windows (15s by
// default). Progressive facts can arrive indefinitely; they must not starve
// the commander by resetting the quiet timer forever.
func (r *Runner) debounce(ctx context.Context, reason string) (string, bool) {
	d := r.Debounce
	if d <= 0 {
		d = 3 * time.Second
	}
	t := time.NewTimer(d)
	defer t.Stop()
	deadline := time.NewTimer(5 * d)
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return reason, false
		case <-t.C:
			return reason, true
		case <-deadline.C:
			return reason, true
		case next := <-r.wake:
			if isForced(next) || !isForced(reason) {
				// Keep the strongest reason: forced beats normal, and a later
				// normal wake must not downgrade an already-forced round.
				reason = next
			}
			if !t.Stop() {
				select {
				case <-t.C:
				default:
				}
			}
			t.Reset(d)
		}
	}
}

func (r *Runner) needsAttention() bool {
	for _, g := range r.Store.Goals() {
		if !g.Met || g.ReceiptStale {
			return true
		}
	}
	for _, in := range r.Store.ListIntents() {
		switch in.Status {
		case model.StatusPending, model.StatusFailed, model.StatusReviewFailed:
			if !in.Hold {
				return true
			}
		case model.StatusRunning, model.StatusClaimed, model.StatusWaiting:
			return true
		case model.StatusReviewing:
			// The fixed review phase owns its own wake when it finishes. Waking
			// the commander merely because a review is running makes it poll a
			// direction it must not act on yet.
		}
	}
	// A message addressed to the commander wakes the loop directly (D88), so an
	// idle project with nothing open has nothing to attend to.
	return false
}

// workFinished is "the engagement the operator chartered is complete": at
// least one goal exists and all goals are met, with nothing pending or running.
// A project with no goals is deliberately not finished — it may be a fresh
// engagement the operator is still setting up, and auto-pausing it would fight
// the next click.
func (r *Runner) workFinished() bool {
	if r.Store == nil || r.needsAttention() || r.hasReviewing() {
		return false
	}
	return len(r.Store.Goals()) > 0
}

// hasReviewing keeps completion/pause from firing while a fixed review phase is
// still running. It does not wake the commander; the review completion does.
func (r *Runner) hasReviewing() bool {
	for _, in := range r.Store.ListIntents() {
		if in.Status == model.StatusReviewing {
			return true
		}
	}
	return false
}

// settleIfDone hands a finished project to the control plane once, so it can
// pause instead of idling. The runner cannot pause the project itself: dispatch
// gating, run cancellation and the manifest's paused flag all live in the API.
func (r *Runner) settleIfDone() {
	if r.Settled == nil || r.Paused() || !r.workFinished() {
		return
	}
	r.Settled()
}

// Run executes one decision round. It returns nil when the board digest says
// nothing changed (the cheapest possible decision).
func (r *Runner) Run(ctx context.Context, wake string, force bool) *Decision {
	r.runMu.Lock()
	defer r.runMu.Unlock()
	// A paused decider does not spend: the operator stopped
	// it, and only an explicit Resume starts it again.
	if r.Paused() {
		r.mu.Lock()
		reason := r.pauseReason
		r.mu.Unlock()
		if reason == "" {
			reason = "操作员暂停了指挥官：等你点「继续」"
		}
		r.setActivity("paused", reason)
		return nil
	}
	if ctx.Err() != nil {
		return nil
	}
	ctx, cancelRound := context.WithCancelCause(ctx)
	r.mu.Lock()
	if r.paused {
		r.mu.Unlock()
		cancelRound(ErrPaused)
		return nil
	}
	r.cancelRound = cancelRound
	r.mu.Unlock()
	defer func() {
		cancelRound(nil)
		r.mu.Lock()
		r.cancelRound = nil
		r.mu.Unlock()
	}()
	force = force || isForced(wake)
	if !force && !r.needsAttention() {
		r.setActivity("idle", "目标与意图已收口，自动休眠；新增工作或人工唤醒后再决策")
		return nil
	}
	attention := r.attention()
	// Routing a local batch does not spend a model turn. Its durable checkpoint
	// is independent of full-context delivery/ACK, so global history stays readable.
	for i := 0; len(attention.Reasons) == 0 && i < 4; i++ {
		if err := r.Store.AdvanceCommanderAttention(attention.Through); err != nil {
			r.setActivity("blocked", "保存事件路由进度失败："+err.Error())
			return nil
		}
		if !attention.More {
			break
		}
		attention = r.attention()
	}
	if !force && len(attention.Reasons) == 0 && !r.supervisionWorthwhile(board.Build(r.Store, 20)) {
		r.setActivity("idle", "局部任务自行推进，暂无需要全局判断的新事件")
		return nil
	}
	if !force && wake != "heartbeat" && len(attention.Reasons) == 1 && attention.Reasons[0].Code == "context_unacknowledged" {
		r.setActivity("idle", "局部变化已路由；未确认的全局上下文留待心跳或显式重试")
		return nil
	}
	if r.Commander == nil {
		r.setActivity("blocked", "没有可用的模型：在「设置」里配置")
		return nil
	}
	r.mu.Lock()
	if r.paused || ctx.Err() != nil {
		r.mu.Unlock()
		return nil
	}
	modelName := r.ModelName
	roundTimeout := r.RoundTimeout
	retryAfter := r.retryAfter
	r.mu.Unlock()
	if !force && r.clock().Before(retryAfter) {
		r.setActivity("blocked", "上次调用未完成，事件已保留；等待恢复重试或操作员唤醒")
		return nil
	}
	if strings.TrimSpace(modelName) == "" {
		r.setActivity("blocked", "没有可用的模型：在「设置」里配置")
		return nil
	}
	b := board.Build(r.Store, 20)
	digest := board.Digest(b)

	if r.DispatchReady != nil {
		if err := r.DispatchReady(); err != nil {
			r.setActivity("blocked", err.Error())
			return nil
		}
	}

	r.mu.Lock()
	if r.paused || ctx.Err() != nil {
		r.mu.Unlock()
		return nil
	}
	r.round++
	r.lastSupervision = supervisionMarks(b)
	r.activityWake = wake
	d := &Decision{At: time.Now(), Round: r.round, Wake: wake, Digest: digest,
		Attention: attention.Reasons, AttentionThrough: attention.Through}
	if len(d.Attention) == 0 {
		code, detail := "supervision", "需要检查未被局部承接的运行状态"
		if force {
			code, detail = "explicit_wake", "操作员明确请求一次全局判断"
		}
		d.Attention = []store.AttentionReason{{Code: code, Kind: "control", Detail: detail}}
	}
	r.mu.Unlock()
	r.setActivity("board", "读事实意图图，整理本轮输入")

	stateText := ""
	if r.State != nil {
		stateText, _ = r.State.Load()
	}
	// This round's memory situation. The compacted/handoff halves are the
	// runner's own last-round record; resume/restart come from the caller
	// (which is the only side that can ask the executor). A nil Session is the
	// safe zero value: it injects the note in full (D89).
	rc := RoundContext{}
	if r.Session != nil {
		rc = r.Session()
	}
	if r.State != nil {
		rc.StatePath = r.State.Path()
	}
	r.mu.Lock()
	ctxStatus := ContextStatus{
		Used:        r.lastContext,
		Window:      r.windowLocked(),
		Compacted:   r.lastCompacted,
		HandoffDone: r.handoffDone,
	}
	rc.Compacted = r.lastCompacted
	// Only the round immediately after the handoff gets the whole note back.
	// The cycle flag stays true until compaction; feeding it here would paste
	// the note into every later pressure round (and call each of them "the
	// round after the handoff").
	rc.Handoff = r.handoffRound
	r.mu.Unlock()
	r.setActivity("commander", "指挥官会话运行中")
	start := time.Now()
	// The round is the unit the wake loop serves: bound it so a session that
	// waits inside the round (for executor results that can only arrive in a
	// later round) cannot hold the loop indefinitely. The cause is structured so
	// the control plane can record "cut by policy" instead of a model failure.
	roundCtx := ctx
	if roundTimeout > 0 {
		var cancel context.CancelFunc
		roundCtx, cancel = context.WithTimeoutCause(ctx, roundTimeout,
			fmt.Errorf("%w（%s）", ErrRoundTimeout, roundTimeout))
		defer cancel()
	}
	working, contextErr := retrieval.WorkingContext(r.Store, "commander")
	if contextErr != nil {
		d.Error = "读取工作上下文：" + contextErr.Error()
		r.finish(d)
		return d
	}
	if err := r.Store.BindCommanderDelivery(working.DeliveryID, attention); err != nil {
		d.Error = "绑定事件交付失败：" + err.Error()
		r.finish(d)
		return d
	}
	// Record the presentation before the model is called: the retry schedule
	// must start even when the round crashes or the model never ACKs. After
	// DeliveryPointerAfter showings the prompt degrades to the delivery id and
	// a summary, so an unacknowledging model cannot make every heartbeat carry
	// the same full page again.
	shown, showErr := r.Store.NoteDeliveryShown("commander", working.DeliveryID, r.clock())
	if showErr != nil {
		d.Error = "保存上下文交付尝试失败：" + showErr.Error()
		r.finish(d)
		return d
	}
	attentionJSON, _ := json.Marshal(d.Attention)
	prompt := "## 本轮需要全局判断的原因\n" + string(attentionJSON) + "\n局部任务由其承接者继续推进；以下事件标识可跨重试保持稳定，不代表新的执行授权。调用中断后先核对已登记动作与结果，未知效果不能盲目重放。\n" + CommanderRoundPrompt(b, stateText, r.Scope, ctxStatus, rc) + contextDeliverySection(working, shown)
	roundCtx = context.WithValue(roundCtx, commanderDeliveryKey{}, working.DeliveryID)
	res, err := r.Commander(roundCtx, prompt, true)
	timedOut := errors.Is(context.Cause(roundCtx), ErrRoundTimeout)
	d.MS = time.Since(start).Milliseconds()
	d.Tokens = res.Tokens
	d.Actions = res.Actions
	d.Model = modelName
	switch {
	case errors.Is(context.Cause(roundCtx), ErrPaused):
		d.Notes = append(d.Notes, "操作员暂停：本轮已取消，不再自动调用指挥官")
	case timedOut:
		// Not a model failure: the round did its work, it just did not end.
		// The session survives the cancel, so the next wake continues it.
		d.Notes = append(d.Notes, fmt.Sprintf("本轮超过墙钟上限 %s，已截断：下一次唤醒重新决策（会话保留）", roundTimeout))
	case err != nil:
		d.Error = err.Error()
	default:
		d.Notes = append(d.Notes, "由 harness 指挥官会话执行（D78）")
	}
	r.mu.Lock()
	// A standby pause is requested by this very round, so the round DID finish
	// its input and its delivery checkpoint is still valid; an operator pause
	// cancels the round, so its checkpoint is not. The run-context check already
	// rejects every canceled round; the selfPaused exception keeps the standby
	// round from being replayed as unprocessed after the operator resumes.
	roundFinishedInput := err == nil && !timedOut && roundCtx.Err() == nil && (!r.paused || r.selfPaused)
	if roundFinishedInput {
		if checkpointErr := r.Store.CompleteCommanderAttention(attention); checkpointErr != nil {
			d.Error = "模型调用已结束，但保存事件交付进度失败：" + checkpointErr.Error()
		} else {
			d.AttentionDelivered = true
		}
	}
	if !d.AttentionDelivered {
		r.retryAfter = r.clock().Add(30 * time.Second)
	} else {
		r.retryAfter = time.Time{}
	}
	r.mu.Unlock()
	r.mu.Lock()
	if res.ContextTokens > 0 {
		r.lastContext = res.ContextTokens
	}
	if res.ContextWindow > 0 {
		// The harness resolved the model's real window; stop guessing from the
		// profile default (D80).
		r.observedWindow = res.ContextWindow
	}
	r.lastCompacted = res.Compacted
	// thisHandoff is whether the round that just ended was the 85% handoff.
	// handoffDone is the pressure-cycle flag ("do not hand off again until the
	// context resets"); handoffRound is the one-round flag the next prompt reads
	// ("your note was just rewritten, here it is in full once"). A standby round
	// counts as finished too: it was not cut, only the next wake is forbidden.
	thisHandoff := ctxStatus.Handoff() && err == nil && roundCtx.Err() == nil && (!r.paused || r.selfPaused)
	switch {
	case res.Compacted:
		// Compaction ends the pressure cycle: the context was reset, and the
		// next climb to 85% is a new handoff.
		r.handoffDone = false
	case thisHandoff:
		r.handoffDone = true
	}
	if window := r.windowLocked(); window > 0 {
		if pct := (ContextStatus{Used: r.lastContext, Window: window}).Percent(); pct > 0 && pct < HandoffWarnPercent {
			// A larger window or a much smaller session can leave the warning
			// band without a compaction event; the next climb is a new cycle.
			r.handoffDone = false
		}
	}
	r.handoffRound = thisHandoff
	r.mu.Unlock()
	if ctxStatus.Handoff() {
		// A handoff round deliberately dispatches nothing: counting it as "no
		// progress" would pollute the no-new-facts observation.
		note := fmt.Sprintf("上下文 %d%%：本轮是交接轮，重写 state.md 后收口（D80）", ctxStatus.Percent())
		if !hasAction(res.Actions, "state") {
			note += "；但没有写笔记"
		}
		d.Notes = append(d.Notes, note)
	} else {
		r.noteProgress(b.Facts.Total)
	}
	r.finish(d)
	return d
}

type commanderDeliveryKey struct{}

// CommanderDeliveryID identifies the context page prepared by the runner.
// The API binds it to the actual run before starting the executor, without
// extracting bookkeeping from prompt text.
func CommanderDeliveryID(ctx context.Context) string {
	id, _ := ctx.Value(commanderDeliveryKey{}).(string)
	return id
}

// contextDeliverySection is the "work context" half of the round prompt. A
// pending delivery is replayed verbatim until the model acknowledges it, so the
// acknowledgement requirement is restated in both forms. After
// DeliveryPointerAfter full showings the repeated page degrades to its identity
// and a change summary: the model reads the full context through
// GET /api/context and still has to ACK. Only the payload degrades, never the
// acknowledgement.
func contextDeliverySection(ctx retrieval.Context, d store.Delivery) string {
	if d.Attempts <= store.DeliveryPointerAfter {
		raw, _ := json.Marshal(ctx)
		return "\n## 可续读的工作上下文\n" + string(raw) +
			"\n这是本轮交付，不代表已经处理。考虑完这些变化与历史目录，或把待处理引用明确保存在笔记后，使用 context/ack 确认 delivery_id；未确认交付按退避重投，多次未确认后只展示摘要，原交付仍可查询。需要更多前提、历史与正文时可以继续查询检索接口。\n"
	}
	summary := struct {
		DeliveryID  string `json:"delivery_id"`
		Attempts    int    `json:"attempts"`
		Changes     int    `json:"changes"`
		SurveyFacts int    `json:"survey_facts"`
		Read        string `json:"read"`
		Acknowledge string `json:"acknowledge"`
		Note        string `json:"note"`
	}{
		DeliveryID:  ctx.DeliveryID,
		Attempts:    d.Attempts,
		Changes:     len(ctx.Changes.Changes),
		SurveyFacts: len(ctx.Survey.Facts),
		Read:        "GET /api/context",
		Acknowledge: "POST /api/context/ack",
		Note:        "同一交付已多次展示且未确认：本轮只给摘要，不重复全文；处理后仍要确认这个 delivery_id。",
	}
	raw, _ := json.Marshal(summary)
	return fmt.Sprintf("\n## 可续读的工作上下文（同一交付已展示 %d 次，本轮只给摘要）\n%s"+
		"\n这是本轮交付（摘要），不代表已经处理。需要全文时用 GET /api/context；考虑完这些变化与历史目录，或把待处理引用明确保存在笔记后，使用 context/ack 确认 delivery_id。没有确认的交付会按退避继续重投。\n",
		d.Attempts, string(raw))
}

// hasAction reports whether the round performed one kind of ledger mutation.
// The API records the mutations, so this is what actually happened, not what
// the model said.
func hasAction(actions []Action, kind string) bool {
	for _, a := range actions {
		if a.Kind == kind {
			return true
		}
	}
	return false
}

// noteProgress observes fact-count changes without judging attack progress.
func (r *Runner) noteProgress(facts int) {
	r.mu.Lock()
	progressed := facts != r.lastFacts
	if progressed {
		r.stalled = 0
	} else {
		r.stalled++
	}
	r.lastFacts = facts
	r.mu.Unlock()
}

func (r *Runner) finish(d *Decision) {
	r.mu.Lock()
	r.last = d
	r.tokens += d.Tokens
	r.mu.Unlock()
	r.appendLog(d)
	if r.Paused() {
		r.mu.Lock()
		reason := r.pauseReason
		r.mu.Unlock()
		if reason == "" {
			reason = "指挥官已暂停：等你点「继续」"
		}
		r.setActivity("paused", reason)
		return
	}
	r.setActivity("idle", idleDetail(d))
}

// idleDetail is the one line the graph's card keeps showing between rounds:
// what the last round did, or why it did nothing.
func idleDetail(d *Decision) string {
	switch {
	case d == nil:
		return "等待唤醒"
	case d.Error != "":
		return "上一轮出错：" + clipLine(d.Error)
	case len(d.Actions) == 0:
		if len(d.Notes) > 0 {
			return "上一轮没有动作：" + clipLine(d.Notes[0])
		}
		return "上一轮没有动作"
	}
	parts := make([]string, 0, 4)
	for i, a := range d.Actions {
		if i == 3 {
			parts = append(parts, "…")
			break
		}
		if a.Text != "" {
			parts = append(parts, a.Kind+" "+a.Text)
		} else {
			parts = append(parts, a.Kind)
		}
	}
	return "上一轮：" + clipLine(strings.Join(parts, " · "))
}

// clipLine keeps an activity detail to one short line: the card is a glance,
// not a second decision log.
func clipLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	r := []rune(s)
	if len(r) > 80 {
		return string(r[:80]) + "…"
	}
	return s
}

func (r *Runner) appendLog(d *Decision) {
	if r.LogPath == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(r.LogPath), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(r.LogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	b, err := json.Marshal(d)
	if err != nil {
		return
	}
	_, _ = f.Write(append(b, '\n'))
}

// ReadLog returns the last N decisions, oldest first.
func ReadLog(path string, limit int) ([]Decision, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []Decision{}, nil
		}
		return nil, err
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if limit > 0 && len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	out := make([]Decision, 0, len(lines))
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		var d Decision
		if json.Unmarshal([]byte(ln), &d) == nil {
			out = append(out, d)
		}
	}
	return out, nil
}

// hourlyRunTokens is what every run (intent runs and commander rounds) spent in
// the window. It is observation for the console, never a budget.
func hourlyRunTokens(st *store.Store, since time.Time) int {
	total := 0
	for _, run := range st.Runs() {
		if run.StartedAt.After(since) {
			total += run.Tokens
		}
	}
	return total
}
