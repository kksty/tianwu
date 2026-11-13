package aop

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	pb "github.com/kksty/tianwu/internal/aop/pb"
	"github.com/kksty/tianwu/internal/aop/pty"
	"github.com/kksty/tianwu/internal/ws"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// fakePTYHost speaks just enough of the host side: it accepts one PTY stream,
// answers Open with Opened + a greeting, echoes Input and reports Resize.
func fakePTYHost(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/aop/application/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := ws.Upgrade(w, r)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		send := func(msg *pty.ProtocolMessage) {
			payload, _ := anypb.New(msg)
			raw, _ := proto.Marshal(&pb.Envelope{Id: ptyStream(msg), Payload: payload})
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
				send(&pty.ProtocolMessage{Message: &pty.ProtocolMessage_Opened{
					Opened: &pty.Opened{StreamId: id},
				}})
				send(&pty.ProtocolMessage{Message: &pty.ProtocolMessage_Output{
					Output: &pty.Output{StreamId: id, Data: []byte("hello")},
				}})
			case *pty.ProtocolMessage_Input:
				send(&pty.ProtocolMessage{Message: &pty.ProtocolMessage_Output{
					Output: &pty.Output{StreamId: v.Input.GetStreamId(), Data: append([]byte("echo:"), v.Input.GetData()...)},
				}})
			case *pty.ProtocolMessage_Resize:
				send(&pty.ProtocolMessage{Message: &pty.ProtocolMessage_Output{
					Output: &pty.Output{StreamId: v.Resize.GetStreamId(), Data: []byte("resize")},
				}})
			case *pty.ProtocolMessage_Close:
				send(&pty.ProtocolMessage{Message: &pty.ProtocolMessage_Closed{
					Closed: &pty.Closed{StreamId: v.Close.GetStreamId()},
				}})
				return
			}
		}
	})
	return httptest.NewServer(mux)
}

func nextOutput(t *testing.T, term *Terminal) string {
	t.Helper()
	select {
	case data := <-term.Output():
		return string(data)
	case err := <-term.Errors():
		t.Fatalf("terminal error: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("no terminal output")
	}
	return ""
}

func TestTerminalOpenWriteResizeClose(t *testing.T) {
	srv := fakePTYHost(t)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	term, err := OpenTerminal(ctx, srv.URL, "tok", "local", 80, 24)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got := nextOutput(t, term); got != "hello" {
		t.Fatalf("greeting = %q", got)
	}
	if err := term.Write([]byte("ls")); err != nil {
		t.Fatal(err)
	}
	if got := nextOutput(t, term); got != "echo:ls" {
		t.Fatalf("echo = %q", got)
	}
	if err := term.Resize(120, 40); err != nil {
		t.Fatal(err)
	}
	if got := nextOutput(t, term); got != "resize" {
		t.Fatalf("resize = %q", got)
	}
	term.Close()
	select {
	case <-term.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("terminal did not close")
	}
}

func TestTerminalRejectsBadNode(t *testing.T) {
	// A host that closes the stream is what a missing agent looks like.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := ws.Upgrade(w, r)
		if err != nil {
			return
		}
		_ = conn.Close()
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := OpenTerminal(ctx, srv.URL, "", "nope", 80, 24); err == nil {
		t.Fatal("expected an error for a closed stream")
	}
}
