package api

// Participant API: the surface a running executor uses to talk to its peers and
// to read the live project graph.
//
// It is mounted under /agent/<project>/api/ and authenticated by a collab token,
// not by the console token: the console token is an instance credential that
// every local process could inherit and that says nothing about which run is
// calling. The hub resolves the token to an Identity and puts it in the request
// context; every handler below derives sender/project from that identity and
// never from the body.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kksty/tianwu/internal/aopexec"
	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

type identityCtxKey struct{}

// WithIdentity attaches the resolved caller to an agent request. The hub is the
// only writer; handlers must treat a missing identity as unauthenticated.
func WithIdentity(ctx context.Context, id collab.Identity) context.Context {
	return context.WithValue(ctx, identityCtxKey{}, id)
}

func identityOf(r *http.Request) (collab.Identity, bool) {
	id, ok := r.Context().Value(identityCtxKey{}).(collab.Identity)
	return id, ok
}

type agentSurfaceCtxKey struct{}

// WithAgentSurface marks a request that authenticated with the harness session
// credential (cmd/tw's roleAgent) instead of the operator's console token. That
// credential has no per-run identity, so the auth gate is the only place that
// knows the caller is a session: it stamps the request before the project mux
// runs. Handlers must not infer the role from anything the model can send.
func WithAgentSurface(ctx context.Context) context.Context {
	return context.WithValue(ctx, agentSurfaceCtxKey{}, true)
}

// IsAgentSurface reports whether the auth gate marked ctx as a harness session
// request. It is the read half of WithAgentSurface for tests and for callers
// that need the role without a *http.Request (harnessSession adds the
// participant identity check).
func IsAgentSurface(ctx context.Context) bool {
	marked, _ := ctx.Value(agentSurfaceCtxKey{}).(bool)
	return marked
}

// harnessSession reports whether the caller is a harness session (commander or
// executor) rather than the operator. The participant mux authenticates an
// executor with a per-run collab identity; the commander talks to the project
// mux with the derived agent token, which carries no identity, so the auth gate
// marks that path instead. Report/verification ("support") runs, their output
// and the review queue are operator-side post-processing and must stay absent
// from either session's API view (docs/01-architecture.md §8).
func harnessSession(r *http.Request) bool {
	if _, ok := identityOf(r); ok {
		return true
	}
	return IsAgentSurface(r.Context())
}

// identityFile is the case-relative credential a run uses to call the
// participant API. It exists only for the lifetime of the turn (the dispatcher
// revokes the token and removes the file when the run settles) and grants
// nothing beyond this project's collaboration/fact surface.
const identityFile = ".tw/identity.json"

func identityPath(workDir string) string {
	return filepath.Join(workDir, filepath.FromSlash(identityFile))
}

func writeRunIdentity(workDir string, id collab.Identity) error {
	path := identityPath(workDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func (s *Server) publishRunIdentity(workDir string, id collab.Identity) error {
	s.identityMu.Lock()
	defer s.identityMu.Unlock()
	in, ok := s.Store.GetIntent(id.IntentID)
	if !ok || in.RunID != id.RunID || in.Status != model.StatusRunning {
		return fmt.Errorf("run %s 已不再持有意图 %s", id.RunID, id.IntentID)
	}
	return writeRunIdentity(workDir, id)
}

func (s *Server) removeRunIdentity(intentID, runID string) {
	s.identityMu.Lock()
	defer s.identityMu.Unlock()
	path := identityPath(filepath.Join(s.CaseRoot, intentID))
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		log.Printf("run %s: 读取待清理身份失败：%v", runID, err)
		return
	}
	var id collab.Identity
	if err := json.Unmarshal(raw, &id); err != nil {
		log.Printf("run %s: 解析待清理身份失败：%v", runID, err)
		return
	}
	if id.RunID != runID {
		return
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("run %s: 清理身份失败：%v", runID, err)
	}
}

// AgentHandler is the mux the hub mounts for participant calls.
func (s *Server) AgentHandler() http.Handler { return s.agentMux }

// Defaults and ceiling for the agent's read-only event tail. The default is the
// same 40 lines the operator's peek shows; the ceiling stops one curl from
// pulling a whole run log into a model's context.
const (
	defaultEventTailLines = 40
	maxEventTailLines     = 200
)

func (s *Server) registerAgent(mux *http.ServeMux) {
	mux.HandleFunc("PUT /api/intents/{id}/context", s.updateTaskContext)
	mux.HandleFunc("GET /api/instructions", s.listInstructions)
	mux.HandleFunc("GET /api/instructions/{id}", s.getInstruction)
	mux.HandleFunc("GET /api/instructions/{id}/history", s.instructionHistory)
	mux.HandleFunc("POST /api/instructions/{id}/ack", s.acknowledgeInstruction)
	mux.HandleFunc("GET /api/traffic/flows", s.listHTTPFlows)
	mux.HandleFunc("GET /api/traffic/flow", s.httpFlow)
	mux.HandleFunc("GET /api/kb", s.worldKB)
	mux.HandleFunc("POST /api/kb/candidates", s.nominateWorldKB)
	s.registerActions(mux)
	s.registerResourceRoutes(mux)
	s.registerFocusRoutes(mux)
	s.registerCommunicationRoutes(mux)
	mux.HandleFunc("GET /api/artifacts", s.listArtifacts)
	mux.HandleFunc("GET /api/artifacts/data", s.artifactData)
	mux.HandleFunc("GET /api/access", s.listAccess)
	mux.HandleFunc("GET /api/access/{id}/material", s.accessMaterial)
	mux.HandleFunc("POST /api/access/{id}/lease", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := identityOf(r); !ok {
			writeErr(w, 401, "缺少本轮协作身份")
			return
		}
		s.browserLease(w, r)
	})
	mux.HandleFunc("POST /api/intents/{id}/operator", s.requestOperator)
	mux.HandleFunc("GET /api/participants", s.agentParticipants)
	mux.HandleFunc("GET /api/participants/self", s.agentSelf)
	// Model discovery: the same sanitized view the console has, so a run can
	// choose a profile it can actually start instead of guessing an id.
	mux.HandleFunc("GET /api/model-profiles", s.agentModelProfiles)
	// Read-only tail of any run in this project: the participant-side "peek".
	mux.HandleFunc("GET /api/runs/{id}/events/tail", s.agentRunEventsTail)
	// Spawn/workers/kill: one call creates a ledger worker and reports whether
	// it started; ownership (not project membership) gates kill.
	mux.HandleFunc("POST /api/spawn", s.agentSpawn)
	mux.HandleFunc("GET /api/workers", s.agentWorkers)
	mux.HandleFunc("POST /api/workers/{id}/kill", s.agentKillWorker)
	mux.HandleFunc("GET /api/messages", s.agentMessages)
	mux.HandleFunc("POST /api/messages", s.agentSendMessage)
	mux.HandleFunc("POST /api/messages/{id}/ack", s.agentAckMessage)
	mux.HandleFunc("POST /api/messages/{id}/retract", s.retractMessage)
	mux.HandleFunc("GET /api/intents/{id}", s.agentIntent)
	mux.HandleFunc("GET /api/intents/{id}/readiness", s.getIntentReadiness)
	mux.HandleFunc("GET /api/facts", s.agentFacts)
	mux.HandleFunc("GET /api/changes", s.changes)
	mux.HandleFunc("GET /api/context", s.workingContext)
	mux.HandleFunc("GET /api/experience", s.experienceCandidates)
	mux.HandleFunc("GET /api/playbooks/{id}/history", s.playbookHistory)
	mux.HandleFunc("POST /api/retrieval/index", s.indexFacts)
	mux.HandleFunc("GET /api/retrieval/status", s.retrievalStatus)
	mux.HandleFunc("POST /api/context/ack", s.acknowledgeContext)
	mux.HandleFunc("GET /api/graph", s.graphQuery)
	mux.HandleFunc("GET /api/facts/{id}/history", s.factHistory)
	mux.HandleFunc("GET /api/runs/{id}/output", s.runOutput)
	mux.HandleFunc("GET /api/facts/{id}", s.agentFact)
	// Delegation and waits are the same handlers as the console; the caller is
	// derived from the context identity, and a participant may only act for its
	// own intent.
	mux.HandleFunc("POST /api/intents/{id}/children", s.createChildIntent)
	mux.HandleFunc("GET /api/intents/{id}/children", s.listChildIntents)
	mux.HandleFunc("POST /api/intents/{id}/wait", s.waitIntent)
	mux.HandleFunc("POST /api/intents/{id}/handoff", s.handoffIntent)
	mux.HandleFunc("GET /api/intents/{id}/usage", s.intentUsage)
}

// participantView is one addressable participant. contactable means "a message
// sent now will either reach a live run or be queued for its next run"; a
// finished direction is listed with contactable=false and must be reopened
// explicitly.
type participantView struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"` // intent|commander
	IntentID      string `json:"intent_id,omitempty"`
	Title         string `json:"title"`
	Status        string `json:"status,omitempty"`
	Generation    int    `json:"generation,omitempty"`
	RunID         string `json:"run_id,omitempty"`
	Profile       string `json:"profile,omitempty"`
	ResolvedModel string `json:"model,omitempty"`
	// Tier is the routing tier frozen on this direction's current run
	// ("strong" / "worker"), empty when there is no run or the run predates the
	// field. It is how a peer learns whether it is talking to a strong or a
	// worker direction without knowing any role name; the profile catalogue is
	// still the place to look up what a tier means (omitempty on old rows).
	Tier           string `json:"tier,omitempty"`
	HandoffVersion int    `json:"handoff_version,omitempty"`
	Contactable    bool   `json:"contactable"`
	Note           string `json:"note,omitempty"`
	// Authorized answers "may I send this participant a message right now"
	// under the project's peer policy. It is only filled on the participant's
	// own roster (the agent view); the console roster leaves it nil, because the
	// operator is not subject to peer rules.
	Authorized *bool `json:"authorized,omitempty"`
}

func (s *Server) agentParticipants(w http.ResponseWriter, r *http.Request) {
	id, ok := identityOf(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "缺少协作身份")
		return
	}
	views := s.participantViews()
	for i := range views {
		allowed := true
		switch views[i].ID {
		case collab.CommanderID, id.ParticipantID:
			// The commander is the operator's channel and is exempt from peer
			// policy; a participant cannot message itself (the send path
			// refuses it before authorization is consulted).
		default:
			allowed = s.peerAllowed(id.ParticipantID, views[i].ID)
		}
		views[i].Authorized = &allowed
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"self": id.ParticipantID, "participants": views,
	})
}

// listParticipants is the console's roster. It has no "self": the operator is
// not a participant, and the commander is listed as a non-messageable row.
func (s *Server) listParticipants(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"participants": s.participantViews()})
}

func (s *Server) participantViews() []participantView {
	out := []participantView{{
		ID: collab.CommanderID, Kind: "commander", Title: "指挥官",
		Contactable: true, Note: "可以直接发消息：正在跑的一轮经 IOA 送入，否则排队并在下一轮提示词里注入",
	}}
	latest := s.latestRuns()
	for _, in := range s.Store.ListIntents() {
		out = append(out, s.participantOf(in, latest[in.ID]))
	}
	return out
}

// agentModelProfiles is the participant-facing model list. It delegates to the
// console handler so the two views cannot drift, but keeps an identity check
// here: the console route is gated by the console token, while this mux is
// mounted for collab tokens and should not answer an unauthenticated caller
// even if it were ever reached outside the hub. The response is the same
// ProfileInfo shape, which never contains a credential (docs/17 §3.2).
func (s *Server) agentModelProfiles(w http.ResponseWriter, r *http.Request) {
	if _, ok := identityOf(r); !ok {
		writeErr(w, http.StatusUnauthorized, "缺少协作身份")
		return
	}
	s.listModelProfiles(w, r)
}

// runTailView is the run card shown next to an event tail. It carries only the
// identity/status fields a peer needs to make sense of the events; the full run
// row (pricing, usage, exit reason, log path) stays on the console.
type runTailView struct {
	ID              string `json:"id"`
	IntentID        string `json:"intent_id"`
	Purpose         string `json:"purpose,omitempty"`
	Status          string `json:"status"`
	Model           string `json:"model,omitempty"`
	Profile         string `json:"profile,omitempty"`
	ResolvedProfile string `json:"resolved_profile,omitempty"`
}

// agentRunEventsTail is the participant-side peek: the last N events of any run
// in this project, read-only. It is the primitive behind "look at what a peer
// or a child is doing" (sapper's `peek`, nvwabase's capture), and it is
// deliberately not "only my own run": a run looking at its own tail is what
// gives it a chance to check its next step, but the useful case is the peer.
//
// Scope: the Store is this project's ledger, so a run from another project is
// simply not found (404). The id is one path segment (isPlainID) because it is
// also a file name; the journal path is built from it in readRunEventTail.
//
// Report/verification runs are operator-side support work. A verifier run keeps
// the reviewed fact's intent id and is listed in coverage/readiness, so a
// session can learn its id without guessing: it must not be able to read that
// run's events. Same 404 as a missing run, so the surface gives nothing away.
func (s *Server) agentRunEventsTail(w http.ResponseWriter, r *http.Request) {
	if _, ok := identityOf(r); !ok {
		writeErr(w, http.StatusUnauthorized, "缺少协作身份")
		return
	}
	runID := r.PathValue("id")
	if !isPlainID(runID) {
		writeErr(w, http.StatusNotFound, "没有这个 run："+runID)
		return
	}
	run, ok := s.Store.GetRun(runID)
	if !ok || supportPurpose(run.Purpose) {
		writeErr(w, http.StatusNotFound, "没有这个 run："+runID)
		return
	}
	lines, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("lines")))
	if err != nil || lines <= 0 {
		lines = defaultEventTailLines
	}
	if lines > maxEventTailLines {
		writeErr(w, http.StatusBadRequest,
			fmt.Sprintf("lines 上限 %d：一次只读事件尾部，完整日志在控制台", maxEventTailLines))
		return
	}
	events, total, err := readRunEventTail(s.DataDir, runID, lines)
	if err != nil {
		writeErr(w, http.StatusNotFound, "run 没有事件日志："+runID)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"run": runTailView{
			ID: run.ID, IntentID: run.IntentID, Purpose: run.Purpose, Status: run.Status,
			Model: run.Model, Profile: run.Profile, ResolvedProfile: run.ResolvedProfile,
		},
		"events":    events,
		"lines":     lines,
		"truncated": total > lines,
	})
}

// agentIntent lets a run read its own intent, including handoff_version for the
// optimistic-concurrency check.
func (s *Server) agentIntent(w http.ResponseWriter, r *http.Request) {
	id, ok := identityOf(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "缺少协作身份")
		return
	}
	intentID := r.PathValue("id")
	if intentID != id.IntentID {
		writeErr(w, http.StatusForbidden, "只能读取自己的意图")
		return
	}
	in, ok := s.Store.GetIntent(intentID)
	if !ok {
		writeErr(w, http.StatusNotFound, "意图不存在")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"intent": in})
}

func (s *Server) agentSelf(w http.ResponseWriter, r *http.Request) {
	id, ok := identityOf(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "缺少协作身份")
		return
	}
	in, found := s.Store.GetIntent(id.IntentID)
	if !found {
		writeErr(w, http.StatusNotFound, "意图不存在")
		return
	}
	latest := s.latestRuns()[in.ID]
	writeJSON(w, http.StatusOK, map[string]any{
		"identity": map[string]any{
			"project_id": id.ProjectID, "participant_id": id.ParticipantID,
			"intent_id": id.IntentID, "run_id": id.RunID, "generation": id.Generation,
			"expires_at": id.ExpiresAt,
		},
		"participant": s.participantOf(in, latest),
	})
}

func (s *Server) participantOf(in model.Intent, run model.Run) participantView {
	v := participantView{
		ID: collab.ParticipantID(in.ID), Kind: "intent", IntentID: in.ID,
		Title: in.Title, Status: in.Status, Generation: in.Attempts,
		RunID: in.RunID, Profile: in.Profile, HandoffVersion: in.HandoffVersion,
	}
	if run.ID != "" {
		v.RunID = run.ID
		v.ResolvedModel = run.Model
		v.Tier = run.Tier
		if v.Profile == "" {
			v.Profile = run.ResolvedProfile
		}
	}
	switch in.Status {
	case model.StatusPending, model.StatusClaimed, model.StatusRunning:
		v.Contactable = true
	case model.StatusReviewing:
		v.Contactable = false
		v.Note = "固定复核进行中；复核结束前没有可投递的执行轮次"
	case model.StatusDone, model.StatusFailed, model.StatusReviewFailed, model.StatusParked:
		v.Contactable = false
		v.Note = "方向已结束；要重新联系必须先 retry/reopen，消息不会静默复活它"
	}
	return v
}

// latestRuns maps intent id to its newest run. The intent only stores the
// current run; the map is for the model/revision display when that pointer is
// stale or empty.
//
// Report/verification runs are skipped: a verifier run inherits the reviewed
// fact's intent id, and otherwise it would be reported as the direction's
// current holder (its run id, model and profile are operator-side support
// work). The intent's own run is the newest run that actually is the direction.
func (s *Server) latestRuns() map[string]model.Run {
	out := map[string]model.Run{}
	for _, r := range s.Store.Runs() {
		if supportPurpose(r.Purpose) {
			continue
		}
		cur, ok := out[r.IntentID]
		if !ok || r.StartedAt.After(cur.StartedAt) {
			out[r.IntentID] = r
		}
	}
	return out
}

type sendMessageReq struct {
	Durable        bool     `json:"durable"`
	To             string   `json:"to"`
	Kind           string   `json:"kind"`
	Text           string   `json:"text"`
	FactRefs       []string `json:"fact_refs"`
	InReplyTo      string   `json:"in_reply_to"`
	IdempotencyKey string   `json:"idempotency_key"`
	ToGeneration   int      `json:"to_generation"`
}

const maxMessageText = 16 << 10

func (s *Server) agentSendMessage(w http.ResponseWriter, r *http.Request) {
	id, ok := identityOf(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "缺少协作身份")
		return
	}
	var req sendMessageReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是 JSON")
		return
	}
	req.To = strings.TrimSpace(req.To)
	req.Text = strings.TrimSpace(req.Text)
	req.Kind = strings.TrimSpace(req.Kind)
	if req.To == "" || req.Text == "" {
		writeErr(w, http.StatusBadRequest, "to 与 text 必填")
		return
	}
	if len([]rune(req.Text)) > maxMessageText {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("消息太长（上限 %d 字）", maxMessageText))
		return
	}
	if req.To == id.ParticipantID {
		writeErr(w, http.StatusBadRequest, "不能给自己发消息")
		return
	}
	// References are checked before the target branch: they name ledger rows
	// that either exist or do not, whether the recipient is an intent or the
	// commander.
	for _, ref := range req.FactRefs {
		if _, ok := s.Store.FactByID(strings.TrimSpace(ref)); !ok {
			writeErr(w, http.StatusBadRequest, "引用的事实不存在："+ref)
			return
		}
	}
	if req.InReplyTo != "" {
		quoted, ok := s.Store.MessageByID(req.InReplyTo)
		if !ok {
			writeErr(w, http.StatusBadRequest, "回复的消息不存在："+req.InReplyTo)
			return
		}
		// A reply is a correlation claim, not a free-form annotation: it must
		// join the two parties of this send, in either direction. A result may
		// legitimately quote the acceptance the same participant just sent (the
		// thread reads request -> acceptance -> result), so requiring the other
		// party's message only would break the closed loop. Letting it name an
		// arbitrary third-party message would make the request/acceptance/result
		// view attributable to a conversation that never happened (tests surface
		// unverifiable quotes as quote_valid=false, but the write path should not
		// create them at all).
		betweenParties := (quoted.FromParticipantID == id.ParticipantID && quoted.ToParticipantID == req.To) ||
			(quoted.FromParticipantID == req.To && quoted.ToParticipantID == id.ParticipantID)
		if !betweenParties {
			writeErr(w, http.StatusBadRequest, "回复引用不成立：只能引用你们两人之间的一条消息")
			return
		}
	}
	// The commander is a participant like an intent is (98 §6.2): a message
	// addressed to it is stored the same way and delivered by the same service,
	// so a round that is not live still gets it in its next prompt.
	if req.To == collab.CommanderID {
		if req.ToGeneration != 0 {
			writeErr(w, http.StatusBadRequest, "指挥官不使用 generation：不要带 to_generation")
			return
		}
		created, isNew, err := s.Store.AddMessage(model.Message{
			Durable:           req.Durable,
			ProjectID:         id.ProjectID,
			FromParticipantID: id.ParticipantID, FromRunID: id.RunID, FromGeneration: id.Generation,
			ToParticipantID: collab.CommanderID,
			Kind:            req.Kind, Text: req.Text, FactRefs: req.FactRefs,
			InReplyTo: req.InReplyTo, IdempotencyKey: req.IdempotencyKey,
			Status: model.MessageQueued,
		})
		if err != nil {
			if errors.Is(err, store.ErrIdempotencyConflict) {
				writeErr(w, http.StatusConflict, err.Error())
				return
			}
			if errors.Is(err, store.ErrMessageBackpressure) {
				s.writeBackpressure(w, collab.CommanderID, err)
				return
			}
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !isNew {
			// A retry of an accepted message must not deliver twice.
			writeJSON(w, http.StatusOK, map[string]any{"message": created, "created": false, "delivery": created.Status})
			return
		}
		delivery := s.deliverToCommander(r.Context(), created)
		// The sender's own event stream records the outgoing message too, so a
		// peek at that run can tell what it said, to whom, and when.
		s.appendCollabEvent(id.RunID, delivery, "out", delivery.Status)
		writeJSON(w, http.StatusAccepted, map[string]any{
			"message": delivery, "created": true, "delivery": delivery.Status,
		})
		return
	}
	targetID, ok := collab.IntentIDFromParticipant(req.To)
	if !ok {
		writeErr(w, http.StatusBadRequest, "不是可用的参与者 id："+req.To)
		return
	}
	target, found := s.Store.GetIntent(targetID)
	if !found {
		writeErr(w, http.StatusNotFound, "参与者不存在："+req.To)
		return
	}
	// Authorization comes before the target's state is reported: in allowlist
	// mode an unauthorised direction must not learn whether the target is live,
	// finished or on which generation. The commander branch above is exempt
	// (it is the operator's channel).
	if !s.peerAllowed(id.ParticipantID, req.To) {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error": "未授权：项目 peer 规则不允许从 " + id.ParticipantID + " 发给 " + req.To + "；让 operator 用 PUT /api/peers 授权",
			"code":  "peer_denied",
		})
		return
	}
	if req.ToGeneration != 0 && req.ToGeneration != target.Attempts {
		writeErr(w, http.StatusConflict,
			fmt.Sprintf("目标已换代（请求 %d，当前 %d）：旧 run 不能给新 holder 投递", req.ToGeneration, target.Attempts))
		return
	}
	// Refuse a finished direction instead of storing a message its holder will
	// never see; reopening is an explicit retry/reopen.
	switch target.Status {
	case model.StatusDone, model.StatusFailed, model.StatusReviewFailed, model.StatusParked:
		writeErr(w, http.StatusConflict, "目标方向已结束（"+target.Status+"）：先显式 retry/reopen，消息不能复活它")
		return
	}

	msg := model.Message{
		Durable:           req.Durable,
		ProjectID:         id.ProjectID,
		FromParticipantID: id.ParticipantID, FromRunID: id.RunID, FromGeneration: id.Generation,
		ToParticipantID: req.To, ToGeneration: target.Attempts,
		Kind: req.Kind, Text: req.Text, FactRefs: req.FactRefs,
		InReplyTo: req.InReplyTo, IdempotencyKey: req.IdempotencyKey,
		Status: model.MessageQueued,
	}
	created, isNew, err := s.Store.AddMessage(msg)
	if err != nil {
		if errors.Is(err, store.ErrIdempotencyConflict) {
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		if errors.Is(err, store.ErrMessageBackpressure) {
			s.writeBackpressure(w, req.To, err)
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !isNew {
		// A retry of an accepted message must not deliver twice.
		writeJSON(w, http.StatusOK, map[string]any{"message": created, "created": false, "delivery": created.Status})
		return
	}

	delivery := s.deliverMessage(r.Context(), target, created)
	s.appendCollabEvent(id.RunID, delivery, "out", delivery.Status)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"message": delivery, "created": true, "delivery": delivery.Status,
	})
}

func (s *Server) deliverMessage(ctx context.Context, target model.Intent, msg model.Message) model.Message {
	workDir := filepath.Join(s.CaseRoot, target.ID)
	sess, hasSession := aopexec.ReadSession(workDir)
	run, hasRun := s.Store.GetRun(target.RunID)
	live := target.Status == model.StatusRunning && hasRun && run.Status == model.RunRunning && hasSession
	if !live || (s.Say == nil && s.SayEvent == nil) {
		// Between turns or waiting to start: keep it queued. The next run can
		// read it, and nothing silently starts an untracked harness turn.
		return msg
	}
	claimed, acquired, claimErr := s.Store.BeginMessageDelivery(msg.ID)
	if claimErr != nil {
		log.Printf("消息 %s 投递准入失败：%v", msg.ID, claimErr)
		return msg
	}
	if !acquired {
		return claimed
	}
	var deliveryID string
	var sayErr error
	if claimed.Kind == model.MessageResult && len(claimed.Chain) > 0 && s.SayResult != nil {
		deliveryID, sayErr = s.SayResult(ctx, target, sess, formatPeerMessage(claimed), claimed.EventID, claimed.Chain)
	} else {
		deliveryID, sayErr = s.sayText(ctx, target, sess, formatPeerMessage(claimed), claimed.Interrupt, claimed.EventID)
	}
	errText := ""
	if sayErr != nil {
		errText = sayErr.Error()
	}
	final, uerr := s.Store.FinishMessageDelivery(msg.ID, deliveryID, target.RunID, "ioa", errText)
	if uerr != nil {
		log.Printf("消息 %s 投递结果写盘失败：%v", msg.ID, uerr)
		return claimed
	}
	// Audit the delivery in the recipient run's own event stream. The full body
	// stays in the message ledger; the journal gets the ids, direction and a
	// clipped excerpt, so peeking a run shows "it was interrupted by whom"
	// without turning the event log into a second mailbox (docs/17 §3.3).
	s.appendCollabEvent(target.RunID, final, "in", final.Status)
	return final
}

// sayText prefers the event-id carrying transport, falling back to the plain
// Say hook for tests and for hosts that predate the stable id.
func (s *Server) sayText(ctx context.Context, in model.Intent, sess aopexec.SessionInfo, text string, interrupt bool, eventID string) (string, error) {
	if s.SayEvent != nil {
		return s.SayEvent(ctx, in, sess, text, interrupt, eventID)
	}
	if s.Say == nil {
		return "", ErrSayUnavailable
	}
	return s.Say(ctx, in, sess, text, interrupt)
}

// formatPeerMessage is the text the receiving model sees. It carries the
// addressing/reply/fact metadata in a readable prefix; IOA itself only carries a
// session target, so without this the model would see an anonymous sentence.
func formatPeerMessage(m model.Message) string {
	if m.NoticeFactID != "" {
		// A live mechanical notice is a one-line pointer, not a chat turn. The
		// receiving model must read the fact itself; the notice carries no body
		// and no authorization.
		var b strings.Builder
		b.WriteString("[共享发现]")
		b.WriteString(" from=" + m.FromParticipantID)
		b.WriteString(" fact=" + m.NoticeFactID)
		if m.NoticeVersion > 0 {
			fmt.Fprintf(&b, " v=%d", m.NoticeVersion)
		}
		if m.Coalesced > 0 {
			fmt.Fprintf(&b, " 合并=%d", m.Coalesced)
		}
		b.WriteString(" message_id=" + m.ID)
		b.WriteString("\n")
		b.WriteString(m.Text)
		return b.String()
	}
	var b strings.Builder
	b.WriteString("[collab]")
	b.WriteString(" from=" + m.FromParticipantID)
	b.WriteString(" to=" + m.ToParticipantID)
	if m.Kind != "" {
		b.WriteString(" kind=" + m.Kind)
	}
	b.WriteString(" message_id=" + m.ID)
	if m.InReplyTo != "" {
		b.WriteString(" in_reply_to=" + m.InReplyTo)
	}
	if len(m.FactRefs) > 0 {
		b.WriteString(" facts=" + strings.Join(m.FactRefs, ","))
	}
	b.WriteString("\n")
	b.WriteString(m.Text)
	return b.String()
}

// listMessages is the operator's view of the project conversation. It is not
// participant-authenticated: the console token already gates /p/<id>/api.
func (s *Server) listMessages(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	writeJSON(w, http.StatusOK, map[string]any{"messages": messageFilters(s.Store.Messages(limit), r)})
}

func (s *Server) agentMessages(w http.ResponseWriter, r *http.Request) {
	id, ok := identityOf(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "缺少协作身份")
		return
	}
	after, _ := strconv.ParseUint(strings.TrimSpace(r.URL.Query().Get("after")), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	msgs := s.Store.MessagesForBox(id.ParticipantID, after, limit, strings.TrimSpace(r.URL.Query().Get("box")))
	var next uint64 = after
	if len(msgs) > 0 {
		next = msgs[len(msgs)-1].Seq
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": msgs, "next": next})
}

func (s *Server) agentAckMessage(w http.ResponseWriter, r *http.Request) {
	id, ok := identityOf(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "缺少协作身份")
		return
	}
	msgID := r.PathValue("id")
	msg, found := s.Store.MessageByID(msgID)
	if !found {
		writeErr(w, http.StatusNotFound, "消息不存在")
		return
	}
	if msg.ToParticipantID != id.ParticipantID {
		writeErr(w, http.StatusForbidden, "只能确认发给自己的消息")
		return
	}
	now := time.Now()
	updated, err := s.Store.UpdateMessage(msgID, func(m *model.Message) {
		m.Status = model.MessageConfirmed
		m.AckedAt = &now
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": updated})
}

// factView is the participant's summary shape: enough to decide whether to read
// the full fact. Doc and evidence bodies are the depth path, not the list.
type factView struct {
	EntityRef      string      `json:"entity_ref,omitempty"`
	SourceRunID    string      `json:"source_run_id,omitempty"`
	DeclarationRef string      `json:"declaration_ref,omitempty"`
	DeclarationID  string      `json:"declaration_id,omitempty"`
	ClaimVersion   uint64      `json:"claim_version"`
	ObservedAt     *time.Time  `json:"observed_at,omitempty"`
	ContextRef     string      `json:"execution_context_ref,omitempty"`
	ID             string      `json:"id"`
	IntentID       string      `json:"intent_id"`
	Kind           string      `json:"kind"`
	Title          string      `json:"title"`
	Summary        string      `json:"summary,omitempty"`
	Confidence     string      `json:"confidence,omitempty"`
	Origin         string      `json:"origin,omitempty"`
	Proof          string      `json:"proof,omitempty"`
	Status         string      `json:"status"`
	EvidenceIDs    []string    `json:"evidence_ids,omitempty"`
	Tags           []string    `json:"tags,omitempty"`
	Refs           []model.Ref `json:"refs,omitempty"`
	CreatedAt      time.Time   `json:"created_at"`
}

func (s *Server) agentFacts(w http.ResponseWriter, r *http.Request) {
	page, err := s.searchFacts(r)
	if err != nil {
		retrievalError(w, err)
		return
	}
	out := make([]factView, 0, len(page.Facts))
	for _, f := range page.Facts {
		out = append(out, toFactView(f))
	}
	writeJSON(w, 200, map[string]any{"facts": out, "next_cursor": page.NextCursor, "has_more": page.HasMore, "total": page.Total, "snapshot_version": page.Version, "search": page.Search, "matches": page.Matches})
}

func toFactView(f model.Fact) factView {
	return factView{
		EntityRef: f.EntityRef,
		ID:        f.ID, IntentID: f.IntentID, Kind: f.Kind, Title: f.Title, SourceRunID: f.SourceRunID, DeclarationRef: f.DeclarationRef,
		DeclarationID: f.DeclarationID, ClaimVersion: f.ClaimVersion, ObservedAt: f.ObservedAt, ContextRef: f.ExecutionContextRef,
		Summary: f.Summary, Confidence: f.Confidence, Origin: f.Origin,
		Proof: f.Proof, Status: f.Status, EvidenceIDs: f.EvidenceIDs,
		Tags: f.Tags, Refs: f.Refs, CreatedAt: f.CreatedAt,
	}
}

func (s *Server) agentFact(w http.ResponseWriter, r *http.Request) {
	f, version, ok := s.Store.FactVersion(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "事实不存在")
		return
	}
	want := make(map[string]bool, len(f.EvidenceIDs))
	for _, eid := range f.EvidenceIDs {
		want[eid] = true
	}
	evs := make([]model.Evidence, 0, len(want))
	for _, e := range s.Store.Evidence() {
		if want[e.ID] {
			evs = append(evs, e)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"fact": f, "version": version, "evidence": evs, "reviews": s.Store.Reviews(f.ID),
	})
}
