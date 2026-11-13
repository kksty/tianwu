package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	pb "github.com/kksty/tianwu/internal/aop/pb"
	"github.com/kksty/tianwu/internal/aop/pty"
	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/ws"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// ptyHost is a minimal AOP host that serves one PTY stream.
func ptyHost(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := ws.Upgrade(w, r)
		if err != nil {
			return
		}
		defer conn.Close()
		send := func(msg *pty.ProtocolMessage) {
			payload, _ := anypb.New(msg)
			raw, _ := proto.Marshal(&pb.Envelope{Id: msgStream(msg), Payload: payload})
			_ = conn.WriteBinary(raw)
		}
		for {
			raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var env pb.Envelope
			if proto.Unmarshal(raw, &env) != nil || env.Payload == nil {
				continue
			}
			var msg pty.ProtocolMessage
			if env.Payload.UnmarshalTo(&msg) != nil {
				continue
			}
			switch v := msg.Message.(type) {
			case *pty.ProtocolMessage_Open:
				id := v.Open.GetStreamId()
				send(&pty.ProtocolMessage{Message: &pty.ProtocolMessage_Opened{Opened: &pty.Opened{StreamId: id}}})
				send(&pty.ProtocolMessage{Message: &pty.ProtocolMessage_Output{Output: &pty.Output{StreamId: id, Data: []byte("hello")}}})
			case *pty.ProtocolMessage_Input:
				send(&pty.ProtocolMessage{Message: &pty.ProtocolMessage_Output{
					Output: &pty.Output{StreamId: v.Input.GetStreamId(), Data: append([]byte("echo:"), v.Input.GetData()...)},
				}})
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func msgStream(msg *pty.ProtocolMessage) string {
	if out := msg.GetOutput(); out != nil {
		return out.GetStreamId()
	}
	return ""
}

type termExec struct{ url string }

func (termExec) Name() string { return "term" }
func (termExec) Run(context.Context, executor.Request, func(executor.Event)) (executor.Result, error) {
	return executor.Result{ExitCode: 0}, nil
}
func (termExec) Nodes() []executor.NodeInfo {
	return []executor.NodeInfo{{ID: "lab", Transport: "aop", Endpoint: "http://host"}}
}
func (termExec) ResolveNode(string) string { return "lab" }
func (e termExec) TerminalEndpoint(string) (string, string, string, bool) {
	return e.url, "tok", "local", true
}

func TestNodeTerminalBridgesBytes(t *testing.T) {
	host := ptyHost(t)
	s := New(nil, termExec{url: host.URL}, t.TempDir(), t.TempDir())
	mux := http.NewServeMux()
	s.Register(mux)
	front := httptest.NewServer(mux)
	defer front.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(front.URL, "http") + "/api/nodes/lab/terminal?cols=80&rows=24"
	conn, err := ws.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial bridge: %v", err)
	}
	defer conn.Close()

	read := func() string {
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		return string(data)
	}
	if got := read(); got != "hello" {
		t.Fatalf("greeting = %q", got)
	}
	if err := conn.WriteBinary([]byte("ls")); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != "echo:ls" {
		t.Fatalf("echo = %q", got)
	}
}

// mode=repl must attach the host's singleton `main-repl` instead of opening a
// second shell: the kind/name/singleton triple is the contract with harness
// (pkg/console.StartPersistent).
func TestNodeTerminalReplAttachesSingletonMainRepl(t *testing.T) {
	opens := make(chan *pty.Open, 1)
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := ws.Upgrade(w, r)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var env pb.Envelope
			if proto.Unmarshal(raw, &env) != nil || env.Payload == nil {
				continue
			}
			var msg pty.ProtocolMessage
			if env.Payload.UnmarshalTo(&msg) != nil {
				continue
			}
			open, ok := msg.Message.(*pty.ProtocolMessage_Open)
			if !ok {
				continue
			}
			opens <- open.Open
			payload, _ := anypb.New(&pty.ProtocolMessage{Message: &pty.ProtocolMessage_Opened{
				Opened: &pty.Opened{StreamId: open.Open.GetStreamId()},
			}})
			raw, _ = proto.Marshal(&pb.Envelope{Id: open.Open.GetStreamId(), Payload: payload})
			_ = conn.WriteBinary(raw)
		}
	}))
	defer host.Close()

	s := New(nil, termExec{url: host.URL}, t.TempDir(), t.TempDir())
	mux := http.NewServeMux()
	s.Register(mux)
	front := httptest.NewServer(mux)
	defer front.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(front.URL, "http") +
		"/api/nodes/lab/terminal?cols=80&rows=24&mode=repl"
	conn, err := ws.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	select {
	case open := <-opens:
		if open.GetKind() != "repl" || open.GetName() != "main-repl" || !open.GetSingleton() {
			t.Fatalf("open = %+v, want repl/main-repl/singleton", open)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("host never saw the open frame")
	}
}

// An audit node is a local one-shot process: it has no host to dial, so the
// bridge refuses before upgrading instead of pretending to open a shell.
func TestNodeTerminalRefusesNodeWithoutHost(t *testing.T) {
	s := New(nil, nodeExec{}, t.TempDir(), t.TempDir())
	mux := http.NewServeMux()
	s.Register(mux)
	front := httptest.NewServer(mux)
	defer front.Close()

	res, err := http.Get(front.URL + "/api/nodes/audit/terminal")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "只有 AOP 宿主能开终端") {
		t.Fatalf("refusal = %d %s", res.StatusCode, body)
	}
}

// Closing the browser socket must detach a REPL (the host keeps the singleton
// conversation for the next page load) and close a shell (no leaked process).
func TestNodeTerminalDisconnectDetachesRepl(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode string
		want string
	}{
		{"repl detaches", "&mode=repl", "detach"},
		{"shell closes", "", "close"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seen := make(chan string, 4)
			host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := ws.Upgrade(w, r)
				if err != nil {
					return
				}
				defer conn.Close()
				for {
					raw, err := conn.ReadMessage()
					if err != nil {
						return
					}
					var env pb.Envelope
					if proto.Unmarshal(raw, &env) != nil || env.Payload == nil {
						continue
					}
					var msg pty.ProtocolMessage
					if env.Payload.UnmarshalTo(&msg) != nil {
						continue
					}
					switch v := msg.Message.(type) {
					case *pty.ProtocolMessage_Open:
						id := v.Open.GetStreamId()
						for _, reply := range []*pty.ProtocolMessage{
							{Message: &pty.ProtocolMessage_Opened{Opened: &pty.Opened{StreamId: id}}},
							{Message: &pty.ProtocolMessage_Output{Output: &pty.Output{StreamId: id, Data: []byte("ready")}}},
						} {
							payload, _ := anypb.New(reply)
							raw, _ := proto.Marshal(&pb.Envelope{Id: id, Payload: payload})
							_ = conn.WriteBinary(raw)
						}
					case *pty.ProtocolMessage_Detach:
						seen <- "detach"
					case *pty.ProtocolMessage_Close:
						seen <- "close"
					}
				}
			}))
			defer host.Close()

			s := New(nil, termExec{url: host.URL}, t.TempDir(), t.TempDir())
			mux := http.NewServeMux()
			s.Register(mux)
			front := httptest.NewServer(mux)
			defer front.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			url := "ws" + strings.TrimPrefix(front.URL, "http") +
				"/api/nodes/lab/terminal?cols=80&rows=24" + tc.mode
			conn, err := ws.Dial(ctx, url, nil)
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			// Wait for the PTY to be attached before hanging up, otherwise the
			// request is canceled while it is still opening.
			_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			if first, err := conn.ReadMessage(); err != nil || string(first) != "ready" {
				t.Fatalf("ready frame = %q err=%v", first, err)
			}
			_ = conn.Close()

			select {
			case got := <-seen:
				if got != tc.want {
					t.Fatalf("host saw %q, want %q", got, tc.want)
				}
			case <-time.After(3 * time.Second):
				t.Fatalf("host never saw %q", tc.want)
			}
		})
	}
}
