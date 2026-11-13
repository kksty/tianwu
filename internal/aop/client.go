// Package aop is the client side of the Agent Orchestration Protocol: the
// control plane speaking harness's own language instead of inventing one.
//
// Transport: the WebSocket compatibility layer harness ships for browsers
// (`/api/aop/application/ws`). One connection carries one bidirectional stream of
// `aop.Envelope` messages, each frame a marshalled protobuf. Requests carry a
// ProtocolMessage in the Any payload; the reply comes back with `reply_to` set
// to the request id, and events arrive on the same socket while a turn runs.
// (The Connect/gRPC transport is the same service, but a bidi Connect stream
// needs HTTP/2 and the harness serves cleartext HTTP/1.1 here.)
package aop

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	pb "github.com/kksty/tianwu/internal/aop/pb"
	"github.com/kksty/tianwu/internal/ws"
)

// Client dials one harness server (aiscan web: the process that hosts the AOP
// application transport and the agent pool).
type Client struct {
	BaseURL string
	Token   string
}

func New(baseURL, token string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Token: token}
}

// Conn is one open Envelope stream. It multiplexes replies and events by
// request id, so callers see request/response while events keep flowing.
type Conn struct {
	sock    *ws.Conn
	events  chan *pb.Event
	replies chan *pb.Envelope
	errs    chan error
	// dropped counts events the reader could not hand over. A ledger that
	// silently loses events is worse than one that says how many it lost, so
	// the executor appends this to the run's reason instead of hiding it.
	dropped atomic.Int64
	// cursor is the last delivery cursor seen on this stream. A resumed turn
	// passes it back as after_cursor, so the host replays only the new tail
	// instead of the whole session.
	cursor atomic.Pointer[string]
}

// Dial opens the application socket and starts the reader.
func (c *Client) Dial(ctx context.Context) (*Conn, error) {
	url := applicationWSURL(c.BaseURL)
	header := http.Header{}
	if c.Token != "" {
		header.Set("Authorization", "Bearer "+c.Token)
	}
	sock, err := ws.Dial(ctx, url, header)
	if err != nil {
		return nil, fmt.Errorf("aop: 连接 %s：%w", url, err)
	}
	conn := &Conn{
		sock:    sock,
		events:  make(chan *pb.Event, 512),
		replies: make(chan *pb.Envelope, 16),
		errs:    make(chan error, 1),
	}
	go conn.read()
	return conn, nil
}

func applicationWSURL(base string) string {
	u := base
	switch {
	case strings.HasPrefix(u, "https://"):
		u = "wss://" + strings.TrimPrefix(u, "https://")
	case strings.HasPrefix(u, "http://"):
		u = "ws://" + strings.TrimPrefix(u, "http://")
	}
	return strings.TrimRight(u, "/") + "/api/aop/application/ws"
}

func (c *Conn) read() {
	for {
		raw, err := c.sock.ReadMessage()
		if err != nil {
			select {
			case c.errs <- err:
			default:
			}
			close(c.events)
			close(c.replies)
			return
		}
		var env pb.Envelope
		if err := proto.Unmarshal(raw, &env); err != nil {
			continue
		}
		var msg pb.ProtocolMessage
		if env.Payload == nil {
			continue
		}
		// Other registered namespaces (commands, traffic) have their own reply
		// schemas. Keep their envelopes for the matching caller.
		_ = env.Payload.UnmarshalTo(&msg)
		if dc := env.GetDeliveryCursor(); dc != "" {
			c.cursor.Store(&dc)
		}
		if ev := msg.GetEvent(); ev != nil {
			select {
			case c.events <- ev:
			default:
				c.dropped.Add(1)
			}
			continue
		}
		select {
		case c.replies <- &env:
		default:
		}
	}
}

// Events is the stream of turn events for every session on this connection.
func (c *Conn) Events() <-chan *pb.Event { return c.events }

// Dropped is how many events were discarded because the consumer fell behind.
func (c *Conn) Dropped() int { return int(c.dropped.Load()) }

// Cursor is the last delivery cursor seen on the stream. A resumed turn uses it
// as after_cursor so the host resends only the tail, not the whole session.
func (c *Conn) Cursor() string {
	if p := c.cursor.Load(); p != nil {
		return *p
	}
	return ""
}

func (c *Conn) send(id string, msg *pb.ProtocolMessage) error {
	payload, err := anypb.New(msg)
	if err != nil {
		return err
	}
	raw, err := proto.Marshal(&pb.Envelope{Id: id, Payload: payload})
	if err != nil {
		return err
	}
	return c.sock.WriteBinary(raw)
}

// call sends one request and waits for the envelope that answers it.
func (c *Conn) call(ctx context.Context, msg *pb.ProtocolMessage) (*pb.Envelope, *pb.ProtocolMessage, error) {
	id := newID()
	if err := c.send(id, msg); err != nil {
		return nil, nil, err
	}
	for {
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case err := <-c.errs:
			return nil, nil, err
		case env, ok := <-c.replies:
			if !ok {
				return nil, nil, errors.New("aop: 连接已断开")
			}
			if env.ReplyTo != id {
				continue // another request's reply; the protocol allows interleaving
			}
			var out pb.ProtocolMessage
			if env.Payload == nil || env.Payload.UnmarshalTo(&out) != nil {
				return nil, nil, errors.New("aop: 回复无法解析")
			}
			// A refusal travels as a ProtocolError payload, not as a broken
			// request: return it as an error so callers see "NOT_FOUND: session
			// not found" instead of "unexpected reply type".
			if pe := out.GetProtocolError(); pe != nil {
				return env, &out, fmt.Errorf("aop: %s：%s", pe.GetCode(), pe.GetMessage())
			}
			return env, &out, nil
		}
	}
}

// OpenSession asks for a session, optionally pinned to a node. workDir is the
// session's workspace (the case directory); it travels as a Tianwu fork
// extension because upstream AOP has no per-session working directory (D110).
func (c *Conn) OpenSession(ctx context.Context, nodeID, title, workDir string) (*pb.Session, error) {
	req := &pb.OpenSessionRequest{NodeId: nodeID, Title: title}
	if extension := WorkDirExtension(workDir); extension != nil {
		req.Extensions = []*anypb.Any{extension}
	}
	_, reply, err := c.call(ctx, &pb.ProtocolMessage{Message: &pb.ProtocolMessage_OpenSessionRequest{
		OpenSessionRequest: req,
	}})
	if err != nil {
		return nil, err
	}
	resp := reply.GetOpenSessionResponse()
	if resp == nil {
		return nil, fmt.Errorf("aop: 意外的回复 %T", reply.Message)
	}
	if rej := resp.GetRejected(); rej != nil {
		return nil, fmt.Errorf("aop: 会话被拒：%s %s", rej.GetCode(), rej.GetMessage())
	}
	return resp.GetAccepted(), nil
}

// TurnOptions is how a turn relates to the session it runs in.
type TurnOptions struct {
	// Continue reuses the session's existing conversation instead of starting a
	// new one: the input is delivered as the next user message but is not
	// re-published as a new session opening (the host then keeps the context).
	Continue bool
	// MaxTurns caps the host agent loop for this turn; 0 leaves the host default.
	MaxTurns uint32
	// Eval is the intent's acceptance criterion. It travels as the harness's
	// AgentRunOptions extension, not as text in the brief: the evaluator is a
	// different role and must not judge the executor's prompt.
	Eval string
}

const agentRunOptionsTypeURL = "type.googleapis.com/cyber.agent.AgentRunOptions"

// agentRunOptions encodes the extension by hand. tianwu does not link the whole
// harness module just for one two-field message; the wire format is stable and
// the harness validates it with the real proto on its side.
func agentRunOptions(criteria string) *anypb.Any {
	var b []byte
	if criteria = strings.TrimSpace(criteria); criteria != "" {
		b = protowire.AppendTag(b, 1, protowire.BytesType) // eval_criteria
		b = protowire.AppendString(b, criteria)
	}
	return &anypb.Any{TypeUrl: agentRunOptionsTypeURL, Value: b}
}

// RunTurn sends one prompt. Events for it come back on Events().
func (c *Conn) RunTurn(ctx context.Context, sessionID, turnID, prompt string) (*pb.TurnReceipt, error) {
	return c.RunTurnOpts(ctx, sessionID, turnID, prompt, TurnOptions{})
}

// RunTurnOpts is RunTurn with an explicit continuation choice.
func (c *Conn) RunTurnOpts(ctx context.Context, sessionID, turnID, prompt string, opts TurnOptions) (*pb.TurnReceipt, error) {
	msg := &pb.Message{
		Id:      newID(),
		Role:    "user",
		Content: []*pb.Content{{Value: &pb.Content_Text{Text: &pb.TextContent{Text: prompt}}}},
	}
	req := &pb.RunTurnRequest{
		SessionId:       sessionID,
		TurnId:          turnID,
		Input:           msg,
		ContinueSession: opts.Continue,
		MaxTurns:        opts.MaxTurns,
	}
	if strings.TrimSpace(opts.Eval) != "" {
		req.Extensions = []*anypb.Any{agentRunOptions(opts.Eval)}
	}
	_, reply, err := c.call(ctx, &pb.ProtocolMessage{Message: &pb.ProtocolMessage_RunTurnRequest{
		RunTurnRequest: req,
	}})
	if err != nil {
		return nil, err
	}
	resp := reply.GetRunTurnResponse()
	if resp == nil {
		return nil, fmt.Errorf("aop: 意外的回复 %T", reply.Message)
	}
	if rej := resp.GetRejected(); rej != nil {
		return nil, fmt.Errorf("aop: 回合被拒：%s %s", rej.GetCode(), rej.GetMessage())
	}
	return resp.GetAccepted(), nil
}

// CancelTurn asks the node to stop; the stream still ends with turn_ended.
func (c *Conn) CancelTurn(ctx context.Context, sessionID, turnID, reason string) error {
	_, reply, err := c.call(ctx, &pb.ProtocolMessage{Message: &pb.ProtocolMessage_CancelTurnRequest{
		CancelTurnRequest: &pb.CancelTurnRequest{SessionId: sessionID, TurnId: turnID, Reason: reason},
	}})
	if err != nil {
		return err
	}
	if resp := reply.GetCancelTurnResponse(); resp != nil {
		if rej := resp.GetRejected(); rej != nil {
			return fmt.Errorf("aop: 取消失败：%s %s", rej.GetCode(), rej.GetMessage())
		}
	}
	return nil
}

// CloseSession ends the session on the node.
func (c *Conn) CloseSession(ctx context.Context, sessionID, reason string) error {
	_, _, err := c.call(ctx, &pb.ProtocolMessage{Message: &pb.ProtocolMessage_CloseSessionRequest{
		CloseSessionRequest: &pb.CloseSessionRequest{SessionId: sessionID, Reason: reason},
	}})
	return err
}

// Watch subscribes to a session's events. It is a subscription, not an RPC: the
// server answers with the event stream itself (envelopes whose reply_to is this
// request), so there is nothing to wait for here.
func (c *Conn) Watch(ctx context.Context, sessionID, afterCursor string) error {
	return c.send(newID(), &pb.ProtocolMessage{Message: &pb.ProtocolMessage_WatchEventsRequest{
		WatchEventsRequest: &pb.WatchEventsRequest{SessionId: sessionID, AfterCursor: afterCursor},
	}})
}

func (c *Conn) Close() error { return c.sock.Close() }

func newID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
