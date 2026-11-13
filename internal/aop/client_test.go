package aop

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	pb "github.com/kksty/tianwu/internal/aop/pb"
	"github.com/kksty/tianwu/internal/ws"
)

// The session workspace is a Tianwu fork extension on OpenSessionRequest: the
// upstream schema has no per-session working directory (D110). Pin the wire
// shape so a refactor cannot silently send sessions back to the agent's
// process directory.
func TestWorkDirExtensionPayload(t *testing.T) {
	if WorkDirExtension("  ") != nil {
		t.Fatal("empty work dir must not create an extension")
	}
	const dir = `C:\data\projects\p\cases\in_1`
	ext := WorkDirExtension(dir)
	if ext.GetTypeUrl() != WorkDirExtensionURL {
		t.Fatalf("type url = %q", ext.GetTypeUrl())
	}
	var payload workDirPayload
	if err := json.Unmarshal(ext.GetValue(), &payload); err != nil {
		t.Fatalf("payload = %q: %v", ext.GetValue(), err)
	}
	if payload.WorkDir != dir {
		t.Fatalf("work dir = %q, want %q", payload.WorkDir, dir)
	}
	if !payload.SingleTask {
		t.Fatal("control plane lost ownership of turn scheduling")
	}
}

// TestSessionLifecycle drives the client against a minimal AOP server: open a
// session, run a turn, receive events, close. It pins the two things that are
// easy to get wrong — reply correlation by reply_to, and WatchEvents being a
// subscription rather than an RPC.
// The eval criterion is a harness extension, not prompt text. Pin the wire
// shape here: the harness validates it with cyber.agent.AgentRunOptions, and a
// wrong type URL or field number silently disables evaluation.
func TestAgentRunOptionsExtensionCarriesEvalCriteria(t *testing.T) {
	ext := agentRunOptions("完成验收标准")
	if ext.GetTypeUrl() != agentRunOptionsTypeURL {
		t.Fatalf("type url = %q", ext.GetTypeUrl())
	}
	num, typ, n := protowire.ConsumeTag(ext.GetValue())
	if num != 1 || typ != protowire.BytesType || n < 0 {
		t.Fatalf("tag = %d/%d/%d", num, typ, n)
	}
	criteria, m := protowire.ConsumeString(ext.GetValue()[n:])
	if m < 0 || criteria != "完成验收标准" {
		t.Fatalf("criteria = %q, n = %d", criteria, m)
	}
}

func TestSessionLifecycle(t *testing.T) {
	var openedWorkDir *anypb.Any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/aop/application/ws" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer testkey" {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		conn, err := ws.Upgrade(w, r)
		if err != nil {
			return
		}
		defer conn.Close()
		watchID := ""
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
				for _, extension := range m.OpenSessionRequest.GetExtensions() {
					if extension.GetTypeUrl() == WorkDirExtensionURL {
						openedWorkDir = extension
					}
				}
				_ = replyEnv(conn, env.Id, &pb.ProtocolMessage{Message: &pb.ProtocolMessage_OpenSessionResponse{
					OpenSessionResponse: &pb.OpenSessionResponse{Outcome: &pb.OpenSessionResponse_Accepted{
						Accepted: &pb.Session{Id: "s1", State: "open", NodeId: m.OpenSessionRequest.GetNodeId()},
					}},
				}})
			case *pb.ProtocolMessage_WatchEventsRequest:
				watchID = env.Id
			case *pb.ProtocolMessage_RunTurnRequest:
				_ = replyEnv(conn, env.Id, &pb.ProtocolMessage{Message: &pb.ProtocolMessage_RunTurnResponse{
					RunTurnResponse: &pb.RunTurnResponse{Outcome: &pb.RunTurnResponse_Accepted{
						Accepted: &pb.TurnReceipt{SessionId: "s1", TurnId: m.RunTurnRequest.GetTurnId(), State: "running"},
					}},
				}})
				// Events are delivered under the subscription id, not the turn id.
				for seq, ev := range []*pb.Event{
					{Seq: 1, Payload: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}},
					{Seq: 2, Payload: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{StopReason: "completed"}}},
				} {
					ev.Id = "e" + string(rune('0'+seq))
					_ = replyEnv(conn, watchID, &pb.ProtocolMessage{Message: &pb.ProtocolMessage_Event{Event: ev}})
				}
			case *pb.ProtocolMessage_CloseSessionRequest:
				_ = replyEnv(conn, env.Id, &pb.ProtocolMessage{Message: &pb.ProtocolMessage_CloseSessionResponse{
					CloseSessionResponse: &pb.CloseSessionResponse{Outcome: &pb.CloseSessionResponse_Accepted{
						Accepted: &pb.Session{Id: "s1", State: "closed"},
					}},
				}})
			}
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c := New("ws"+strings.TrimPrefix(srv.URL, "http"), "testkey")
	c.BaseURL = srv.URL
	conn, err := c.Dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	const sessionWorkDir = "/data/projects/p/cases/in_1"
	sess, err := conn.OpenSession(ctx, "local", "t", sessionWorkDir)
	if err != nil {
		t.Fatal(err)
	}
	if sess.GetId() != "s1" || sess.GetNodeId() != "local" {
		t.Fatalf("session = %+v", sess)
	}
	if openedWorkDir == nil {
		t.Fatal("OpenSessionRequest carried no work dir extension")
	}
	var payload workDirPayload
	if err := json.Unmarshal(openedWorkDir.GetValue(), &payload); err != nil || payload.WorkDir != sessionWorkDir {
		t.Fatalf("work dir payload = %q, %v", openedWorkDir.GetValue(), err)
	}
	if err := conn.Watch(ctx, sess.GetId(), ""); err != nil {
		t.Fatal(err)
	}
	receipt, err := conn.RunTurn(ctx, sess.GetId(), "turn-1", "hi")
	if err != nil {
		t.Fatal(err)
	}
	if receipt.GetTurnId() != "turn-1" {
		t.Fatalf("receipt = %+v", receipt)
	}
	for want := 1; want <= 2; want++ {
		select {
		case ev := <-conn.Events():
			if int(ev.GetSeq()) != want {
				t.Fatalf("event seq = %d, want %d", ev.GetSeq(), want)
			}
		case <-ctx.Done():
			t.Fatalf("missing event %d", want)
		}
	}
	if err := conn.CloseSession(ctx, sess.GetId(), "done"); err != nil {
		t.Fatal(err)
	}
}

func TestApplicationWSURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"http://127.0.0.1:8080", "ws://127.0.0.1:8080/api/aop/application/ws"},
		{"https://hub.example/", "wss://hub.example/api/aop/application/ws"},
		{"ws://already", "ws://already/api/aop/application/ws"},
	}
	for _, c := range cases {
		if got := applicationWSURL(c.in); got != c.want {
			t.Fatalf("applicationWSURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func replyEnv(conn *ws.Conn, replyTo string, msg *pb.ProtocolMessage) error {
	payload, err := anypb.New(msg)
	if err != nil {
		return err
	}
	raw, err := proto.Marshal(&pb.Envelope{Id: "reply", ReplyTo: replyTo, Payload: payload})
	if err != nil {
		return err
	}
	return conn.WriteBinary(raw)
}
