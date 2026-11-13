package aopexec

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/kksty/tianwu/internal/aop"
	pb "github.com/kksty/tianwu/internal/aop/pb"
	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/ws"
)

// fakeHost is the smallest AOP host that behaves like the real one on the wire:
// requests answered with reply_to, events delivered under the subscription id,
// and a turn that ends with turn_ended.
type fakeHost struct {
	mu       sync.Mutex
	opened   int
	sessions map[string]bool
	prompts  []string
	cont     []bool
	titles   []string
	cancels  int
	closes   int
	// workDirs records the working directory each OpenSessionRequest carried
	// (the Tianwu fork's session workspace extension, D110).
	workDirs []string
	// watchCursors records the after_cursor of each WatchEvents request; the
	// second run must not replay from zero.
	watchCursors []string
	// hold parks the turn until a cancel arrives (the cancellation test).
	hold bool
	// rejectTurn makes RunTurn answer with a ProtocolError.
	rejectTurn bool
	// hostDir is what ListAgents reports as the agent's working directory; ""
	// means this fake host does not report one.
	hostDir string
	// replay makes a continued turn arrive after the previous turn's history,
	// the way an un-cursored WatchEvents subscription does.
	replay bool
}

func (h *fakeHost) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cyber.rpc.agent.AgentService/ListAgents" {
			if h.hostDir == "" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"agents": []any{map[string]any{
				"hello": map[string]any{
					"nodeId":  "local",
					"runtime": map[string]any{"workingDir": h.hostDir, "os": "test"},
				},
			}}})
			return
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		conn, err := ws.Upgrade(w, r)
		if err != nil {
			return
		}
		defer conn.Close()
		var sendMu sync.Mutex
		send := func(replyTo string, msg *pb.ProtocolMessage) {
			payload, err := anypb.New(msg)
			if err != nil {
				t.Errorf("pack: %v", err)
				return
			}
			env := &pb.Envelope{Id: "r", ReplyTo: replyTo, Payload: payload}
			if msg.GetEvent() != nil {
				env.DeliveryCursor = "cursor-1"
			}
			raw, err := proto.Marshal(env)
			if err != nil {
				t.Errorf("marshal: %v", err)
				return
			}
			sendMu.Lock()
			defer sendMu.Unlock()
			_ = conn.WriteBinary(raw)
		}
		event := func(sub string, ev *pb.Event) {
			send(sub, &pb.ProtocolMessage{Message: &pb.ProtocolMessage_Event{Event: ev}})
		}
		watchID, sessionID := "", ""
		canceled := make(chan struct{})
		for {
			raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var env pb.Envelope
			if proto.Unmarshal(raw, &env) != nil {
				continue
			}
			var msg pb.ProtocolMessage
			if env.Payload == nil || env.Payload.UnmarshalTo(&msg) != nil {
				continue
			}
			switch m := msg.Message.(type) {
			case *pb.ProtocolMessage_OpenSessionRequest:
				h.mu.Lock()
				h.opened++
				h.titles = append(h.titles, m.OpenSessionRequest.GetTitle())
				for _, extension := range m.OpenSessionRequest.GetExtensions() {
					if extension.GetTypeUrl() != aop.WorkDirExtensionURL {
						continue
					}
					var payload struct {
						WorkDir string `json:"work_dir"`
					}
					if json.Unmarshal(extension.GetValue(), &payload) == nil {
						h.workDirs = append(h.workDirs, payload.WorkDir)
					}
				}
				sessionID = "s" + time.Now().Format("150405.000000")
				h.sessions[sessionID] = true
				h.mu.Unlock()
				send(env.Id, &pb.ProtocolMessage{Message: &pb.ProtocolMessage_OpenSessionResponse{
					OpenSessionResponse: &pb.OpenSessionResponse{Outcome: &pb.OpenSessionResponse_Accepted{
						Accepted: &pb.Session{Id: sessionID, State: "open", NodeId: m.OpenSessionRequest.GetNodeId()},
					}},
				}})
			case *pb.ProtocolMessage_WatchEventsRequest:
				watchID = env.Id
				h.mu.Lock()
				h.watchCursors = append(h.watchCursors, m.WatchEventsRequest.GetAfterCursor())
				h.mu.Unlock()
			case *pb.ProtocolMessage_RunTurnRequest:
				req := m.RunTurnRequest
				h.mu.Lock()
				h.prompts = append(h.prompts, contentText(req.GetInput().GetContent()))
				h.cont = append(h.cont, req.GetContinueSession())
				known := h.sessions[req.GetSessionId()]
				h.mu.Unlock()
				if req.GetContinueSession() && !known {
					send(env.Id, &pb.ProtocolMessage{Message: &pb.ProtocolMessage_ProtocolError{
						ProtocolError: &pb.ProtocolError{Code: "NOT_FOUND", Message: "session not found"},
					}})
					continue
				}
				if h.rejectTurn {
					send(env.Id, &pb.ProtocolMessage{Message: &pb.ProtocolMessage_ProtocolError{
						ProtocolError: &pb.ProtocolError{Code: "UNAVAILABLE", Message: "node is not connected"},
					}})
					continue
				}
				send(env.Id, &pb.ProtocolMessage{Message: &pb.ProtocolMessage_RunTurnResponse{
					RunTurnResponse: &pb.RunTurnResponse{Outcome: &pb.RunTurnResponse_Accepted{
						Accepted: &pb.TurnReceipt{SessionId: req.GetSessionId(), TurnId: req.GetTurnId(), State: "running"},
					}},
				}})
				go func(sub, sid, tid string) {
					if h.replay {
						// WatchEvents without a cursor replays the session: the
						// previous turn's events arrive before ours, and its
						// turn_ended must not end the new run.
						event(sub, &pb.Event{SessionId: sid, TurnId: "turn-old", Payload: &pb.Event_Usage{Usage: &pb.TokenUsage{TotalTokens: 20015}}})
						event(sub, &pb.Event{SessionId: sid, TurnId: "turn-old", Payload: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{
							StopReason: "canceled", Error: &pb.ProtocolError{Code: "canceled", Message: "context canceled"},
						}}})
					}
					event(sub, &pb.Event{SessionId: sid, TurnId: tid, Payload: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}})
					if h.hold {
						select {
						case <-canceled:
						case <-time.After(10 * time.Second):
						}
						event(sub, &pb.Event{SessionId: sid, TurnId: tid, Payload: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{StopReason: "canceled"}}})
						return
					}
					event(sub, &pb.Event{SessionId: sid, TurnId: tid, Payload: &pb.Event_MessageDelta{MessageDelta: &pb.MessageDelta{Value: &pb.MessageDelta_Text{Text: "KX-"}}}})
					event(sub, &pb.Event{SessionId: sid, TurnId: tid, Payload: &pb.Event_MessageDelta{MessageDelta: &pb.MessageDelta{Value: &pb.MessageDelta_Text{Text: "8842"}}}})
					event(sub, &pb.Event{SessionId: sid, TurnId: tid, Payload: &pb.Event_Message{Message: &pb.Message{
						Role:    "assistant",
						Content: []*pb.Content{{Value: &pb.Content_Text{Text: &pb.TextContent{Text: "found KX-8842"}}}},
					}}})
					event(sub, &pb.Event{SessionId: sid, TurnId: tid, Payload: &pb.Event_ToolCall{ToolCall: &pb.ToolCall{Name: "bash", Kind: "bash"}}})
					event(sub, &pb.Event{SessionId: sid, TurnId: tid, Payload: &pb.Event_ToolResult{ToolResult: &pb.ToolResult{
						Name: "bash", IsError: false,
						Output: []*pb.Content{{Value: &pb.Content_Text{Text: &pb.TextContent{Text: "ok"}}}},
					}}})
					event(sub, &pb.Event{SessionId: sid, TurnId: tid, Payload: &pb.Event_Usage{Usage: &pb.TokenUsage{TotalTokens: 12}}})
					event(sub, &pb.Event{SessionId: sid, TurnId: tid, Payload: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{
						StopReason: "completed",
						Usage:      &pb.TokenUsage{TotalTokens: 5715},
					}}})
				}(watchID, req.GetSessionId(), req.GetTurnId())
			case *pb.ProtocolMessage_CancelTurnRequest:
				h.mu.Lock()
				h.cancels++
				h.mu.Unlock()
				send(env.Id, &pb.ProtocolMessage{Message: &pb.ProtocolMessage_CancelTurnResponse{
					CancelTurnResponse: &pb.CancelTurnResponse{Outcome: &pb.CancelTurnResponse_Accepted{
						Accepted: &pb.TurnReceipt{State: "canceled"},
					}},
				}})
				close(canceled)
			case *pb.ProtocolMessage_CloseSessionRequest:
				h.mu.Lock()
				h.closes++
				h.mu.Unlock()
				send(env.Id, &pb.ProtocolMessage{Message: &pb.ProtocolMessage_CloseSessionResponse{
					CloseSessionResponse: &pb.CloseSessionResponse{Outcome: &pb.CloseSessionResponse_Accepted{
						Accepted: &pb.Session{Id: m.CloseSessionRequest.GetSessionId(), State: "closed"},
					}},
				}})
			}
		}
	}
}

func newFake(t *testing.T, h *fakeHost) (*Executor, func()) {
	t.Helper()
	srv := httptest.NewServer(h.handler(t))
	return &Executor{BaseURL: srv.URL, Token: "tok", Agent: "local", Timeout: 10 * time.Second}, srv.Close
}

func TestRunStreamsTurnEvents(t *testing.T) {
	work := t.TempDir()
	h := &fakeHost{sessions: map[string]bool{}, hostDir: filepath.Dir(work)}
	ex, done := newFake(t, h)
	defer done()

	var events []executor.Event
	res, err := ex.Run(context.Background(), executor.Request{
		IntentID: "i1", Prompt: "find the token", WorkDir: work,
	}, func(ev executor.Event) { events = append(events, ev) })
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != "completed" || res.Tokens != 5715 || res.Text != "found KX-8842" {
		t.Fatalf("result = %+v", res)
	}
	if res.IsError {
		t.Fatalf("completed turn must not be an error: %+v", res)
	}
	var kinds []string
	for _, ev := range events {
		if ev.SessionID == "" {
			t.Fatal("accepted turn lost its native session identity")
		}
		kinds = append(kinds, ev.Kind)
	}
	want := []string{"turn_started", "message", "tool_call", "tool_result", "usage", "turn_ended"}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Fatalf("kinds = %v, want %v", kinds, want)
	}
	if n := len(events); n != res.Events {
		t.Fatalf("emitted %d events, result says %d", n, res.Events)
	}
	// The session workspace travels with OpenSession (D110), and the prompt
	// names the absolute case directory so writes land inside it.
	if len(h.workDirs) != 1 || h.workDirs[0] != work {
		t.Fatalf("session work dirs = %v, want %v", h.workDirs, work)
	}
	if !strings.Contains(h.prompts[0], work) || !strings.HasPrefix(h.prompts[0], "find the token") {
		t.Fatalf("prompt does not name the case directory: %q", h.prompts[0])
	}
	if h.cont[0] {
		t.Fatal("a fresh run must not ask for continue_session")
	}
	if !CanResume(work) {
		t.Fatal("the marker was not written, so steer could not work")
	}
	if h.opened != 1 || h.titles[0] != "tianwu: i1" {
		t.Fatalf("opened=%d titles=%v", h.opened, h.titles)
	}
}

// An evaluated run keeps the executor brief out of the turn input: the harness
// evaluator's Goal is the short control-plane goal, while the agent reads the
// full brief from the case file.
func TestEvaluatedRunKeepsBriefOutOfTurnGoal(t *testing.T) {
	work := t.TempDir()
	h := &fakeHost{sessions: map[string]bool{}, hostDir: filepath.Dir(work)}
	ex, done := newFake(t, h)
	defer done()

	brief := "## 意图\nUNIQUE-EXECUTOR-BRIEF 枚举攻击面\n\n## 交接（必做）\n写 .tw/facts.jsonl"
	_, err := ex.Run(context.Background(), executor.Request{
		IntentID: "i1", Prompt: brief, WorkDir: work,
		Goal: "枚举攻击面", Eval: "产出一份候选清单",
	}, func(executor.Event) {})
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(work, ".tw", "brief.md"))
	if err != nil {
		t.Fatalf("brief file not written: %v", err)
	}
	if !strings.Contains(string(data), "UNIQUE-EXECUTOR-BRIEF") {
		t.Fatalf("brief file lost the executor brief: %q", string(data))
	}
	p := h.prompts[0]
	if strings.Contains(p, "UNIQUE-EXECUTOR-BRIEF") {
		t.Fatalf("turn input still carries the executor brief: %q", p)
	}
	if !strings.Contains(p, "枚举攻击面") || !strings.Contains(p, "先读取 `.tw/brief.md`") || strings.Contains(p, filepath.Join(work, ".tw", "brief.md")) {
		t.Fatalf("turn input lost the short goal or brief path: %q", p)
	}
}

func TestRunReferencesSkillFileAndKeepsItsBaseDirectory(t *testing.T) {
	work := t.TempDir()
	dir := filepath.Join(t.TempDir(), "tianwu-report")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(skill, []byte("# Report\nHANDOFF-RULE-42"), 0o644); err != nil {
		t.Fatal(err)
	}

	h := &fakeHost{sessions: map[string]bool{}, hostDir: filepath.Dir(work)}
	ex, done := newFake(t, h)
	defer done()

	if _, err := ex.Run(context.Background(), executor.Request{
		IntentID: "i1", Prompt: "write the report", WorkDir: work,
		Skills: []string{skill},
	}, func(executor.Event) {}); err != nil {
		t.Fatal(err)
	}
	if len(h.prompts) != 1 {
		t.Fatalf("prompts = %d", len(h.prompts))
	}
	if !strings.Contains(h.prompts[0], skill) || strings.Contains(h.prompts[0], "HANDOFF-RULE-42") {
		t.Fatalf("AOP prompt must reference the skill file without inlining its body: %q", h.prompts[0])
	}
}

func TestCommanderSkillInvocationReachesHostAtInputStart(t *testing.T) {
	work := t.TempDir()
	h := &fakeHost{sessions: map[string]bool{}, hostDir: filepath.Dir(work)}
	ex, done := newFake(t, h)
	defer done()
	_, err := ex.Run(context.Background(), executor.Request{
		Prompt: "/skill:commander ## 本轮\n读取项目状态", WorkDir: work, Purpose: "commander",
	}, func(executor.Event) {})
	if err != nil {
		t.Fatal(err)
	}
	// Match the public harness command grammar at the actual wire boundary.
	name, args, found := strings.Cut(strings.TrimPrefix(h.prompts[0], "/skill:"), " ")
	if !strings.HasPrefix(h.prompts[0], "/skill:") || !found || name != "commander" || !strings.Contains(args, work) {
		t.Fatalf("native skill command cannot expand: %q", h.prompts[0])
	}
}

func TestRunFailsWhenSkillIsMissing(t *testing.T) {
	work := t.TempDir()
	h := &fakeHost{sessions: map[string]bool{}, hostDir: filepath.Dir(work)}
	ex, done := newFake(t, h)
	defer done()

	_, err := ex.Run(context.Background(), executor.Request{
		IntentID: "i1", Prompt: "x", WorkDir: work,
		Skills: []string{filepath.Join(t.TempDir(), "missing", "SKILL.md")},
	}, func(executor.Event) {})
	if err == nil || !strings.Contains(err.Error(), "技能") {
		t.Fatalf("want a missing-skill error, got %v", err)
	}
}

func TestRunRefusesACaseOutsideTheHost(t *testing.T) {
	work := t.TempDir()
	h := &fakeHost{sessions: map[string]bool{}, hostDir: t.TempDir()}
	ex, done := newFake(t, h)
	defer done()

	_, err := ex.Run(context.Background(), executor.Request{
		IntentID: "i1", Prompt: "x", WorkDir: work,
	}, func(executor.Event) {})
	if err == nil || !strings.Contains(err.Error(), "不包含") {
		t.Fatalf("want a containment error, got %v", err)
	}
	if h.opened != 1 {
		t.Fatalf("opened = %d", h.opened)
	}
}

func TestRunResumesTheMarkedSession(t *testing.T) {
	h := &fakeHost{sessions: map[string]bool{"s-old": true}}
	ex, done := newFake(t, h)
	defer done()
	work := t.TempDir()
	if err := writeMarker(work, marker{URL: ex.BaseURL, Agent: "local", Session: "s-old"}); err != nil {
		t.Fatal(err)
	}
	// The fake only knows sessions it opened itself, so teach it this one.
	h.mu.Lock()
	h.sessions["s-old"] = true
	h.mu.Unlock()

	res, err := ex.Run(context.Background(), executor.Request{
		IntentID: "i1", Prompt: "keep going", WorkDir: work, Resume: true,
	}, func(ev executor.Event) {
		if ev.SessionID != "s-old" {
			t.Errorf("resume session = %q", ev.SessionID)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != "completed" {
		t.Fatalf("result = %+v", res)
	}
	if h.opened != 0 {
		t.Fatalf("resume opened %d new sessions, want 0", h.opened)
	}
	if len(h.cont) != 1 || !h.cont[0] {
		t.Fatalf("continue flags = %v, want [true]", h.cont)
	}
	if !strings.Contains(h.prompts[0], "keep going") || !strings.Contains(h.prompts[0], work) {
		t.Fatalf("resumed prompt = %q", h.prompts[0])
	}
}

// A continued turn must ask the host for the tail, not replay the session from
// zero. The cursor is opaque; the test only checks that the marker's value is
// what the second WatchEvents request carries.
func TestResumeUsesTheDeliveryCursor(t *testing.T) {
	work := t.TempDir()
	h := &fakeHost{sessions: map[string]bool{}, hostDir: filepath.Dir(work)}
	ex, done := newFake(t, h)
	defer done()

	if _, err := ex.Run(context.Background(), executor.Request{
		IntentID: "i1", Prompt: "first", WorkDir: work,
	}, func(executor.Event) {}); err != nil {
		t.Fatal(err)
	}
	m, ok := readMarker(work)
	if !ok || m.Cursor == "" {
		t.Fatalf("marker has no cursor after the first turn: %+v", m)
	}
	if _, err := ex.Run(context.Background(), executor.Request{
		IntentID: "i1", Prompt: "second", WorkDir: work, Resume: true,
	}, func(executor.Event) {}); err != nil {
		t.Fatal(err)
	}
	if len(h.watchCursors) != 2 || h.watchCursors[0] != "" || h.watchCursors[1] != m.Cursor {
		t.Fatalf("watch cursors = %v, want [\"\" %q]", h.watchCursors, m.Cursor)
	}
}

// TestResumeIgnoresReplayedTurns is the regression for the bug that made every
// steered AOP run "finish" one second in, cancelled, without running a turn:
// WatchEvents replays the session, and an old turn_ended used to end the new t
// urn.
func TestResumeIgnoresReplayedTurns(t *testing.T) {
	h := &fakeHost{sessions: map[string]bool{"s-old": true}, replay: true}
	ex, done := newFake(t, h)
	defer done()
	work := t.TempDir()
	if err := writeMarker(work, marker{URL: ex.BaseURL, Agent: "local", Session: "s-old"}); err != nil {
		t.Fatal(err)
	}

	var events []executor.Event
	res, err := ex.Run(context.Background(), executor.Request{
		IntentID: "i1", Prompt: "keep going", WorkDir: work, Resume: true,
	}, func(ev executor.Event) { events = append(events, ev) })
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != "completed" || res.IsError {
		t.Fatalf("replayed turn_ended leaked into the result: %+v", res)
	}
	if res.Tokens != 5715 {
		t.Fatalf("tokens = %d, want the current turn's 5715 (the replay's 20015 leaked)", res.Tokens)
	}
	for _, ev := range events {
		if ev.Text == "canceled" || (ev.Kind == executor.KindTurnEnd && ev.StopReason == "canceled") {
			t.Fatalf("event from the replayed turn was emitted: %+v", ev)
		}
	}
}

func TestResumeWithoutMarkerIsRefused(t *testing.T) {
	h := &fakeHost{sessions: map[string]bool{}}
	ex, done := newFake(t, h)
	defer done()
	_, err := ex.Run(context.Background(), executor.Request{
		IntentID: "i1", Prompt: "keep going", WorkDir: t.TempDir(), Resume: true,
	}, func(executor.Event) {})
	if !errors.Is(err, ErrNoSession) {
		t.Fatalf("want ErrNoSession, got %v", err)
	}
	if h.opened != 0 {
		t.Fatal("a refused resume must not silently open a new session")
	}
}

func TestResumeRefusesASessionFromAnotherHost(t *testing.T) {
	h := &fakeHost{sessions: map[string]bool{}}
	ex, done := newFake(t, h)
	defer done()
	work := t.TempDir()
	if err := writeMarker(work, marker{URL: "http://elsewhere:1", Agent: "local", Session: "s1"}); err != nil {
		t.Fatal(err)
	}
	_, err := ex.Run(context.Background(), executor.Request{
		IntentID: "i1", Prompt: "keep going", WorkDir: work, Resume: true,
	}, func(executor.Event) {})
	if err == nil || !strings.Contains(err.Error(), "http://elsewhere:1") {
		t.Fatalf("want a mismatch error naming the other host, got %v", err)
	}
}

// TestResumeOfADeadSessionDropsTheMarker: a session the host no longer knows
// must not stay "resumable" forever — the next attempt should be a plain "no
// session", which is the degrade path the decider understands.
func TestResumeOfADeadSessionDropsTheMarker(t *testing.T) {
	h := &fakeHost{sessions: map[string]bool{}}
	ex, done := newFake(t, h)
	defer done()
	work := t.TempDir()
	if err := writeMarker(work, marker{URL: ex.BaseURL, Agent: "local", Session: "gone"}); err != nil {
		t.Fatal(err)
	}
	_, err := ex.Run(context.Background(), executor.Request{
		IntentID: "i1", Prompt: "keep going", WorkDir: work, Resume: true,
	}, func(executor.Event) {})
	if err == nil || !strings.Contains(err.Error(), "NOT_FOUND") {
		t.Fatalf("want the host's NOT_FOUND, got %v", err)
	}
	if CanResume(work) {
		t.Fatal("a dead session must not keep claiming to be resumable")
	}
}

func TestTurnRejectionSurfacesTheCode(t *testing.T) {
	h := &fakeHost{sessions: map[string]bool{}, rejectTurn: true}
	ex, done := newFake(t, h)
	defer done()
	_, err := ex.Run(context.Background(), executor.Request{
		IntentID: "i1", Prompt: "x", WorkDir: t.TempDir(),
	}, func(executor.Event) {})
	if err == nil || !strings.Contains(err.Error(), "UNAVAILABLE") {
		t.Fatalf("want the protocol code in the error, got %v", err)
	}
}

func TestCancelAsksTheHostToStop(t *testing.T) {
	h := &fakeHost{sessions: map[string]bool{}, hold: true}
	ex, done := newFake(t, h)
	defer done()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Cancel an accepted turn, rather than racing host/session setup against a
	// wall-clock sleep when the full suite runs under load.
	res, err := ex.Run(ctx, executor.Request{
		IntentID: "i1", Prompt: "x", WorkDir: t.TempDir(),
	}, func(ev executor.Event) {
		if ev.Kind == executor.KindTurnStart {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v (%+v)", err, res)
	}
	if res.StopReason != "canceled" {
		t.Fatalf("stop reason = %q, want canceled", res.StopReason)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cancels != 1 {
		t.Fatalf("cancel requests = %d, want 1", h.cancels)
	}
}

// TestEventMapping pins the payload-to-kind mapping, including the two details
// that cost time on the wire: the error belongs to turn_ended, and usage is a
// uint64 that must reach Result.Tokens.
func TestEventMapping(t *testing.T) {
	ev := event(&pb.Event{Payload: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{
		StopReason: "error",
		Error:      &pb.ProtocolError{Code: "provider_auth", Message: "bad key"},
		Usage:      &pb.TokenUsage{TotalTokens: 99},
	}}})
	if ev.Kind != executor.KindTurnEnd || ev.OK {
		t.Fatalf("turn_ended with error mapped to %+v", ev)
	}
	if ev.Tokens != 99 || !strings.Contains(ev.Text, "provider_auth") {
		t.Fatalf("mapped event = %+v", ev)
	}

	// The host attaches protocol payloads we have no descriptor for; recording
	// the event must not fail because of them.
	withExt := &pb.Event{
		Extensions: []*anypb.Any{{TypeUrl: "type.googleapis.com/unknown.Thing", Value: []byte{1, 2}}},
		Payload:    &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}},
	}
	if raw := rawJSON(withExt); len(raw) == 0 || !strings.Contains(string(raw), "turn_started") {
		t.Fatalf("raw = %q", raw)
	}
}

// The execution review pairs a result with its call and shows the harness's
// own execution time, so the protocol mapping must carry ToolCall.id and
// ToolResult.call_id/duration_ms into the journal (and thus onto the wire the
// console reads).
func TestEventMappingCarriesToolCallIdentityAndDuration(t *testing.T) {
	call := event(&pb.Event{Payload: &pb.Event_ToolCall{ToolCall: &pb.ToolCall{Id: "call-1", Name: "bash", Kind: "bash"}}})
	if call.Kind != executor.KindToolCall || call.CallID != "call-1" {
		t.Fatalf("tool_call = %+v", call)
	}
	result := event(&pb.Event{Payload: &pb.Event_ToolResult{ToolResult: &pb.ToolResult{
		CallId: "call-1", Name: "bash", DurationMs: 250,
		Output: []*pb.Content{{Value: &pb.Content_Text{Text: &pb.TextContent{Text: "ok"}}}},
	}}})
	if result.Kind != executor.KindToolResult || !result.OK || result.CallID != "call-1" || result.DurationMs != 250 {
		t.Fatalf("tool_result = %+v", result)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"call_id":"call-1"`) || !strings.Contains(string(encoded), `"duration_ms":250`) {
		t.Fatalf("journal line lost the review fields: %s", encoded)
	}
	// An absent duration must stay omitted, not become a measured 0 on the wire.
	failed := event(&pb.Event{Payload: &pb.Event_ToolResult{ToolResult: &pb.ToolResult{
		CallId: "call-2", Name: "bash", IsError: true,
	}}})
	encoded, err = json.Marshal(failed)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "duration_ms") {
		t.Fatalf("unreported duration was written as a measured zero: %s", encoded)
	}
	if failed.CallID != "call-2" || failed.OK {
		t.Fatalf("failed result = %+v", failed)
	}
}

// The harness puts the window it resolved for the model in usage.detail; the
// control plane must not re-derive it from its own model table (D80).
func TestEventMappingCarriesResolvedContextWindow(t *testing.T) {
	ev := event(&pb.Event{Payload: &pb.Event_Usage{Usage: &pb.TokenUsage{
		TotalTokens: 100,
		Detail:      map[string]uint64{"context_tokens": 12345, "context_window": 1000000},
	}}})
	if ev.ContextTokens != 12345 || ev.ContextWindow != 1000000 {
		t.Fatalf("usage = %+v", ev)
	}
	ended := event(&pb.Event{Payload: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{
		StopReason: "completed",
		Usage:      &pb.TokenUsage{TotalTokens: 100, Detail: map[string]uint64{"context_window": 1000000}},
	}}})
	if ended.ContextWindow != 1000000 {
		t.Fatalf("turn_ended = %+v", ended)
	}
}

func TestMarkerRoundTrip(t *testing.T) {
	work := t.TempDir()
	if CanResume(work) {
		t.Fatal("a fresh case must not claim a session")
	}
	if err := writeMarker(work, marker{URL: "http://h", Agent: "a", Session: "s", Turn: "t"}); err != nil {
		t.Fatal(err)
	}
	m, ok := readMarker(work)
	if !ok || m.URL != "http://h" || m.Session != "s" {
		t.Fatalf("marker = %+v ok=%v", m, ok)
	}
	if !CanResume(work) {
		t.Fatal("CanResume = false after writing the marker")
	}
}

// A purpose run gets no marker, so nothing will ever continue or close its
// session: the executor must close it, or reviews and reports accumulate one
// dead session per call on the host.
func TestPurposeRunClosesItsSession(t *testing.T) {
	work := t.TempDir()
	h := &fakeHost{sessions: map[string]bool{}, hostDir: filepath.Dir(work)}
	ex, done := newFake(t, h)
	defer done()

	if _, err := ex.Run(context.Background(), executor.Request{
		Prompt: "write the report", WorkDir: work, Purpose: "report",
	}, func(executor.Event) {}); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closes != 1 {
		t.Fatalf("closes = %d, want the one-shot session closed", h.closes)
	}
}

// The commander is a long session across rounds (D78): its purpose must not
// take the one-shot close path, or every round starts from an empty context and
// the model has to relearn the skill and rebuild its memory from state.md.
func TestCommanderRunKeepsItsSessionForTheNextRound(t *testing.T) {
	work := t.TempDir()
	h := &fakeHost{sessions: map[string]bool{}, hostDir: filepath.Dir(work)}
	ex, done := newFake(t, h)
	defer done()

	if _, err := ex.Run(context.Background(), executor.Request{
		Prompt: "round one", WorkDir: work, Purpose: "commander",
	}, func(executor.Event) {}); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	closes := h.closes
	h.mu.Unlock()
	if closes != 0 {
		t.Fatalf("commander session closed after the round: %d", closes)
	}
	if !CanResume(work) {
		t.Fatal("commander round left no resume marker for the next round")
	}
}

func TestEventMappingCarriesUsageBreakdown(t *testing.T) {
	ev := event(&pb.Event{Payload: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{
		StopReason: "completed",
		Usage: &pb.TokenUsage{
			InputTokens: 100, OutputTokens: 20, TotalTokens: 120,
			Detail: map[string]uint64{"cache_read": 40, "cache_write": 5, "requests": 2},
		},
	}}})
	if ev.InputTokens != 100 || ev.OutputTokens != 20 || ev.CachedInputTokens != 40 ||
		ev.CacheWriteTokens != 5 || ev.UsageRequests != 2 || ev.UsageMissing != 0 {
		t.Fatalf("usage event = %+v", ev)
	}
	res := executor.Result{}
	absorb(&res, ev)
	if !res.UsageKnown || res.InputTokens != 100 || res.OutputTokens != 20 ||
		res.CachedInputTokens != 40 || res.UsageRequests != 2 {
		t.Fatalf("absorbed = %+v", res)
	}
	// A turn whose calls all returned no usage is unknown, not zero.
	missing := event(&pb.Event{Payload: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{
		StopReason: "completed",
		Usage:      &pb.TokenUsage{Detail: map[string]uint64{"requests": 2, "usage_missing": 2}},
	}}})
	absorb(&res, missing)
	if res.UsageKnown {
		t.Fatalf("all-missing usage marked known: %+v", res)
	}
}

// The live harness reports per-call usage events (with requests/usage_missing)
// but its turn_ended usage carries only the token totals: detail has cache_*
// and no request counters. The totals are real usage, so the run must not be
// reported as "usage missing / 0 tokens" just because the counters are absent.
func TestTurnEndedTotalsWithoutRequestCountersStayKnown(t *testing.T) {
	res := executor.Result{}
	absorb(&res, event(&pb.Event{Payload: &pb.Event_Usage{Usage: &pb.TokenUsage{
		InputTokens: 100, OutputTokens: 20, TotalTokens: 120,
		Detail: map[string]uint64{"requests": 1, "cache_read": 40},
	}}}))
	absorb(&res, event(&pb.Event{Payload: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{
		StopReason: "completed",
		Usage: &pb.TokenUsage{
			InputTokens: 1600, OutputTokens: 300, TotalTokens: 1900,
			Detail: map[string]uint64{"cache_read": 1500, "cache_write": 0},
		},
	}}}))
	if !res.UsageKnown || res.Tokens != 1900 || res.InputTokens != 1600 || res.OutputTokens != 300 {
		t.Fatalf("absorbed = %+v", res)
	}
	// The request count is per-call, not zero just because turn_ended omitted it.
	if res.UsageRequests != 1 {
		t.Fatalf("requests = %d, want the per-call count", res.UsageRequests)
	}
}

// A build that emits no per-call usage events still reports equivalent totals
// on turn_ended; those are enough to price the run.
func TestTurnEndedTotalsAloneAreKnown(t *testing.T) {
	res := executor.Result{}
	absorb(&res, event(&pb.Event{Payload: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{
		StopReason: "completed",
		Usage:      &pb.TokenUsage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12},
	}}}))
	if !res.UsageKnown || res.Tokens != 12 {
		t.Fatalf("absorbed = %+v", res)
	}
}

// Without any reported usage at all the run stays unknown, never a measured 0.
func TestTurnEndedWithoutUsageStaysUnknown(t *testing.T) {
	res := executor.Result{}
	absorb(&res, event(&pb.Event{Payload: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{
		StopReason: "completed",
		Usage:      &pb.TokenUsage{Detail: map[string]uint64{"context_tokens": 100}},
	}}}))
	if res.UsageKnown {
		t.Fatalf("no usage marked known: %+v", res)
	}
}

func TestEventMappingPartialUsageIsKnownWithMissingCount(t *testing.T) {
	ev := event(&pb.Event{Payload: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{
		StopReason: "completed",
		Usage: &pb.TokenUsage{
			InputTokens: 10, OutputTokens: 2, TotalTokens: 12,
			Detail: map[string]uint64{"requests": 3, "usage_missing": 1},
		},
	}}})
	res := executor.Result{}
	absorb(&res, ev)
	if !res.UsageKnown || res.UsageMissing != 1 || res.UsageRequests != 3 {
		t.Fatalf("partial usage = %+v", res)
	}
}
