package api

// Child delegation, waits, and the wake scheduler.
//
// A child intent is a normal intent with ParentID set: it gets its own case,
// claim, run and profile, and it is still visible on the board. A wait is a
// business state on the parent (StatusWaiting): the parent's turn ends, its
// execution slot is released, and a structured result event moves it back to
// pending. Nothing here invents a second scheduler: it reuses startRun and the
// project's MaxRuns cap.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/kksty/tianwu/internal/aopexec"
	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

const (
	maxWaitTimeout = 7 * 24 * time.Hour
	maxChildTitle  = 200
)

type childIntentReq struct {
	ContextFactIDs     []string `json:"context_fact_ids"`
	PinnedFactIDs      []string `json:"pinned_fact_ids"`
	InterestEntityRefs []string `json:"interest_entity_refs"`
	Title              string   `json:"title"`
	Description        string   `json:"description"`
	Eval               string   `json:"eval"`
	FromFactIDs        []string `json:"from_fact_ids"`
	Profile            string   `json:"profile"`
	MaxTurns           int      `json:"max_turns"`
	IdempotencyKey     string   `json:"idempotency_key"`
}

// caller describes who is delegating. A participant can only delegate from its
// own live run; the console/commander acts for the project and is recorded as
// the commander participant.
type caller struct {
	participant string
	runID       string
	agent       bool
}

func (s *Server) caller(r *http.Request) caller {
	if id, ok := identityOf(r); ok {
		return caller{participant: id.ParticipantID, runID: id.RunID, agent: true}
	}
	return caller{participant: collab.CommanderID}
}

func (s *Server) createChildIntent(w http.ResponseWriter, r *http.Request) {
	parentID := r.PathValue("id")
	c := s.caller(r)
	parent, ok := s.Store.GetIntent(parentID)
	if !ok {
		writeErr(w, http.StatusNotFound, "父意图不存在")
		return
	}
	if c.agent {
		if c.participant != collab.ParticipantID(parentID) {
			writeErr(w, http.StatusForbidden, "只能从自己的意图委派子任务")
			return
		}
		if parent.Status != model.StatusRunning || parent.RunID != c.runID {
			writeErr(w, http.StatusConflict, "只有当前正在运行的 turn 可以委派子任务")
			return
		}
	} else {
		switch parent.Status {
		case model.StatusPending, model.StatusRunning, model.StatusClaimed:
		default:
			writeErr(w, http.StatusConflict, "父意图已结束（"+parent.Status+"）：先 retry/reopen")
			return
		}
		c.runID = parent.RunID
	}
	var req childIntentReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是 JSON")
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	req.Description = strings.TrimSpace(req.Description)
	req.Profile = strings.TrimSpace(req.Profile)
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	if req.Title == "" {
		writeErr(w, http.StatusBadRequest, "title 必填：子任务是账本里的一行")
		return
	}
	if len([]rune(req.Title)) > maxChildTitle {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("title 最多 %d 字", maxChildTitle))
		return
	}
	if req.MaxTurns < 0 || req.MaxTurns > executor.MaxTurnsCap {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("max_turns 必须在 0–%d 之间", executor.MaxTurnsCap))
		return
	}
	if err := s.checkPremiseRefs(req.FromFactIDs); err != nil {
		writePremiseError(w, err)
		return
	}
	for _, id := range append(append([]string(nil), req.ContextFactIDs...), req.PinnedFactIDs...) {
		if _, ok := s.Store.FactByID(strings.TrimSpace(id)); !ok {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "引用的事实不存在：" + id, "code": "fact_ref_missing"})
			return
		}
	}
	if req.Profile != "" {
		if _, err := s.profileBinding(req.Profile); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if s.LocalNode != "" && parent.Node != "" && parent.Node != s.LocalNode {
			writeErr(w, http.StatusConflict, "父意图在远端节点，模型配置只能在本机执行")
			return
		}
	}
	child := model.Intent{
		Title: req.Title, Description: req.Description,
		Eval: strings.TrimSpace(req.Eval), MaxTurns: req.MaxTurns,
		FromFactIDs: req.FromFactIDs, Skills: append([]string(nil), parent.Skills...),
		ContextFactIDs:     req.ContextFactIDs,
		PinnedFactIDs:      req.PinnedFactIDs,
		InterestEntityRefs: req.InterestEntityRefs,
		// An empty profile is not "inherit the parent's": it goes through the
		// same default chain as any intent (intent.Profile → project
		// worker_profile → instance active), so a strong-tier parent still
		// spawns cheap worker-tier children unless it names a profile (D89).
		Profile: req.Profile, Node: parent.Node,
		CreatedBy: c.participant, DelegatedByRun: c.runID, IdempotencyKey: req.IdempotencyKey,
	}
	created, isNew, err := s.Store.CreateChildIntent(parentID, child)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, store.ErrChildIdempotencyConflict) {
			status = http.StatusConflict
		}
		writeErr(w, status, err.Error())
		return
	}
	started, queueNote := false, ""
	if isNew {
		started, queueNote = s.tryStartChild(created.ID)
	}
	resp := map[string]any{
		"child": s.childView(created), "created": isNew,
		"started": started, "queued": queueNote,
	}
	if isNew {
		writeJSON(w, http.StatusCreated, resp)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) tryStartChild(id string) (bool, string) {
	if !s.hasCapacity() {
		return false, "并发已满：等一个 run 结束会自动开始"
	}
	if _, err := s.StartRun(id); err != nil {
		return false, err.Error()
	}
	return true, ""
}

type childIntentView struct {
	ID            string      `json:"id"`
	ParentID      string      `json:"parent_intent_id"`
	Title         string      `json:"title"`
	Description   string      `json:"description,omitempty"`
	Status        string      `json:"status"`
	Profile       string      `json:"profile,omitempty"`
	Node          string      `json:"node,omitempty"`
	Attempts      int         `json:"attempts"`
	RunID         string      `json:"run_id,omitempty"`
	LastRunStatus string      `json:"last_run_status,omitempty"`
	StopReason    string      `json:"stop_reason,omitempty"`
	Facts         int         `json:"facts"`
	CreatedAt     time.Time   `json:"created_at"`
	Wait          *model.Wait `json:"wait,omitempty"`
}

func (s *Server) childView(in model.Intent) childIntentView {
	v := childIntentView{
		ID: in.ID, ParentID: in.ParentID, Title: in.Title, Description: in.Description,
		Status: in.Status, Profile: in.Profile, Node: in.Node, Attempts: in.Attempts,
		RunID: in.RunID, CreatedAt: in.CreatedAt, Wait: in.Wait,
	}
	if in.RunID != "" {
		if r, ok := s.Store.GetRun(in.RunID); ok {
			v.LastRunStatus = r.Status
			v.StopReason = r.StopReason
			if v.StopReason == "" {
				v.StopReason = r.ExitReason
			}
		}
	}
	db := s.Store.Snapshot()
	for _, f := range db.Facts {
		if f.IntentID == in.ID {
			v.Facts++
		}
	}
	return v
}

func (s *Server) listChildIntents(w http.ResponseWriter, r *http.Request) {
	parentID := r.PathValue("id")
	if c := s.caller(r); c.agent && c.participant != collab.ParticipantID(parentID) {
		writeErr(w, http.StatusForbidden, "只能查看自己的子任务")
		return
	}
	if _, ok := s.Store.GetIntent(parentID); !ok {
		writeErr(w, http.StatusNotFound, "意图不存在")
		return
	}
	children := s.Store.Children(parentID)
	out := make([]childIntentView, 0, len(children))
	for _, child := range children {
		out = append(out, s.childView(child))
	}
	writeJSON(w, http.StatusOK, map[string]any{"children": out})
}

type waitIntentReq struct {
	Actions        []string `json:"actions"`
	Children       []string `json:"children"`
	Mode           string   `json:"mode"`
	TimeoutSeconds int      `json:"timeout_seconds"`
}

// waitIntent registers the condition the current turn yields on. It returns
// ready=true when the children have already settled (the parent continues its
// turn); otherwise 202 means "registered, stop this turn".
func (s *Server) waitIntent(w http.ResponseWriter, r *http.Request) {
	intentID := r.PathValue("id")
	in, ok := s.Store.GetIntent(intentID)
	if !ok {
		writeErr(w, http.StatusNotFound, "意图不存在")
		return
	}
	c := s.caller(r)
	runID := in.RunID
	if c.agent {
		if c.participant != collab.ParticipantID(intentID) {
			writeErr(w, http.StatusForbidden, "只能为自己的意图登记等待")
			return
		}
		if in.Status != model.StatusRunning || in.RunID != c.runID {
			writeErr(w, http.StatusConflict, "只有当前正在运行的 turn 可以登记等待")
			return
		}
		runID = c.runID
	} else {
		switch in.Status {
		case model.StatusRunning, model.StatusWaiting, model.StatusPending:
		default:
			writeErr(w, http.StatusConflict, "意图已结束（"+in.Status+"）：不再等待")
			return
		}
	}
	var req waitIntentReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是有效的等待条件 JSON")
		return
	}
	children := make([]string, 0, len(req.Children))
	seen := map[string]bool{}
	for _, id := range req.Children {
		id = strings.TrimSpace(id)
		if id != "" && !seen[id] {
			seen[id] = true
			children = append(children, id)
		}
	}
	if len(children) == 0 && len(req.Actions) == 0 {
		for _, child := range s.Store.ActiveChildren(intentID) {
			children = append(children, child.ID)
		}
	}
	if len(children) == 0 && len(req.Actions) == 0 {
		writeErr(w, http.StatusBadRequest, "没有可等待的子任务：先委派，或显式给 children")
		return
	}
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = model.WaitAll
	}
	var deadline *time.Time
	if req.TimeoutSeconds > 0 {
		// Clamp seconds before converting: a large JSON integer can overflow
		// time.Duration and turn a future deadline into an immediate timeout.
		seconds := min(req.TimeoutSeconds, int(maxWaitTimeout/time.Second))
		d := time.Duration(seconds) * time.Second
		at := time.Now().Add(d)
		deadline = &at
	}
	state, err := s.Store.RegisterWait(intentID, runID, model.Wait{
		Children: children, Actions: req.Actions, Mode: mode, Deadline: deadline,
	})
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, store.ErrWaitInvalid) {
			status = http.StatusBadRequest
		}
		writeErr(w, status, err.Error())
		return
	}
	if state.Ready {
		// A re-registered wait on an already waiting direction may have moved it
		// back to pending; let the scheduler pick it up.
		if cur, ok := s.Store.GetIntent(intentID); ok && cur.Status == model.StatusPending {
			go s.pump()
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ready": true, "reason": state.Reason,
			"results": state.Results, "pending": state.Pending,
		})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"ready": false, "waiting": true, "reason": state.Reason,
		"results": state.Results, "pending": state.Pending,
	})
	// A persisted wait is a scheduler yield, not a request for the model to
	// voluntarily finish. Releasing the parent lets a child use the only slot.
	if in.Status == model.StatusRunning && runID != "" {
		cause, note := model.CancelCauseChildWait, "等待子任务：让出执行槽位"
		if len(req.Actions) > 0 {
			cause, note = model.CancelCauseActionWait, "等待外部动作结果：让出执行槽位"
		}
		_ = s.CancelRunWithCause(runID, cause, note)
	}
}

// hasCapacity is the one slot check used by the scheduler. The API cap counts
// intent runs only; verifier/report purpose runs are not intent work. It reads
// the lifecycle service's answer so the scheduler and dispatch cannot disagree.
func (s *Server) hasCapacity() bool {
	return s.Lifecycle.HasCapacity()
}

// pump starts unstarted children, then wakes parents whose condition is
// satisfied or whose deadline has passed. It is safe to call from many places:
// StartRun's claim and the MaxRuns check make a second scheduler a no-op.
func (s *Server) pump() {
	if s.premisePolicy() == model.PremiseVerifiedOnly {
		s.SchedulePendingReviews()
	}
	s.pumpMu.Lock()
	defer s.pumpMu.Unlock()
	if s.deleting.Load() || s.paused.Load() || s.closing.Load() {
		return
	}
	for _, in := range s.Store.ListIntents() {
		creator, ownedSpawn := collab.IntentIDFromParticipant(in.CreatedBy)
		if in.Status != model.StatusPending || (in.ParentID == "" && !ownedSpawn) || in.Hold || in.Wait != nil {
			// A child cancelled as part of a subtree is failed (not pending),
			// and a crash-reset child is failed too. Pending therefore means
			// "created, or resumed after a project pause"; starting it is what
			// the delegation asked for. An intent with a wait is a wake, handled
			// by the second loop with its results, never as a fresh brief.
			continue
		}
		ownerID := in.ParentID
		if ownerID == "" {
			ownerID = creator
		}
		parent, ok := s.Store.GetIntent(ownerID)
		if !ok || model.Settled(parent.Status) || parent.Hold || parent.Aborted {
			// Nobody will consume these results; do not spend a model run on an
			// orphan. A waiting parent is not settled and still wants them.
			continue
		}
		if !s.hasCapacity() {
			return
		}
		var startErr error
		if previous, ok := s.Store.GetRun(in.RunID); ok && previous.CancelCause == model.CancelCausePause && aopexec.CanResume(filepath.Join(s.CaseRoot, in.ID)) {
			_, startErr = s.startRunWith(in.ID, "项目已显式恢复，继续原方向。先核对中断前已登记的动作与结果；unknown 不代表未执行，不得盲目重放。", in.RunID, true, true)
		} else {
			_, startErr = s.StartRun(in.ID)
		}
		if err := startErr; err != nil {
			log.Printf("scheduler: 子任务 %s 没起来：%v", in.ID, err)
		}
	}
	for _, in := range s.Store.ListIntents() {
		if in.Wait == nil || in.Hold {
			continue
		}
		if in.Status != model.StatusWaiting && in.Status != model.StatusPending {
			continue
		}
		if !in.Wait.WakePending {
			if state, ok := s.Store.EvaluateWait(in.ID); ok && state.Ready {
				if _, _, err := s.Store.SatisfyWait(in.ID); err != nil {
					log.Printf("scheduler: 结算等待 %s 失败：%v", in.ID, err)
					continue
				}
			}
		}
		cur, ok := s.Store.GetIntent(in.ID)
		if !ok || cur.Wait == nil || !cur.Wait.WakePending {
			continue
		}
		if !s.hasCapacity() {
			return
		}
		if cur.Status == model.StatusWaiting {
			// ClaimIntent accepts pending/failed only. The wake is a new turn on
			// a pending direction; the wait (and its structured results) stays
			// attached until startWake consumes it.
			prepared, err := s.Store.UpdateIntent(cur.ID, func(i *model.Intent) {
				i.Status = model.StatusPending
				i.Hold = false
			})
			if err != nil {
				log.Printf("scheduler: 准备唤醒 %s 失败：%v", cur.ID, err)
				continue
			}
			cur = prepared
		}
		s.startWake(cur)
	}
}

// startWake re-dispatches a direction whose wait is satisfied. It continues the
// old session when the host still has it; after a host restart it builds a fresh
// brief with the child results instead of pretending the context survived.
func (s *Server) startWake(in model.Intent) {
	workDir := filepath.Join(s.CaseRoot, in.ID)
	resume := aopexec.CanResume(workDir)
	prompt := s.wakePrompt(in, resume)
	prevRun := in.RunID
	var runID string
	var err error
	if resume {
		runID, err = s.startRunWith(in.ID, prompt, prevRun, true, true)
	} else {
		runID, err = s.startRunWith(in.ID, prompt, "", false, true)
	}
	if err != nil && resume && s.hasCapacity() {
		// The marker may point at a host/session that no longer exists (restart,
		// DB replaced). Do not leave the direction waiting on a dead trajectory:
		// build a fresh brief with the child results and say so honestly.
		log.Printf("scheduler: 唤醒 %s 续话失败，改走新会话：%v", in.ID, err)
		prompt = s.wakePrompt(in, false)
		runID, err = s.startRunWith(in.ID, prompt, "", false, true)
	}
	if err != nil {
		// Capacity or a transient host error: leave the wait (and WakePending)
		// intact so the next finish/watchdog tick tries again.
		log.Printf("scheduler: 唤醒 %s 失败：%v", in.ID, err)
		return
	}
	log.Printf("scheduler: 唤醒 %s（%s，run %s）", in.ID, in.Wait.WakeReason, runID)
}

func (s *Server) wakePrompt(in model.Intent, resume bool) string {
	if resume {
		return "等待已结束，继续原方向；结果由控制面附在本轮交接中。"
	}
	// A fresh wake session is built before the dispatcher writes this turn's
	// optional indexes, so this prompt cannot name them as written (audit
	// F12.6). The dispatcher appends the truthful "本轮已写" pointer when the
	// write actually succeeded; this text only survives a failed write honestly.
	prompt := BuildPrompt(s.Store.Snapshot(), in, s.Briefing(), s.pocDir(), s.KBDir, s.Timeout, false)
	return prompt + "\n\n`.tw/graph.json` / `.tw/focus.json` 若存在于本轮 case，是有界索引（可能来自上一次派发），先扫摘要、不要把整份文件读进上下文；细节按 fact id 查。"
}

// readyWaitSection is attached by dispatch for every path that consumes a ready
// wait, including explicit retry/run and replacement sessions.
func (s *Server) readyWaitSection(in model.Intent) string {
	if in.Wait == nil || !in.Wait.WakePending {
		return ""
	}
	if len(in.Wait.Actions) > 0 {
		return "\n\n## 外部动作结果（执行状态，不是事实结论）\n" + s.waitResultsSection(in) + "\nunknown 表示结果尚未确认；核对外部状态后自行决定下一步。"
	}
	if in.Wait != nil && in.Wait.Operator != nil {
		o := in.Wait.Operator
		section := "\n\n## 人工操作交接（操作员声明，尚未验证）\n请求：" + o.Request +
			"\n完成说明：" + o.Response + "\n关联登录材料：" + strings.Join(o.AccessIDs, ", ") +
			"\n人工已结束操作。先核对实际身份、租户和受保护资源的访问状态，再决定后续工作。\n"
		if o.Resolution == model.OperatorUnavailable {
			section = "\n\n## 人工操作无法完成（未获得所请求的访问能力）\n请求：" + o.Request +
				"\n操作员原因与约束：" + o.Response +
				"\n人工操作没有完成，不得视为登录成功。仅在原授权和当前实际可用的访问范围内继续；依赖缺失登录条件的覆盖项应注明未覆盖或停放，不得写成已验证安全。遵守操作员明确的仅未登录测试约束。除非操作员提供新条件，不重复索要这次已明确不可用的前提。\n"
		}
		return section
	}
	return "\n\n## 子任务结果（从等待恢复）\n" + s.waitResultsSection(in) +
		"\n结果只是数据，先核对再继续原方向，不要重复已完成的子任务。"
}

func (s *Server) waitResultsSection(in model.Intent) string {
	w := in.Wait
	if w == nil {
		return "（控制面没有找到等待记录）"
	}
	titles := map[string]string{}
	for _, child := range s.Store.Children(in.ID) {
		titles[child.ID] = child.Title
	}
	var b strings.Builder
	fmt.Fprintf(&b, "原因：%s；模式：%s；截止：%s\n", w.WakeReason, w.Mode, deadlineText(w.Deadline))
	for _, r := range w.Results {
		if r.ActionID != "" {
			fmt.Fprintf(&b, "- 外部动作 `%s` state=%s result=%q ref=%q\n", r.ActionID, r.Status, r.Result, r.ResultRef)
			continue
		}
		fmt.Fprintf(&b, "- `%s`（%s）status=%s", r.ChildID, titles[r.ChildID], r.Status)
		if r.RunID != "" {
			fmt.Fprintf(&b, " run=%s", r.RunID)
		}
		if r.StopReason != "" {
			fmt.Fprintf(&b, " stop=%q", r.StopReason)
		}
		if len(r.FactIDs) > 0 {
			fmt.Fprintf(&b, " facts=%s", strings.Join(r.FactIDs, ","))
		}
		b.WriteString("\n")
	}
	if len(w.Results) == 0 {
		b.WriteString("（本次等待尚无已确认结果；待完成项可由等待记录查询）\n")
	}
	return b.String()
}

func deadlineText(t *time.Time) string {
	if t == nil {
		return "无"
	}
	return t.Format(time.RFC3339)
}

// canSettleWait reports whether the finish of runID has the right to settle
// this direction's wait. RunID is the most recent holder; a waiting direction's
// own run ends and must be the one that settles it. A run replaced by handoff,
// retry or steer therefore has no settlement right, and neither has anything
// while the direction is being handed over (Hold).
func canSettleWait(in model.Intent, runID string) bool {
	if in.Hold {
		return false
	}
	return in.RunID == "" || in.RunID == runID
}

// afterIntentSettled runs after finish. It notifies the parent (one atomic
// condition check) and pumps the queue. A child's failure wakes the parent just
// like success: the parent decides, the control plane does not retry.
//
// runID is the run that just reached a terminal state. It gates the notification:
// an old run cancelled by a handoff/retry must not wake the parent on behalf of
// its replacement, and the parent's finish later settles the wait it registered.
func (s *Server) afterIntentSettled(intentID, runID, runStatus string) {
	in, ok := s.Store.GetIntent(intentID)
	if !ok {
		return
	}
	// Route before consuming a wait, so a registered waiter gets its structured
	// result once rather than an additional conversation notification.
	s.routeAndDeliver()
	if in.ParentID != "" && canSettleWait(in, runID) {
		if parent, ok := s.Store.GetIntent(in.ParentID); ok && parent.Wait != nil && !model.Settled(parent.Status) && !parent.Hold {
			if state, ok := s.Store.EvaluateWait(parent.ID); ok && state.Ready {
				if _, _, err := s.Store.SatisfyWait(parent.ID); err != nil {
					log.Printf("wake: 结算父意图 %s 失败：%v", parent.ID, err)
				}
			}
		}
	}
	s.pump()
}

// cancelDescendants stops a cancelled direction's whole subtree. Children are
// marked aborted before their runs are cancelled, so the finish path settles
// them as failed instead of pending (a pending child would keep the parent
// waiting forever).
func (s *Server) cancelDescendants(id, reason string) {
	for _, child := range s.Store.Descendants(id) {
		switch child.Status {
		case model.StatusDone, model.StatusFailed, model.StatusParked:
			continue
		}
		if _, err := s.Store.UpdateIntent(child.ID, func(i *model.Intent) {
			i.Hold = true
			i.Aborted = true
			i.Wait = nil
			if i.Status == model.StatusPending || i.Status == model.StatusWaiting {
				i.Status = model.StatusFailed
			}
		}); err != nil {
			log.Printf("cancel: 标记子意图 %s 失败：%v", child.ID, err)
		}
		if (child.Status == model.StatusRunning || child.Status == model.StatusClaimed) && child.RunID != "" {
			if err := s.CancelRunWithCause(child.RunID, model.CancelCauseStopLoss, "父意图取消："+reason); err != nil {
				// No dispatcher owns that run (a row restored by hand, or one
				// that died between AddRun and registration). Leaving it
				// "running" would make the subtree uncancellable; fail it.
				log.Printf("cancel: 停止子意图 %s 的 run 失败，直接结算为失败：%v", child.ID, err)
				_, _ = s.Store.UpdateIntent(child.ID, func(i *model.Intent) {
					i.Status = model.StatusFailed
					i.Hold = true
					i.Aborted = true
					i.Wait = nil
				})
			}
		}
	}
}
