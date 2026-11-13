// Package aopexec runs an intent through a harness AOP host (docs/06, D43).
// It is now the only execution transport: the control plane's own machine is a
// host too, started and supervised by tw (D64).
//
// One run is one turn in a host session. The control plane supervises local
// hosts; the fork's WorkDir extension binds tools to the case directory. The
// ledger and host share a filesystem, including skill assets and run output.
package aopexec

import (
	"context"
	"errors"
	"fmt"
	"github.com/kksty/tianwu/internal/aop/operation"
	"github.com/kksty/tianwu/internal/aop/tool"
	"github.com/kksty/tianwu/internal/aop/traffic"
	"os"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/kksty/tianwu/internal/aop"
	pb "github.com/kksty/tianwu/internal/aop/pb"
	"github.com/kksty/tianwu/internal/executor"
)

// Executor is the AOP form of executor.Executor.
type Executor struct {
	// BaseURL is the host, e.g. http://10.0.0.5:18766.
	BaseURL string
	// Token is the host's access key (Bearer).
	Token string
	// Agent pins sessions to one agent node id inside the host. "" lets the
	// host pick, which is only right when it has exactly one agent.
	Agent string
	// Timeout bounds one turn: how long the host may work before the control
	// plane gives up on this session's turn.
	Timeout time.Duration
}

func (e *Executor) Name() string { return "aop(" + e.BaseURL + ")" }

// Run opens (or resumes) a session, runs one turn and streams the host's events.
func (e *Executor) Run(ctx context.Context, req executor.Request, emit func(executor.Event)) (executor.Result, error) {
	if strings.TrimSpace(e.BaseURL) == "" {
		return executor.Result{ExitCode: -1}, errors.New("AOP 节点缺地址：在 nodes.json 里写 \"aop\": \"http://host:port\"")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if e.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, e.Timeout)
		defer cancel()
	}

	client := aop.New(e.BaseURL, e.Token)
	conn, err := client.Dial(ctx)
	if err != nil {
		return executor.Result{ExitCode: -1}, err
	}
	defer conn.Close()

	sess, continued, err := e.session(ctx, conn, req)
	if err != nil {
		return executor.Result{ExitCode: -1}, err
	}

	// A verifier / report run is one shot: it deliberately gets no resume
	// marker, so nothing will ever continue or close its session. Close it
	// here, on success and on failure, or every review/report leaves a session
	// behind on the host. The commander is the opposite: D78 runs it as a long
	// session across rounds, so its session must stay open (line 186 writes the
	// resume marker for it).
	if req.Purpose != "" && req.Purpose != "commander" {
		defer func() {
			cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = conn.CloseSession(cctx, sess.GetId(), "tianwu: "+req.Purpose+" 一次性 run 结束")
		}()
	}

	// closeUnstartedSession tears down a session this attempt opened when no
	// turn was ever submitted. A fresh session with no turn has no trajectory
	// worth keeping, and the commander writes no resume marker for it, so
	// nothing would ever close it: every rejected or interrupted first submit
	// leaked an open session on the host. A continued session is left alone —
	// its marker still points at a trajectory the operator may resume.
	closeUnstartedSession := func(why string) {
		if continued {
			return
		}
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = conn.CloseSession(cctx, sess.GetId(), "tianwu: "+why)
		// The pointer session() wrote must not outlive the session it names.
		_ = os.Remove(markerPath(req.WorkDir))
	}

	// The Tianwu harness fork makes the case directory the session's workspace
	// (D110), so file and shell tools already resolve relative paths there. The
	// prompt still states it: a model writes board dumps, screenshots and proofs
	// and must know where they belong. The case must remain below the agent's
	// configured directory, because the tools' os.Root keeps that boundary.
	//
	// Explicit skill files keep their own assets and relative references. Pass
	// file references, not a second implementation of harness skill expansion.
	// A resumed turn already carries the initial selection in its context.
	skills := ""
	if !req.Resume {
		var skillErr error
		skills, skillErr = skillPrompt(req.Skills)
		if skillErr != nil {
			closeUnstartedSession("技能读不到")
			return executor.Result{ExitCode: -1}, skillErr
		}
	}
	host, gotHost := e.agentWorkdir(ctx, client, sess.GetNodeId())
	fullPrompt, promptErr := promptFor(req.Prompt+skills, req.WorkDir, host)
	if promptErr != nil {
		closeUnstartedSession("工作目录不在宿主里")
		return executor.Result{ExitCode: -1}, promptErr
	}

	// The harness evaluator uses the *turn input* as its Goal. A full executor
	// brief there becomes "## Goal: you are an executor…", which lets the model
	// answer with a bash call instead of verdict. Until the harness carries a
	// separate goal, keep the transport honest: put the brief in the case and
	// send the short control-plane goal as the turn input. The evaluator only
	// needs the goal and the Eval criterion; the executor reads the brief.
	prompt := fullPrompt
	if !req.Resume && strings.TrimSpace(req.Goal) != "" && strings.TrimSpace(req.Eval) != "" {
		if briefPath, err := writeBrief(req.WorkDir, fullPrompt); err == nil {
			short := "本轮目标：" + strings.TrimSpace(req.Goal) +
				"。先读取 `" + briefPath + "`，按其中完整意图、约束与交接契约执行；不读取就执行视为本轮无效。"
			if p, err := promptFor(short, req.WorkDir, host); err == nil {
				prompt = p
			}
		}
	}

	// Watch before running: a host may publish the turn's first events before
	// the RunTurn reply is written back (measured on the wire). A continued turn
	// asks for the tail only: without a cursor the host replays the whole
	// session and the turn filter has to discard it event by event.
	cursor := ""
	if req.Resume {
		if m, ok := readMarker(req.WorkDir); ok {
			cursor = m.Cursor
		}
	}
	if err := conn.Watch(ctx, sess.GetId(), cursor); err != nil {
		closeUnstartedSession("订阅事件失败")
		return executor.Result{ExitCode: -1}, err
	}

	turnID := fmt.Sprintf("turn-%d", time.Now().UnixNano())
	// The wire field is a uint32: a negative int would arrive as 2^32-1 turns.
	// The API rejects such input; this is the last boundary before the cast.
	maxTurns := req.MaxTurns
	if maxTurns < 0 {
		maxTurns = 0
	}
	if maxTurns > executor.MaxTurnsCap {
		maxTurns = executor.MaxTurnsCap
	}
	receipt, err := conn.RunTurnOpts(ctx, sess.GetId(), turnID, prompt, aop.TurnOptions{
		Continue: continued,
		MaxTurns: uint32(maxTurns),
		Eval:     req.Eval,
	})
	if err != nil {
		if continued && strings.Contains(err.Error(), "NOT_FOUND") {
			// The host does not know this session any more (restarted, or its
			// DB was replaced). Dropping the pointer turns the next attempt into
			// an honest "没有可续的会话" instead of the same error forever.
			// ResumeGone is the structured half: dispatch falls back to a fresh
			// session once, and says so (it is not a silent retry).
			_ = os.Remove(markerPath(req.WorkDir))
			return executor.Result{ExitCode: -1, ResumeGone: true}, err
		}
		// The first turn was not accepted: this attempt opened the session and
		// has to close it again. A resumed session keeps its trajectory and
		// marker — a rejected submission there is a failed turn, not a reason to
		// throw the history away.
		closeUnstartedSession("首轮提交失败")
		return executor.Result{ExitCode: -1}, err
	}
	if id := receipt.GetTurnId(); id != "" {
		turnID = id
	}
	// Some hosts omit the redundant session_id on individual events. The
	// accepted turn already has an authoritative session identity, also on
	// resume where no new session_started event is emitted.
	publish := emit
	emit = func(ev executor.Event) {
		if ev.SessionID == "" {
			ev.SessionID = sess.GetId()
		}
		publish(ev)
	}
	// A resume pointer is written for an intent run and for the commander
	// session: both continue the same AOP session on the next turn (D42, D78).
	// A verifier or report work dir becomes a deliverable (or at least is read
	// by it), and a marker there would be junk in what the operator ships.
	if req.Purpose == "" || req.Purpose == "commander" {
		_ = writeMarker(req.WorkDir, marker{URL: e.BaseURL, Agent: sess.GetNodeId(), Session: sess.GetId(), Turn: turnID})
		// Record where the stream got to; the next steer can then start after
		// this cursor instead of replaying the session from zero.
		defer func() { updateMarkerCursor(req.WorkDir, conn.Cursor()) }()
	}

	res := executor.Result{}
	if !gotHost {
		// Not fatal: the session workspace extension still names the case; this
		// only says the host cannot help verify where its agent resolves paths.
		res.Output = "宿主没有上报 agent 的工作目录（本轮按绝对工作目录下发）"
	}
	var deltas strings.Builder
	var lastMessage string

	finishTurn := func(ev *pb.Event) {
		// A final assistant message normally arrives as its own event; when the
		// provider only streamed deltas, synthesise the one message the ledger
		// would otherwise never see (it is the run's own account of what it did).
		if lastMessage == "" && deltas.Len() > 0 {
			msg := executor.Event{Kind: executor.KindMessage, Role: "assistant", Text: truncate(deltas.String(), 400), At: time.Now()}
			res.Events++
			absorb(&res, msg)
			emit(msg)
		}
		end := event(ev)
		res.Events++
		absorb(&res, end)
		emit(end)
	}

	// ours reports whether an event belongs to *this* turn.
	//
	// WatchEvents without a cursor replays the whole session, so a continued
	// turn first receives the previous turn's history — including its
	// turn_ended. Without this filter a steered run "finishes" one second into
	// its own turn, with the old turn's stop reason (measured: the second run
	// ended `canceled` while the agent had not even started).
	ours := func(ev *pb.Event) bool {
		return belongsToTurn(ev, sess.GetId(), turnID)
	}

	for {
		select {
		case <-ctx.Done():
			// The host's stream still ends with turn_ended, but the turn is over
			// for us either way: cancelling is a request, not a wait.
			stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = conn.CancelTurn(stopCtx, sess.GetId(), turnID, "tianwu: 取消")
			cancel()
			drain(conn, ours, 2*time.Second, func(ev *pb.Event) {
				res.Events++
				absorb(&res, event(ev))
				emit(event(ev))
			})
			if res.StopReason == "" {
				res.StopReason = "canceled"
			}
			res.Output = droppedNote(conn, res.Output)
			return res, ctx.Err()

		case ev, ok := <-conn.Events():
			if !ok {
				res.Output = droppedNote(conn, res.Output)
				return res, errors.New("aop: 回合结束前连接断开（宿主的 AOP 通道断了）")
			}
			if !ours(ev) {
				continue
			}
			switch p := ev.GetPayload().(type) {
			case *pb.Event_MessageDelta:
				deltas.WriteString(p.MessageDelta.GetText())
				continue
			case *pb.Event_Message:
				if p.Message.GetRole() == "assistant" {
					if t := contentText(p.Message.GetContent()); t != "" {
						lastMessage = t
					}
				}
			case *pb.Event_TurnEnded:
				finishTurn(ev)
				res.Output = droppedNote(conn, res.Output)
				return res, nil
			}
			if ev.GetError() != nil {
				// An error event is part of the turn, not the end of the
				// stream: the turn still terminates with turn_ended, and the
				// result must carry both.
				res.Events++
				absorb(&res, event(ev))
				emit(event(ev))
				continue
			}
			res.Events++
			absorb(&res, event(ev))
			emit(event(ev))
		}
	}
}

func belongsToTurn(ev *pb.Event, sessionID, turnID string) bool {
	// Never infer traffic ownership from the session currently being watched.
	// Background traffic from an earlier turn must not be rebound to its successor.
	if ext := ev.GetExtension(); ext != nil && ext.MessageIs(new(traffic.Flow)) {
		return ev.GetSessionId() == sessionID && ev.GetTurnId() == turnID && sessionID != "" && turnID != ""
	}
	return (ev.GetSessionId() == "" || ev.GetSessionId() == sessionID) && (ev.GetTurnId() == "" || ev.GetTurnId() == turnID)
}

// session opens a fresh session, or reuses the case's previous one when the
// caller asked for a continuation (D42's steer, mapped onto the protocol's
// continue_session). Reuse is refused when the marker belongs to another host:
// silently opening a new session instead would look like a successful
// continuation while the context is gone.
func (e *Executor) session(ctx context.Context, conn *aop.Conn, req executor.Request) (*pb.Session, bool, error) {
	if req.Resume {
		m, ok := readMarker(req.WorkDir)
		if !ok {
			return nil, false, ErrNoSession
		}
		if m.URL != e.BaseURL {
			return nil, false, fmt.Errorf("这个 case 的 AOP 会话属于 %s，不是 %s", m.URL, e.BaseURL)
		}
		if e.Agent != "" && m.Agent != "" && m.Agent != e.Agent {
			return nil, false, fmt.Errorf("这个 case 的 AOP 会话跑在 agent %q 上，不是 %q", m.Agent, e.Agent)
		}
		node := m.Agent
		if node == "" {
			node = e.Agent
		}
		return &pb.Session{Id: m.Session, NodeId: node, State: "open"}, true, nil
	}

	// A fresh dispatch starts a new session. Closing the old one is courtesy
	// to the host: otherwise every re-dispatch leaves a session behind that
	// nobody will ever continue.
	if old, ok := readMarker(req.WorkDir); ok && old.URL == e.BaseURL {
		_ = conn.CloseSession(ctx, old.Session, "tianwu: 重派")
	}
	title := "tianwu"
	if req.IntentID != "" {
		title = "tianwu: " + req.IntentID
	}
	// The session owns its workspace: tools resolve relative paths there instead
	// of the agent process directory (<data>), so a run that writes
	// `screenshot.png` or `board_end.json` without a prefix lands in its own
	// case/commander directory (D110). The fork reads this extension.
	workDir := req.WorkDir
	if abs, err := filepath.Abs(workDir); err == nil {
		workDir = abs
	}
	sess, err := conn.OpenSession(ctx, e.Agent, title, workDir)
	if err != nil {
		return nil, false, err
	}
	if req.Purpose == "" {
		if err := writeMarker(req.WorkDir, marker{URL: e.BaseURL, Agent: sess.GetNodeId(), Session: sess.GetId()}); err != nil {
			// A run without its marker can still finish; only a later steer
			// loses its target, so this is not worth failing the turn.
			_ = err
		}
	}
	return sess, false, nil
}

// agentWorkdir asks the host where the session's agent resolves paths.
//
// It is one extra HTTP call per run on purpose: a cached value goes stale the
// moment the operator restarts the host from another directory, and a wrong
// answer here means evidence written where the ledger never looks.
func (e *Executor) agentWorkdir(ctx context.Context, c *aop.Client, nodeID string) (string, bool) {
	if nodeID == "" {
		nodeID = e.Agent
	}
	if nodeID == "" {
		return "", false
	}
	agents, err := c.Agents(ctx)
	if err != nil {
		return "", false
	}
	a, ok := aop.Find(agents, nodeID)
	if !ok || strings.TrimSpace(a.WorkingDir) == "" {
		return "", false
	}
	return a.WorkingDir, true
}

// ErrNoSession means the case has no AOP session to continue.
var ErrNoSession = errors.New("要续话，但这个 case 没有 AOP 会话（.tw/aop-session.json）")

// skillPrompt references explicitly selected files. AOP has no per-turn -s
// field; discoverable skills use the harness's /skill:name input instead.
// Do not inline their bodies: that loses the skill's base directory and
// duplicates the harness's formatting/asset handling.
func skillPrompt(paths []string) (string, error) {
	if len(paths) == 0 {
		return "", nil
	}
	var b strings.Builder
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		info, err := os.Stat(p)
		if err != nil {
			return "", fmt.Errorf("AOP 节点读不到技能 %s：%w", p, err)
		}
		if !info.Mode().IsRegular() || info.Size() == 0 {
			return "", fmt.Errorf("技能文件为空或不是普通文件：%s", p)
		}
		fmt.Fprintf(&b, "\n\n本轮选定的技能文件：`%s`。执行前读取完整契约，附属资源相对该文件所在目录解析。", p)
	}
	return b.String(), nil
}

// promptFor appends the session workspace without displacing a native
// /skill:name invocation, which the harness expands only at input start.
//
// Since the Tianwu fork makes the case directory the session workspace (D110),
// the tools and shell already start there; the prompt states it anyway, because
// the model writes board dumps, screenshots and proofs and must know where they
// belong. The absolute path is context, not shell instructions. The host-dir
// sanity check stays: a case the
// agent cannot see is a deployment error, not a run that silently drops proof.
func promptFor(prompt, workDir, hostDir string) (string, error) {
	if strings.TrimSpace(workDir) == "" {
		return prompt, nil
	}
	abs := workDir
	if a, err := filepath.Abs(workDir); err == nil {
		abs = a
	}

	var b strings.Builder
	b.WriteString(prompt)
	b.WriteString("\n\n## 工作目录\n")
	if strings.TrimSpace(hostDir) == "" {
		b.WriteString("本轮的工作目录是 `" + abs + "`。\n")
		b.WriteString("本轮文件与交接产物保存在此目录；工具的相对路径由会话工作目录解析。\n")
		return b.String(), nil
	}

	rel, err := filepath.Rel(hostDir, abs)
	if err != nil {
		return "", fmt.Errorf("宿主工作目录 %s 与本轮 case %s 不在同一个卷上：AOP 节点要求 case 在宿主的工作目录里面", hostDir, abs)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("宿主的工作目录是 %s，不包含本轮 case %s：把宿主起在实例根（tw 的 --data 目录）上，或给这个意图换一个节点", hostDir, abs)
	}
	rel = filepath.ToSlash(rel)
	b.WriteString("本轮的工作目录是 `" + abs + "`（宿主进程目录 `" + hostDir + "` 下的 `" + rel + "`）。\n")
	b.WriteString("本轮文件与交接产物保存在此目录；工具的相对路径由会话工作目录解析。\n")
	return b.String(), nil
}

// writeBrief keeps the executor's full brief beside the case and returns a
// session-relative reference accepted by the native file service.
func writeBrief(workDir, prompt string) (string, error) {
	if strings.TrimSpace(workDir) == "" {
		return "", errors.New("no work dir")
	}
	path := filepath.Join(workDir, ".tw", "brief.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(prompt), 0o644); err != nil {
		return "", err
	}
	return ".tw/brief.md", nil
}

// drain reads what the host still sends after a cancel, bounded in time.
func drain(conn *aop.Conn, ours func(*pb.Event) bool, d time.Duration, emit func(*pb.Event)) {
	deadline := time.After(d)
	for {
		select {
		case ev, ok := <-conn.Events():
			if !ok {
				return
			}
			if !ours(ev) {
				continue
			}
			emit(ev)
			if ev.GetTurnEnded() != nil {
				return
			}
		case <-deadline:
			return
		}
	}
}

func droppedNote(conn *aop.Conn, out string) string {
	if n := conn.Dropped(); n > 0 {
		note := fmt.Sprintf("AOP 通道丢了 %d 个事件（消费太慢）", n)
		if out == "" {
			return note
		}
		return out + "；" + note
	}
	return out
}

// absorb takes the run-level facts from the stream, not from any exit status.
func absorb(res *executor.Result, ev executor.Event) {
	if ev.ContextWindow > 0 {
		res.ContextWindow = ev.ContextWindow
	}
	if ev.ContextTokens > 0 {
		res.FinalContextTokens = ev.ContextTokens
		if ev.ContextTokens > res.ContextTokens {
			res.ContextTokens = ev.ContextTokens
		}
	}
	switch ev.Kind {
	case executor.KindTurnEnd:
		res.StopReason = ev.StopReason
		res.IsError = !ev.OK
		// A final total replaces the per-call sum; an absent usage payload
		// must not erase calls already observed before cancellation.
		if ev.UsageRequests > 0 || ev.Tokens > 0 || ev.InputTokens > 0 || ev.OutputTokens > 0 || ev.CachedInputTokens > 0 || ev.CacheWriteTokens > 0 {
			res.Tokens = ev.Tokens
			res.InputTokens, res.OutputTokens = ev.InputTokens, ev.OutputTokens
			res.CachedInputTokens, res.CacheWriteTokens = ev.CachedInputTokens, ev.CacheWriteTokens
		}
		if ev.UsageRequests > 0 {
			// turn_ended is authoritative when it carries the request counters.
			res.UsageRequests, res.UsageMissing = ev.UsageRequests, ev.UsageMissing
			// Usage is known only if at least one model call actually reported
			// it; an all-missing turn must stay unknown, not cost 0.
			res.UsageKnown = ev.UsageMissing < ev.UsageRequests
		} else if ev.Tokens > 0 || ev.InputTokens > 0 || ev.OutputTokens > 0 {
			// Some harness builds report only the token totals on turn_ended;
			// their detail has cache_* but no request counters. The totals are
			// real provider usage, so the run is not "usage missing"; the call
			// count stays whatever the per-call usage events accumulated.
			res.UsageKnown = true
		}
	case executor.KindUsage:
		// Usage events arrive per model call; turn_ended carries the
		// authoritative turn total and overwrites these. Until then, keep the
		// running breakdown and count the calls we actually saw.
		res.Tokens += ev.Tokens
		res.InputTokens += ev.InputTokens
		res.OutputTokens += ev.OutputTokens
		res.CachedInputTokens += ev.CachedInputTokens
		res.CacheWriteTokens += ev.CacheWriteTokens
		res.UsageRequests += ev.UsageRequests
		res.UsageMissing += ev.UsageMissing
		res.UsageKnown = res.UsageKnown || ev.UsageMissing < ev.UsageRequests || ev.Tokens > 0 || ev.InputTokens > 0 || ev.OutputTokens > 0
	case executor.KindError:
		res.IsError = true
		if res.StopReason == "" {
			res.StopReason = "error"
		}
	case executor.KindStatus:
		if ev.State != "" {
			res.LastState = ev.State
			if executor.IsCompactionState(ev.State) {
				res.Compacted = true
				// The pre-compaction size is no longer the session's size; a
				// later usage event, if any, reports the compacted one.
				res.FinalContextTokens = 0
			}
		}
	case executor.KindMessage:
		if ev.Role == "assistant" && ev.Text != "" {
			res.Text = ev.Text
		}
	}
}

// event translates one AOP event into the ledger's event shape. The kinds are
// the payload names, so the mapping is a switch and never a guess.
func event(ev *pb.Event) executor.Event {
	out := executor.Event{Kind: executor.KindRaw, At: time.Now(), SessionID: ev.GetSessionId(), Raw: rawJSON(ev)}
	if ext := ev.GetExtension(); ext != nil && ext.MessageIs(new(traffic.Flow)) {
		value := new(traffic.Flow)
		if err := ext.UnmarshalTo(value); err != nil {
			out.Kind, out.Text = executor.KindError, "HTTP 观察事件解码失败："+err.Error()
		} else {
			out.Kind, out.NativeFlow, out.NativeEventID = "http", value, ev.GetId()
			if value.Timestamp != nil {
				out.At = value.Timestamp.AsTime()
			}
			out.Text = truncate(value.GetRequest().GetMethod()+" "+value.GetRequest().GetUrl(), 400)
			for _, sidecar := range ev.Extensions {
				if sidecar.MessageIs(new(operation.Ref)) {
					ref := new(operation.Ref)
					if sidecar.UnmarshalTo(ref) == nil {
						out.NativeOperation = ref
					}
				}
			}
		}
		return out
	}
	if ext := ev.GetExtension(); ext != nil && ext.MessageIs(new(tool.Artifact)) {
		value := new(tool.Artifact)
		if err := ext.UnmarshalTo(value); err != nil {
			out.Kind, out.Text = executor.KindError, "原生结果解码失败："+err.Error()
		} else {
			out.Kind, out.Tool, out.Text = "artifact", value.Tool, value.Kind
			out.Native, out.NativeEventID = value, ev.GetId()
		}
		return out
	}
	if ts := ev.GetEmittedAt(); ts != nil {
		out.At = ts.AsTime()
	}
	switch p := ev.GetPayload().(type) {
	case *pb.Event_SessionStarted:
		out.Kind = executor.KindSession
		out.Text = truncate(p.SessionStarted.GetModel(), 400)
	case *pb.Event_SessionEnded:
		out.Kind = executor.KindSessionEnd
		out.Text = truncate(p.SessionEnded.GetReason(), 400)
	case *pb.Event_TurnStarted:
		out.Kind = executor.KindTurnStart
	case *pb.Event_TurnEnded:
		te := p.TurnEnded
		out.Kind = executor.KindTurnEnd
		out.StopReason = te.GetStopReason()
		out.OK = te.GetError() == nil
		fillUsage(&out, te.GetUsage())
		out.ContextTokens = int(te.GetContextTokens())
		if out.ContextTokens == 0 {
			out.ContextTokens = int(te.GetUsage().GetDetail()["context_tokens"])
		}
		out.ContextWindow = int(te.GetUsage().GetDetail()["context_window"])
		out.Text = truncate(out.StopReason, 400)
		if e := te.GetError(); e != nil {
			out.Text = truncate(out.StopReason+" "+e.GetCode()+" "+e.GetMessage(), 400)
		}
	case *pb.Event_Message:
		out.Kind, out.Role = executor.KindMessage, p.Message.GetRole()
		out.Text = truncate(contentText(p.Message.GetContent()), 400)
	case *pb.Event_ToolCall:
		out.Kind = executor.KindToolCall
		out.Tool = p.ToolCall.GetName()
		// The call id is what lets the console pair a result with its call when
		// tool calls and results interleave (parallel tools); it is recorded
		// verbatim, not reconstructed from names or order.
		out.CallID = p.ToolCall.GetId()
		out.Text = truncate(strings.TrimSpace(p.ToolCall.GetName()+" "+p.ToolCall.GetKind()), 400)
	case *pb.Event_ToolResult:
		out.Kind = executor.KindToolResult
		out.Tool = p.ToolResult.GetName()
		out.OK = !p.ToolResult.GetIsError()
		// DurationMs is the harness's own execution measurement for the tool.
		// It survives into the journal so the console can show elapsed time
		// without deriving it from receipt timestamps (which include transport
		// latency and are unusable for short calls).
		out.DurationMs = int(p.ToolResult.GetDurationMs())
		out.CallID = p.ToolResult.GetCallId()
		out.Text = truncate(contentText(p.ToolResult.GetOutput()), 400)
	case *pb.Event_Usage:
		out.Kind = executor.KindUsage
		fillUsage(&out, p.Usage)
		out.ContextTokens = int(p.Usage.GetDetail()["context_tokens"])
		out.ContextWindow = int(p.Usage.GetDetail()["context_window"])
	case *pb.Event_Error:
		out.Kind = executor.KindError
		out.Text = truncate(strings.TrimSpace(p.Error.GetCode()+" "+p.Error.GetMessage()), 400)
	case *pb.Event_Status:
		out.Kind = executor.KindStatus
		out.State = truncate(p.Status.GetState(), 400)
		out.Text = out.State
	}
	return out
}

// fillUsage copies the provider token breakdown. detail keys are the harness's
// own vocabulary (cache_read/cache_write/requests/usage_missing); an absent key
// stays 0 and the consumer must not treat it as a measured zero.
func fillUsage(out *executor.Event, u *pb.TokenUsage) {
	if u == nil {
		return
	}
	out.Tokens = int(u.GetTotalTokens())
	out.InputTokens = int(u.GetInputTokens())
	out.OutputTokens = int(u.GetOutputTokens())
	out.CachedInputTokens = int(u.GetDetail()["cache_read"])
	out.CacheWriteTokens = int(u.GetDetail()["cache_write"])
	out.UsageRequests = int(u.GetDetail()["requests"])
	out.UsageMissing = int(u.GetDetail()["usage_missing"])
}

// rawJSON records the event as protojson with proto field names — the same
// encoding `--output-format stream-json` writes, so a run log reads the same
// whichever transport produced it.
//
// Event.extensions carry protocol payloads this client has no descriptor for
// (`cyber.chat.SessionHistory`, `cyber.agent.LLMRequestDetail`, …); protojson
// refuses to marshal an Any it cannot resolve, and dropping them is the right
// trade: they are host-internal, and one of them is a provider request dump.
func rawJSON(ev *pb.Event) []byte {
	clone := proto.CloneOf(ev)
	clone.Extensions = nil
	// Unknown extension payloads can include provider dumps. Known scanner
	// artifacts are captured through Native, not duplicated in the event log.
	if clone.GetExtension() != nil {
		clone.Payload = nil
	}
	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(clone)
	if err != nil {
		return nil
	}
	return raw
}

func contentText(items []*pb.Content) string {
	var b strings.Builder
	for _, item := range items {
		if item == nil {
			continue
		}
		if t := item.GetText(); t != nil {
			b.WriteString(t.GetText())
		}
	}
	return b.String()
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
