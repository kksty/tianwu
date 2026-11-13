package api

// Spawning a worker, listing workers, and killing one.
//
// A worker is still an intent in the ledger: it has a case, a claim, a run, the
// same concurrency cap and the same cancel machinery. "Spawn" is a first-class
// participant action only in the sense that it is one call: create the intent
// (idempotently) and try to start it, reporting whether it started instead of
// leaving the caller to guess between "queued", "refused" and "created twice".
//
// Authorization for kill is ownership, not project membership: a run may stop a
// worker it created or a descendant of its own direction, and nothing else. The
// operator is the exception: the console routes (workers_console.go) reuse this
// implementation with caller.operator set, so both surfaces answer with the same
// request/response shape instead of growing a second contract.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

const maxSpawnTitle = 200

// workerCaller is who is acting: an executor (identity from its token) or the
// operator (console token). Only two things depend on it: the CreatedBy stamped
// on a spawned intent and the kill authorization rule.
type workerCaller struct {
	participant string // CreatedBy on spawn: "pt_in_x" or actorOf(r)
	intentID    string // caller's own direction (agent only)
	runID       string // caller's live run (agent only)
	operator    bool   // console: sees the whole project, kills any active worker
}

type spawnReq struct {
	ContextFactIDs     []string `json:"context_fact_ids"`
	PinnedFactIDs      []string `json:"pinned_fact_ids"`
	InterestEntityRefs []string `json:"interest_entity_refs"`
	Title              string   `json:"title"`
	Description        string   `json:"description"`
	Eval               string   `json:"eval"`
	Profile            string   `json:"profile"`
	Tier               string   `json:"tier"`
	Node               string   `json:"node"`
	ParentIntentID     string   `json:"parent_intent_id"`
	IdempotencyKey     string   `json:"idempotency_key"`
	FromFactIDs        []string `json:"from_fact_ids"`
	MaxTurns           int      `json:"max_turns"`
}

// workerView is one row of the participant-side roster: enough to decide
// whether to message, peek at or kill this worker, without the full ledger row
// (notes, handoff state, doc) that belongs to the console.
type workerView struct {
	IntentID        string     `json:"intent_id"`
	ParticipantID   string     `json:"participant_id"`
	Title           string     `json:"title"`
	Status          string     `json:"status"`
	ParentIntentID  string     `json:"parent_intent_id,omitempty"`
	CreatedBy       string     `json:"created_by_participant_id,omitempty"`
	DelegatedByRun  string     `json:"delegated_by_run_id,omitempty"`
	Node            string     `json:"node,omitempty"`
	Profile         string     `json:"profile,omitempty"`
	ResolvedProfile string     `json:"resolved_profile,omitempty"`
	Tier            string     `json:"tier,omitempty"`
	RunID           string     `json:"run_id,omitempty"`
	RunStatus       string     `json:"run_status,omitempty"`
	Model           string     `json:"model,omitempty"`
	Running         bool       `json:"running"`
	Hold            bool       `json:"hold,omitempty"`
	LastEventKind   string     `json:"last_event_kind,omitempty"`
	LastEventText   string     `json:"last_event_text,omitempty"`
	LastEventAt     *time.Time `json:"last_event_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	CanKill         bool       `json:"can_kill"`
}

// agentSpawn creates one worker intent and attempts to start it. A capacity
// refusal is not an error: the worker exists (started=false, reason says why)
// and retrying with the same idempotency_key starts that same worker rather
// than creating a second one.
func (s *Server) agentSpawn(w http.ResponseWriter, r *http.Request) {
	id, ok := identityOf(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "缺少协作身份")
		return
	}
	s.spawnWorker(w, r, workerCaller{
		participant: id.ParticipantID, intentID: id.IntentID, runID: id.RunID,
	})
}

// spawnWorker is the one spawn implementation for the operator and the agent.
// The only difference is the caller: an executor may parent under its own live
// direction only, while the operator may parent under any active direction.
func (s *Server) spawnWorker(w http.ResponseWriter, r *http.Request, caller workerCaller) {
	var req spawnReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是 JSON")
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	req.Description = strings.TrimSpace(req.Description)
	req.Eval = strings.TrimSpace(req.Eval)
	req.Profile = strings.TrimSpace(req.Profile)
	req.Tier = strings.ToLower(strings.TrimSpace(req.Tier))
	req.Node = strings.TrimSpace(req.Node)
	req.ParentIntentID = strings.TrimSpace(req.ParentIntentID)
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	if req.Title == "" && req.Description == "" {
		writeErr(w, http.StatusBadRequest, "title 或 description 至少填一个")
		return
	}
	if req.Title == "" {
		req.Title = firstLine(req.Description)
	}
	if len([]rune(req.Title)) > maxSpawnTitle {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("title 最多 %d 字", maxSpawnTitle))
		return
	}
	// Docs/17 §3.2: the key is mandatory because a retried spawn must not create
	// a second individual when the first request's response was lost.
	if req.IdempotencyKey == "" {
		writeErr(w, http.StatusBadRequest, "idempotency_key 必填：重试要能找回同一个 worker，而不是再造一个")
		return
	}
	if req.MaxTurns < 0 || req.MaxTurns > executor.MaxTurnsCap {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("max_turns 必须在 0–%d 之间", executor.MaxTurnsCap))
		return
	}
	profile, err := s.spawnProfile(req.Profile, req.Tier)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	req.Profile = profile
	if err := s.validateSpawnPlacement(req.Node, profile); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.checkPremiseRefs(req.FromFactIDs); err != nil {
		writePremiseError(w, err)
		return
	}
	if req.ParentIntentID != "" {
		parent, ok := s.Store.GetIntent(req.ParentIntentID)
		if !ok {
			writeErr(w, http.StatusNotFound, "父意图不存在："+req.ParentIntentID)
			return
		}
		if caller.operator {
			// The operator may hang work under any direction that is still
			// active (same rule as console child delegation).
			switch parent.Status {
			case model.StatusPending, model.StatusRunning, model.StatusClaimed:
			default:
				writeErr(w, http.StatusConflict, "父意图已结束（"+parent.Status+"）：先 retry/reopen")
				return
			}
		} else {
			// An executor may only hang work under its own direction, and only
			// from the turn that is currently running (same rule as child
			// delegation).
			if parent.ID != caller.intentID {
				writeErr(w, http.StatusForbidden, "只能挂在自己的意图下造人")
				return
			}
			if parent.Status != model.StatusRunning || parent.RunID != caller.runID {
				writeErr(w, http.StatusConflict, "只有当前正在运行的 turn 可以造人")
				return
			}
		}
	}
	created, isNew, err := s.Store.CreateSpawnIntent(model.Intent{
		Title: req.Title, Description: req.Description, Eval: req.Eval,
		Profile: req.Profile, Node: req.Node, ParentID: req.ParentIntentID,
		FromFactIDs: req.FromFactIDs, MaxTurns: req.MaxTurns,
		ContextFactIDs: req.ContextFactIDs, PinnedFactIDs: req.PinnedFactIDs,
		InterestEntityRefs: req.InterestEntityRefs,
		CreatedBy:          caller.participant, DelegatedByRun: caller.runID,
		IdempotencyKey: req.IdempotencyKey,
	})
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, store.ErrSpawnIdempotencyConflict) {
			status = http.StatusConflict
		}
		writeErr(w, status, err.Error())
		return
	}
	started, runID, reason := s.tryStartWorker(created.ID)
	cur, _ := s.Store.GetIntent(created.ID)
	if cur.ID == "" {
		cur = created
	}
	latest := s.latestRuns()
	view := s.workerViewOf(cur, latest[cur.ID], caller)
	var run *model.Run
	if found, ok := s.Store.GetRun(runID); ok {
		run = &found
	}
	code := http.StatusCreated
	if !isNew {
		code = http.StatusOK
	}
	// The canonical shape is the nested view; the flat aliases keep the spawn
	// response usable by a caller that only wants "what did I get, did it
	// start" (worker_id == intent_id: a worker is an intent in this ledger).
	writeJSON(w, code, map[string]any{
		"created": isNew, "started": started, "reason": reason,
		"intent": cur, "run": run, "worker": view,
		"worker_id": cur.ID, "intent_id": cur.ID, "run_id": runID,
		"profile": cur.Profile, "resolved_model": view.Model, "tier": view.Tier,
	})
}

// spawnProfile resolves the requested model binding for a spawn. An explicit
// profile wins; a tier picks the project's tier default (strong / worker). A
// tier with no configured default is an error, not a silent fallback to the
// instance active profile: the caller asked for a tier and would otherwise get
// a different model than it believes it is buying.
func (s *Server) spawnProfile(profile, tier string) (string, error) {
	if tier == "" {
		if profile == "" {
			return "", nil
		}
		if _, err := s.profileBinding(profile); err != nil {
			return "", err
		}
		return profile, nil
	}
	switch tier {
	case "strong", "cheap", "worker":
	default:
		return "", fmt.Errorf("tier 只能是 strong 或 cheap/worker（收到 %q）", tier)
	}
	if profile != "" {
		if _, err := s.profileBinding(profile); err != nil {
			return "", err
		}
		return profile, nil
	}
	worker, strong := "", ""
	if s.ProjectProfiles != nil {
		worker, strong = s.ProjectProfiles.Worker, s.ProjectProfiles.Strong
	}
	want := worker
	if tier == "strong" {
		want = strong
	}
	if strings.TrimSpace(want) == "" {
		return "", fmt.Errorf("tier %s 没有可用的项目 profile：让 operator 配置项目 worker_profile/strong_profile，或显式传 profile", tier)
	}
	if _, err := s.profileBinding(want); err != nil {
		return "", err
	}
	return want, nil
}

// validateSpawnPlacement applies the single-machine rule: named profiles and
// spawns run on the local host; an explicit remote node is rejected.
func (s *Server) validateSpawnPlacement(node, profile string) error {
	if s.Project != nil {
		if node != "" {
			if err := s.checkNode(node); err != nil {
				return err
			}
		}
	} else if node != "" && !s.HasNode(node) {
		return fmt.Errorf("没有这个节点：%s", node)
	}
	if profile == "" {
		return nil
	}
	if s.LocalNode == "" {
		return errors.New("这个实例没有本机宿主：不能按模型配置执行")
	}
	if node != "" && node != s.LocalNode {
		return fmt.Errorf("模型配置 %s 只能在本机宿主 %s 上执行，不能指定节点 %s", profile, s.LocalNode, node)
	}
	if s.Project != nil {
		if err := s.checkNode(s.LocalNode); err != nil {
			return err
		}
	}
	return nil
}

// tryStartWorker starts an already-created worker if the project has a slot.
// started=false with a reason is a normal outcome (capacity, pause, held); the
// intent stays in the ledger and the idempotent retry can start it later.
func (s *Server) tryStartWorker(intentID string) (started bool, runID, reason string) {
	cur, ok := s.Store.GetIntent(intentID)
	if !ok {
		return false, "", "worker 不存在"
	}
	if cur.Status == model.StatusRunning || cur.Status == model.StatusClaimed {
		return cur.RunID != "", cur.RunID, ""
	}
	if model.Settled(cur.Status) {
		return false, cur.RunID, "worker 已结束（" + cur.Status + "）"
	}
	if cur.Hold {
		return false, cur.RunID, "worker 已被 kill（Hold）：不会自动重启"
	}
	if running, limit := s.Lifecycle.ProjectCapacity(); limit > 0 && running >= limit {
		return false, "", fmt.Sprintf("并发已满（%d/%d）：worker 已创建但未启动；用同一 idempotency_key 重试会启动它，不会重复创建",
			running, limit)
	}
	runID, err := s.StartRun(intentID)
	if err != nil {
		return false, "", err.Error()
	}
	return true, runID, ""
}

// agentWorkers lists the project's workers with enough live state to decide what
// to do next. authorized visibility is not restricted to one's own subtree: the
// roster is the "who is here" half of the language ability (peek needs target
// run ids), while kill is where ownership is enforced.
func (s *Server) agentWorkers(w http.ResponseWriter, r *http.Request) {
	id, ok := identityOf(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "缺少协作身份")
		return
	}
	s.workersRoster(w, workerCaller{
		participant: id.ParticipantID, intentID: id.IntentID, runID: id.RunID,
	})
}

// workersRoster is the one roster implementation for the operator and the agent.
func (s *Server) workersRoster(w http.ResponseWriter, caller workerCaller) {
	latest := s.latestRuns()
	intents := s.Store.ListIntents()
	out := make([]workerView, 0, len(intents))
	for _, in := range intents {
		out = append(out, s.workerViewOf(in, latest[in.ID], caller))
	}
	writeJSON(w, http.StatusOK, map[string]any{"workers": out, "count": len(out)})
}

func (s *Server) workerViewOf(in model.Intent, run model.Run, caller workerCaller) workerView {
	runID := in.RunID
	if runID == "" && run.ID != "" {
		runID = run.ID
	}
	resolved := run.ResolvedProfile
	profile := in.Profile
	if profile == "" {
		profile = resolved
	}
	v := workerView{
		IntentID: in.ID, ParticipantID: collab.ParticipantID(in.ID),
		Title: in.Title, Status: in.Status, ParentIntentID: in.ParentID,
		CreatedBy: in.CreatedBy, DelegatedByRun: in.DelegatedByRun,
		Node: in.Node, Profile: profile, ResolvedProfile: resolved,
		Tier: s.profileTier(profile), RunID: runID,
		Hold: in.Hold, CreatedAt: in.CreatedAt,
		CanKill: s.canKillWorker(caller, in),
	}
	if run.ID != "" {
		v.RunStatus = run.Status
		v.Model = run.Model
		v.LastEventKind = run.LastEventKind
		v.LastEventText = run.LastEventText
		v.LastEventAt = run.LastEventAt
		v.Running = in.Status == model.StatusRunning && run.Status == model.RunRunning
	}
	return v
}

// profileTier resolves the tier label of a saved profile for the roster. It is
// display metadata; an unknown profile reports an empty tier instead of
// inventing one.
func (s *Server) profileTier(profileID string) string {
	if profileID == "" || s.Profiles == nil {
		return ""
	}
	profiles, _, err := s.Profiles()
	if err != nil {
		return ""
	}
	for _, p := range profiles {
		if p.ID == profileID {
			return p.Tier
		}
	}
	return ""
}

// canKillWorker answers the roster's can_kill: may this caller kill the target
// right now. A finished worker is false for everyone (kill returns 409); the
// operator may kill any still-active worker; an executor is limited to what it
// created or its own descendants.
func (s *Server) canKillWorker(caller workerCaller, target model.Intent) bool {
	if model.Settled(target.Status) {
		return false
	}
	if caller.operator {
		return true
	}
	return s.mayKillWorker(caller.intentID, caller.participant, target)
}

// mayKillWorker is the ownership rule: the caller created the target, or the
// target is a descendant of the caller's own direction. A participant cannot
// kill itself (that is what a normal cancel/handoff is for).
func (s *Server) mayKillWorker(callerIntentID, callerParticipant string, target model.Intent) bool {
	if callerIntentID != "" && target.ID == callerIntentID {
		return false
	}
	if callerParticipant != "" && target.CreatedBy == callerParticipant {
		return true
	}
	if callerIntentID == "" {
		return false
	}
	seen := map[string]bool{target.ID: true}
	cur := target
	for cur.ParentID != "" {
		if cur.ParentID == callerIntentID {
			return true
		}
		parent, ok := s.Store.GetIntent(cur.ParentID)
		if !ok || seen[parent.ID] {
			return false
		}
		seen[parent.ID] = true
		cur = parent
	}
	return false
}

// agentKillWorker stops a worker the caller owns. Hold is set before the run is
// cancelled so the canceled turn does not fall back to pending and get picked
// up again by the scheduler; Aborted makes the settle path fail the direction
// (not leave it pending forever).
func (s *Server) agentKillWorker(w http.ResponseWriter, r *http.Request) {
	id, ok := identityOf(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "缺少协作身份")
		return
	}
	s.killWorker(w, r, workerCaller{
		participant: id.ParticipantID, intentID: id.IntentID, runID: id.RunID,
	})
}

// killWorker is the one kill implementation for the operator and the agent. The
// operator bypasses the ownership rule (but not the terminal-state check); an
// executor is limited to what it created or its own descendants.
func (s *Server) killWorker(w http.ResponseWriter, r *http.Request, caller workerCaller) {
	raw := strings.TrimSpace(r.PathValue("id"))
	if intentID, ok := collab.IntentIDFromParticipant(raw); ok {
		raw = intentID
	}
	target, found := s.Store.GetIntent(raw)
	if !found {
		writeErr(w, http.StatusNotFound, "没有这个 worker："+raw)
		return
	}
	if !caller.operator && !s.mayKillWorker(caller.intentID, caller.participant, target) {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error": "未授权：只能 kill 自己创建的 worker 或自己方向的后代",
			"code":  "worker_forbidden",
		})
		return
	}
	if model.Settled(target.Status) {
		writeErr(w, http.StatusConflict, "worker 已结束（"+target.Status+"）：不用再 kill")
		return
	}
	// Stop auto-dispatch first: a canceled run returns the intent to pending,
	// and without Hold the scheduler would immediately start it again.
	cur, err := s.Store.UpdateIntent(target.ID, func(i *model.Intent) {
		i.Hold = true
		i.Aborted = true
		i.Wait = nil
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	runID := cur.RunID
	canceled := false
	if runID != "" {
		if run, ok := s.Store.GetRun(runID); ok && run.Status == model.RunRunning {
			if err := s.CancelRunWithCause(runID, model.CancelCauseWorkerKill,
				"worker kill："+caller.participant); err != nil {
				// A running row with no live dispatcher (hand-edited ledger,
				// crash between AddRun and registration): settle it here so the
				// direction cannot stay running forever.
				s.settleKilledRun(runID, target.ID)
			} else {
				canceled = true
			}
		}
	}
	if !canceled {
		// Nothing was live: make the stop final now.
		if _, err := s.Store.UpdateIntent(target.ID, func(i *model.Intent) {
			if !model.Settled(i.Status) {
				i.Status = model.StatusFailed
				i.Hold = true
				i.Aborted = true
				i.Wait = nil
				i.ClaimedBy = ""
			}
		}); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	cur, _ = s.Store.GetIntent(target.ID)
	latest := s.latestRuns()
	writeJSON(w, http.StatusOK, map[string]any{
		"killed": true, "intent_id": target.ID, "run_id": runID,
		"cancel_cause": model.CancelCauseWorkerKill, "canceled": canceled,
		"worker": s.workerViewOf(cur, latest[cur.ID], caller),
	})
}

// settleKilledRun is the fallback for a run row whose dispatcher is gone: it
// writes the terminal row directly with the worker_kill cause. Normal kills go
// through CancelRunWithCause and the dispatch finish path, not this.
func (s *Server) settleKilledRun(runID, intentID string) {
	now := time.Now()
	_, _ = s.Store.UpdateRun(runID, func(r *model.Run) {
		if r.Status != model.RunRunning {
			return
		}
		r.Status = model.RunCanceled
		r.CancelCause = model.CancelCauseWorkerKill
		r.ExitReason = "worker kill"
		r.EndedAt = &now
	})
	if _, err := s.Store.UpdateIntent(intentID, func(i *model.Intent) {
		if !model.Settled(i.Status) {
			i.Status = model.StatusFailed
			i.Hold = true
			i.Aborted = true
			i.Wait = nil
			i.ClaimedBy = ""
		}
	}); err != nil {
		// Best effort: the run row is already terminal, the intent keeps its
		// current status and the next settle/watchdog sweep reconciles it.
		return
	}
}
