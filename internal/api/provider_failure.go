package api

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/settings"
)

// This file is the whole credential-failure layer of the watchdog. It answers
// exactly one question — "did this run die because the provider rejected or ran
// out of this profile's key?" — and its only actions are rotate + restart +
// one retry. It never decides whether a task succeeded, and it never changes a
// fact, an intent or a prompt (docs/17 §3.5/§3.6; watchdog-liveness §6.2).
//
// Boundary with agent.md §1:
//
//   - The input is the provider error *contract*: the harness's own
//     `API error (NNN): …` text and vendor error codes, taken from Result.Output
//     and from the run journal's kind=error events. It is not model output, not
//     a fact title and not tool output: those are things to think about, not
//     signals for a credential action.
//   - The outcome does not touch run status: finish() already settled it from
//     runErr/ExitCode/IsError. Rotation is an operations action.
//   - Upstream has no structured status-code field in the AOP stream yet
//     (harness provider.APIError loses its Code on the way through
//     `aop_emit.go`), so this one function parses the text contract. If the fork
//     adds a status/code field, this function is the only place that changes.
//   - If asked "do not parse text", the fix is an upstream protocol field, not
//     a second parser here.

// providerFailure is a credential-layer class. Only auth and quota lead to a
// rotation; rate limiting means the key is fine (sapper's watchdog makes the
// same distinction) and transient covers everything else retryable.
type providerFailure int

const (
	providerFailureNone providerFailure = iota
	// providerFailureAuth: the provider rejected the credential (401/403,
	// invalid key, unauthorized).
	providerFailureAuth
	// providerFailureQuota: the credential is valid but out of quota/balance.
	providerFailureQuota
	// providerFailureRate: throttled (429, rate limit, overloaded). The key is
	// good; rotating would burn a valid credential for nothing.
	providerFailureRate
	// providerFailureTransient: anything else that failed the turn (network,
	// 5xx, harness error). Retrying may help; rotating would not.
	providerFailureTransient
)

func (f providerFailure) String() string {
	switch f {
	case providerFailureAuth:
		return "auth"
	case providerFailureQuota:
		return "quota"
	case providerFailureRate:
		return "rate"
	case providerFailureTransient:
		return "transient"
	default:
		return "none"
	}
}

// rotatable reports whether this class justifies a credential rotation.
func (f providerFailure) rotatable() bool {
	return f == providerFailureAuth || f == providerFailureQuota
}

// CredentialStates is the shared owner of per-profile credential generations and
// rotation state. In production it is the settings store (wired by the hub),
// because the key pool and its rotation cursor are instance-wide: a project has
// its own api.Server, but "which generation is current / is a rotation in flight
// / is the profile cooling down" must have one answer for all projects.
//
// The owner deliberately deals in opaque generations and counts, never in key
// material. A nil owner means a legacy flat/single-model instance without a
// profile catalogue; that path keeps the old per-Server cooldown.
type CredentialStates interface {
	// CredentialGeneration is the profile's current generation. Dispatch freezes
	// it on the run row.
	CredentialGeneration(profile string) uint64
	// BeginCredentialFailure records one failed run's generation in the shared
	// failure episode and returns the owner's decision. It must be safe to call
	// concurrently from different projects and idempotent for a generation that
	// is already handled.
	BeginCredentialFailure(profile string, runGeneration uint64) (settings.CredentialFailure, error)
	// CompleteCredentialRotation reports that this caller's single-flight
	// rotation claim rotated a key; it advances the generation and opens the
	// cooling window.
	CompleteCredentialRotation(profile string, claim uint64) (uint64, bool)
	// AbortCredentialRotation releases a claim whose rotation did not happen.
	AbortCredentialRotation(profile string, claim uint64) bool
	// NoteCredentialSuccess ends the profile's failure episode after a run on it
	// succeeded; it reports whether there was an episode to end.
	NoteCredentialSuccess(profile string, generation uint64) bool
}

// providerFailureCooldown bounds how often one profile can be rotated in the
// legacy (no shared owner) path. A hub-wired profile pool keeps the cooling
// window in the shared settings store instead, where all projects see it.
const providerFailureCooldown = 10 * time.Minute

// providerRetryNote is the note carried by the single automatic retry. It goes
// to Intent.Notes, which no executor prompt reads: it is operator commentary.
const providerRetryNote = "provider credential rotated; retry once"

// providerFallbackNote is the Intent.Notes line for the one-hop profile switch.
// Like providerRetryNote it is operator commentary, not an executor constraint.
const providerFallbackNote = "provider credentials exhausted; switched to the fallback profile and retried once"

// providerFailureRule is one table row. Order is fixed: exact provider codes
// before fuzzy words, auth before quota before rate.
type providerFailureRule struct {
	class   providerFailure
	needles []string
}

// providerFailureRules is the single table the classifier reads. Needles are
// lowercase; the harness format is `API error (<NNN>): <message>` and vendor
// bodies use the code names below.
var providerFailureRules = []providerFailureRule{
	{providerFailureAuth, []string{
		"api error (401)", "api error (403)",
		"invalid_api_key", "invalid api key", "incorrect api key",
		"unauthorized", "authentication", "authentication_error",
		"permission denied", "invalid token",
	}},
	{providerFailureQuota, []string{
		"api error (402)",
		"insufficient_quota", "insufficient quota",
		"insufficient_balance", "insufficient balance",
		"quota exceeded", "quota_exceeded", "billing", "credit balance",
		"payment required", "余额", "欠费",
		"额度已用尽", "额度用尽", "额度不足", "配额不足", "配额已用完",
	}},
	{providerFailureRate, []string{
		"api error (429)", "rate limit", "rate_limit", "too many requests",
		"overloaded", "overloaded_error",
	}},
}

// classifyProviderFailure turns one failed run's structured result into a
// credential class. Success is not classified. The text it reads is only the
// provider contract (see the file comment); callers pass a Result whose Output
// may have the journal's error-event texts appended.
func classifyProviderFailure(res executor.Result) providerFailure {
	if !res.IsError && !strings.EqualFold(strings.TrimSpace(res.StopReason), "error") {
		return providerFailureNone
	}
	text := strings.ToLower(providerFailureText(res))
	for _, rule := range providerFailureRules {
		for _, needle := range rule.needles {
			if strings.Contains(text, needle) {
				return rule.class
			}
		}
	}
	return providerFailureTransient
}

// providerFailureText is the exact text the classifier may read: the
// transport's stderr/Output plus the harness's stop reason. Never model
// messages, never tool output.
func providerFailureText(res executor.Result) string {
	return res.Output + "\n" + res.StopReason
}

// providerErrorEvidence reads the run's journal and returns the text of its
// kind=error events only. Provider failures show up there as
// `API error (401): …`; the tool/message events around them are context for a
// human, not a credential signal, so they are ignored.
func (s *Server) providerErrorEvidence(runID string) string {
	if strings.TrimSpace(s.DataDir) == "" || !isPlainID(runID) {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(s.DataDir, "runs", runID+".jsonl"))
	if err != nil || len(data) == 0 {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	// A provider failure is at the end of the turn; the head is thousands of
	// ordinary events.
	if len(lines) > 400 {
		lines = lines[len(lines)-400:]
	}
	var b strings.Builder
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, `"kind":"`+executor.KindError+`"`) {
			continue
		}
		var ev struct {
			Kind string `json:"kind"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if ev.Kind != executor.KindError || ev.Text == "" {
			continue
		}
		b.WriteString(ev.Text)
		b.WriteByte('\n')
	}
	return b.String()
}

// afterProviderFailure is the watchdog hook at the end of finish(). It runs in
// its own goroutine (a rotation restarts a host, which takes seconds) and only
// acts for a local intent run that failed on a credential error.
//
// A failure is attributed to the credential generation frozen on the run at
// dispatch (planRun). When the instance has a shared profile owner, the owner
// decides whether the failure is current, stale (an older generation than the
// profile now uses), a duplicate of a rotation already in flight, or inside a
// cooling window; only an "untried credential" claim rotates, and only a
// no-spare/exhausted pool falls through to the one-hop fallback. That is what
// stops a duplicate callback or an old-key concurrent failure from rotating
// twice or pausing a project that already runs on a new key (F6).
func (s *Server) afterProviderFailure(runID, intentID, status string, res executor.Result) {
	if s == nil {
		return
	}
	run, ok := s.Store.GetRun(runID)
	if !ok {
		return
	}
	// Verifier/report runs settle in their own generators and cannot be
	// re-dispatched as an intent attempt; the commander round has its own D67
	// hook (commander.go). Only intent runs belong to this layer.
	if run.Purpose != "" {
		return
	}
	profile := strings.TrimSpace(run.ResolvedProfile)
	if profile == "" {
		return
	}
	// Only the local node can have profile hosts; a remote node owns its own
	// configuration and its own credentials.
	if s.LocalNode != "" && run.Node != s.LocalNode {
		return
	}
	// A successful run on this profile proves a credential works: end the
	// failure episode, so a later failure does not inherit "every key was
	// tried" from an outage that is already over.
	if status == model.RunDone {
		if s.Credentials != nil && s.Credentials.NoteCredentialSuccess(profile, run.CredentialGeneration) {
			log.Printf("provider_event=episode_reset profile=%s", profile)
		}
		return
	}
	if status != model.RunFailed {
		return
	}
	if s.RotateProfileKey == nil && s.ProfileFallback == nil {
		return // legacy single-model instance: nothing to rotate or switch
	}
	class := s.providerFailureClass(runID, res)
	if !class.rotatable() {
		return
	}
	// The default host is executed by the host tw started at boot, which is not
	// in the profile pool and whose env keeps the primary key, so rotating here
	// would advance a cursor that this run never used (D95). Say so instead; the
	// fallback below is the escape.
	if run.Profile == "" {
		log.Printf("provider_event=rotate_skipped profile=%s class=%s reason=default_host", profile, class)
		s.afterProviderFallback(runID, intentID, run, class)
		return
	}
	if s.Credentials == nil {
		s.afterLegacyProviderFailure(runID, intentID, run, class)
		return
	}
	s.afterSharedProviderFailure(runID, intentID, run, class)
}

// afterSharedProviderFailure asks the shared profile owner what this failure
// means. The owner, not this Server, decides stale/rotating/cooling; the Server
// only executes the resulting action (one rotation, the one-hop fallback, or
// nothing).
func (s *Server) afterSharedProviderFailure(runID, intentID string, run model.Run, class providerFailure) {
	profile := strings.TrimSpace(run.ResolvedProfile)
	plan, err := s.Credentials.BeginCredentialFailure(profile, run.CredentialGeneration)
	if err != nil {
		log.Printf("provider_event=credential_state_error profile=%s class=%s err=%q", profile, class, err.Error())
		// The shared state could not be read; do not invent a rotation. The
		// configured fallback is still the operator-visible escape.
		s.afterProviderFallback(runID, intentID, run, class)
		return
	}
	switch {
	case plan.Stale:
		log.Printf("provider_event=stale_failure profile=%s class=%s run_generation=%d current_generation=%d",
			profile, class, plan.RunGeneration, plan.Generation)
		// A failure bound to a credential generation the profile no longer uses
		// must not rotate, fall back, retry or pause: it is an echo of an episode
		// that was already handled, and the project is not necessarily dead.
		return
	case plan.Rotate:
		s.rotateSharedCredential(runID, intentID, run, plan, class)
	case plan.State == settings.CredentialRotating:
		log.Printf("provider_event=rotation_in_flight profile=%s class=%s generation=%d",
			profile, class, plan.Generation)
		// The rotation is already happening for this profile; a second one would
		// burn another credential and can race the host restart.
		return
	case plan.State == settings.CredentialCooling:
		log.Printf("provider_event=cooling profile=%s class=%s generation=%d",
			profile, class, plan.Generation)
		// A rotation just succeeded. A failure inside the cooling window does not
		// prove the new key (or the whole pool) is unusable, so neither rotate nor
		// pause; a later failure outside the window decides.
		return
	case plan.State == settings.CredentialNoSpare, plan.State == settings.CredentialExhausted:
		log.Printf("provider_event=rotate_skipped profile=%s class=%s reason=%s pool=%d attempted=%d",
			profile, class, plan.State, plan.PoolSize, plan.Attempted)
		// No credential is left in this episode: the operator-configured one-hop
		// fallback is the next action, and only its absence may pause the project.
		s.afterProviderFallback(runID, intentID, run, class)
	default:
		// A ready state without a claim is not a state the owner produces; treat
		// it like "no rotation happened" instead of silently retaining a dead key.
		log.Printf("provider_event=rotate_skipped profile=%s class=%s reason=no_claim state=%s",
			profile, class, plan.State)
		s.afterProviderFallback(runID, intentID, run, class)
	}
}

// rotateSharedCredential performs the rotation this caller claimed from the
// shared owner. Exactly one of Complete/Abort releases the claim, so a stuck
// "rotating" state cannot survive a failed rotation.
func (s *Server) rotateSharedCredential(runID, intentID string, run model.Run, plan settings.CredentialFailure, class providerFailure) {
	profile := plan.Profile
	if s.RotateProfileKey == nil {
		// A fallback-only instance (no profile host pool) can still switch, but
		// there is no rotation cursor to advance; never call a nil hook.
		s.Credentials.AbortCredentialRotation(profile, plan.Claim)
		log.Printf("provider_event=rotate_skipped profile=%s class=%s reason=no_rotation_hook", profile, class)
		s.afterProviderFallback(runID, intentID, run, class)
		return
	}
	rotated, err := s.RotateProfileKey(profile)
	switch {
	case err != nil:
		s.Credentials.AbortCredentialRotation(profile, plan.Claim)
		log.Printf("provider_event=rotate_failed profile=%s class=%s err=%q", profile, class, err.Error())
		s.afterProviderFallback(runID, intentID, run, class)
	case !rotated:
		s.Credentials.AbortCredentialRotation(profile, plan.Claim)
		log.Printf("provider_event=rotate_skipped profile=%s class=%s reason=no_spare", profile, class)
		s.afterProviderFallback(runID, intentID, run, class)
	default:
		generation, committed := s.Credentials.CompleteCredentialRotation(profile, plan.Claim)
		if !committed {
			log.Printf("provider_event=rotation_superseded profile=%s", profile)
			return
		}
		restarted, retried := s.restartHostAndRetryOnce(runID, intentID, profile, class)
		log.Printf("provider_event=rotated profile=%s class=%s generation=%d restarted=%t retried=%s",
			profile, class, generation, restarted, retried)
	}
}

// afterLegacyProviderFailure is the pre-F6 path for instances without a shared
// profile owner (flat/single-model setups and bare test servers). It keeps the
// old per-Server rotation claim/cooldown because there is no shared profile state
// to consult; a hub-wired profile pool always has Credentials and takes the
// shared path above.
func (s *Server) afterLegacyProviderFailure(runID, intentID string, run model.Run, class providerFailure) {
	profile := strings.TrimSpace(run.ResolvedProfile)
	switch {
	case s.RotateProfileKey == nil:
		log.Printf("provider_event=rotate_skipped profile=%s class=%s reason=no_rotation_hook", profile, class)
	case s.claimProviderRotation(profile):
		rotated, err := s.RotateProfileKey(profile)
		switch {
		case err != nil:
			s.releaseProviderRotation(profile)
			log.Printf("provider_event=rotate_failed profile=%s class=%s err=%q", profile, class, err.Error())
		case !rotated:
			s.releaseProviderRotation(profile)
			log.Printf("provider_event=rotate_skipped profile=%s class=%s reason=no_spare", profile, class)
		default:
			restarted, retried := s.restartHostAndRetryOnce(runID, intentID, profile, class)
			log.Printf("provider_event=rotated profile=%s class=%s restarted=%t retried=%s", profile, class, restarted, retried)
			return
		}
	default:
		log.Printf("provider_event=rotate_skipped profile=%s class=%s reason=cooldown", profile, class)
	}
	// No key was rotated (default host, no spare, cooldown or an error). Try the
	// operator-configured one-hop fallback; if there is none, stop the project
	// rather than keep dispatching against dead credentials (D67/D95).
	s.afterProviderFallback(runID, intentID, run, class)
}

// restartHostAndRetryOnce rebuilds the profile host so the rotated key reaches
// the harness, then re-dispatches the failed run exactly once. It is shared by
// the shared-owner and legacy paths.
func (s *Server) restartHostAndRetryOnce(runID, intentID, profile string, class providerFailure) (restarted bool, retried string) {
	if s.RestartProfileHost != nil {
		if err := s.RestartProfileHost(profile); err != nil {
			// A failed rebuild is not fatal: the host supervisor keeps trying the
			// profile's host on the next dispatch, and the console shows the
			// error. Retrying here would loop.
			log.Printf("provider_event=restart_failed profile=%s class=%s err=%q", profile, class, err.Error())
		} else {
			restarted = true
		}
	}
	// One automatic retry per failed run: the run that just died is the only
	// thing that may be re-dispatched, and only while its intent still says
	// failed/pending and nothing newer took it.
	if s.claimProviderRetry(runID) && s.providerRetryAllowed(intentID) {
		out, retryErr := s.retryProviderIntent(intentID)
		if retryErr != nil {
			log.Printf("provider_event=retry_failed intent=%s err=%q", intentID, retryErr.Error())
		} else {
			retried = out
		}
	}
	return restarted, retried
}

// providerFailureClass merges the transport result with the journal's structured
// error events and classifies it. Both the executor hook and the commander round
// use this one function, so "is this the provider saying no?" has one answer.
func (s *Server) providerFailureClass(runID string, res executor.Result) providerFailure {
	probe := res
	if evidence := s.providerErrorEvidence(runID); evidence != "" {
		probe.Output = probe.Output + "\n" + evidence
	}
	return classifyProviderFailure(probe)
}

// afterProviderFallback is the second half of the credential layer: when the
// same profile has no key left to try, move this intent's next attempt to the
// operator's configured fallback profile exactly once. If that is not possible,
// pause the project (D67) instead of spending rounds on a dead credential.
func (s *Server) afterProviderFallback(runID, intentID string, run model.Run, class providerFailure) {
	source := strings.TrimSpace(run.ResolvedProfile)
	if run.FallbackFrom != "" {
		// The fallback run already switched once. A second hop is deliberately
		// not automatic: chained switching hides how deep the outage is and can
		// ping-pong between two dead profiles.
		s.pauseForProviderFailure(source, class, "fallback already used")
		return
	}
	if s.ProfileFallback == nil {
		s.pauseForProviderFailure(source, class, "no fallback configured")
		return
	}
	target, err := s.ProfileFallback(source)
	if err != nil {
		log.Printf("provider_event=fallback_invalid profile=%s class=%s err=%q", source, class, err.Error())
		s.addProviderWarning(runID, fmt.Sprintf("模型配置 %s 的 fallback 配置不可用：%s", source, err.Error()))
		s.pauseForProviderFailure(source, class, "fallback invalid")
		return
	}
	if target == "" {
		s.pauseForProviderFailure(source, class, "no fallback configured")
		return
	}
	if !s.claimProviderFallback(runID) || !s.providerRetryAllowed(intentID) {
		return
	}
	out, ferr := s.retryProviderIntentWithProfile(intentID, providerFallbackNote, target, source)
	if ferr != nil {
		log.Printf("provider_event=fallback_failed from=%s to=%s class=%s err=%q", source, target, class, ferr.Error())
		s.addProviderWarning(runID, fmt.Sprintf("从模型配置 %s 切到 %s 失败：%s", source, target, ferr.Error()))
		s.pauseForProviderFailure(source, class, "fallback start failed")
		return
	}
	s.addProviderWarning(runID, fmt.Sprintf("模型配置 %s 的凭据不可用（%s），已自动切到 %s（新 run %s）；本 run 的绑定与成本记录不变", source, class, target, out))
	log.Printf("provider_event=fallback from=%s to=%s class=%s run=%s", source, target, class, out)
}

// pauseForProviderFailure is D67 wired. A credential that is gone is not a
// transient error: retrying it on a heartbeat only burns rounds and leaves
// half-finished cases. Stop dispatch, pause the commander and cancel live runs
// (their intents settle back to pending); only POST /api/project/resume brings
// the project back, and a human decides when.
func (s *Server) pauseForProviderFailure(profile string, class providerFailure, why string) {
	if s.paused.Load() {
		return
	}
	s.SetPaused(true)
	reason := fmt.Sprintf("模型额度/key 不可用（%s：profile=%s，%s）：换 key / profile 后手动继续", class, profile, why)
	if s.Decider != nil {
		s.Decider.PauseFor(reason)
	}
	canceled := s.CancelAllWithCause(model.CancelCausePause, reason)
	log.Printf("provider_event=project_paused profile=%s class=%s why=%q canceled=%d", profile, class, why, canceled)
}

// addProviderWarning appends one operator-facing warning to a run. Warnings are
// diagnostics, not facts: they never change a run's status or an intent's state.
func (s *Server) addProviderWarning(runID, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	_, _ = s.Store.UpdateRun(runID, func(r *model.Run) {
		for _, w := range r.Warnings {
			if w == text {
				return
			}
		}
		r.Warnings = append(r.Warnings, text)
	})
}

// claimProviderFallback records that this failed run already used its one
// profile switch. The map is capped like retriedFor: it only grows with real
// provider failures, but a long-lived instance must not keep ids forever.
func (s *Server) claimProviderFallback(runID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fallbackFor == nil {
		s.fallbackFor = map[string]bool{}
	}
	if s.fallbackFor[runID] {
		return false
	}
	if len(s.fallbackFor) > 4096 {
		s.fallbackFor = map[string]bool{}
	}
	s.fallbackFor[runID] = true
	return true
}

// retryProviderIntent runs the single retry after a key rotation. The seam
// exists so tests can count retries without starting a real run. A provider
// rotation rebuilds the profile host, so the old session went with it: this is
// always a fresh session (whose brief already carries the failure history), and
// RetryIntent requeues the unconfirmed messages the dead host can no longer
// carry.
func (s *Server) retryProviderIntent(intentID string) (string, error) {
	if fn := s.retryProviderIntentFn; fn != nil {
		return fn(intentID, providerRetryNote, "", "")
	}
	return s.RetryIntent(intentID, providerRetryNote, false)
}

// retryProviderIntentWithProfile starts the one fallback attempt: a new run
// bound to targetProfile, still for the same intent, carrying fallbackFrom so
// the run can never hop again. The note goes to Intent.Notes exactly like the
// rotation retry's note.
func (s *Server) retryProviderIntentWithProfile(intentID, note, targetProfile, fallbackFrom string) (string, error) {
	if fn := s.retryProviderIntentFn; fn != nil {
		return fn(intentID, note, targetProfile, fallbackFrom)
	}
	runID, _, err := s.retryIntentRunProfile(intentID, note, false, targetProfile, fallbackFrom)
	return runID, err
}

// providerRetryAllowed is the "no new run took it" check from the spec.
func (s *Server) providerRetryAllowed(intentID string) bool {
	in, ok := s.Store.GetIntent(intentID)
	if !ok {
		return false
	}
	if in.Status != model.StatusFailed && in.Status != model.StatusPending {
		return false
	}
	if in.RunID != "" {
		if r, ok := s.Store.GetRun(in.RunID); ok && r.Status == model.RunRunning {
			return false
		}
	}
	return true
}

// claimProviderRotation reserves the rotation slot for a profile under the
// cooldown. Release on "no spare key" so an operator who adds a key is not
// blocked for ten minutes.
func (s *Server) claimProviderRotation(profile string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.providerCooldown == nil {
		s.providerCooldown = map[string]time.Time{}
	}
	now := time.Now()
	if last, ok := s.providerCooldown[profile]; ok && now.Sub(last) < providerFailureCooldown {
		return false
	}
	s.providerCooldown[profile] = now
	return true
}

func (s *Server) releaseProviderRotation(profile string) {
	s.mu.Lock()
	delete(s.providerCooldown, profile)
	s.mu.Unlock()
}

// claimProviderRetry records that this run already used its one retry. A run id
// is unique, so the map only grows with actual provider failures; cap it anyway
// so a long-lived instance cannot accumulate ids forever.
func (s *Server) claimProviderRetry(runID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.retriedFor == nil {
		s.retriedFor = map[string]bool{}
	}
	if s.retriedFor[runID] {
		return false
	}
	if len(s.retriedFor) > 4096 {
		s.retriedFor = map[string]bool{}
	}
	s.retriedFor[runID] = true
	return true
}
