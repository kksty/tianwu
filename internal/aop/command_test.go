package aop

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/aop/command"
	pb "github.com/kksty/tianwu/internal/aop/pb"
	"github.com/kksty/tianwu/internal/ws"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

func TestNativeCommandWaitsForMatchingCompletionWithoutModelTurn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := ws.Upgrade(w, r)
		if err != nil {
			return
		}
		defer conn.Close()
		send := func(reply string, value proto.Message) {
			payload, _ := anypb.New(value)
			raw, _ := proto.Marshal(&pb.Envelope{Id: "response", ReplyTo: reply, Payload: payload})
			_ = conn.WriteBinary(raw)
		}
		for {
			raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var env pb.Envelope
			if proto.Unmarshal(raw, &env) != nil {
				continue
			}
			var cmd command.CommandProtocolMessage
			if env.Payload.UnmarshalTo(&cmd) == nil && cmd.GetRequest() != nil {
				line := cmd.GetRequest().Line
				send(env.Id, &command.CommandProtocolMessage{Message: &command.CommandProtocolMessage_Receipt{Receipt: &command.CommandReceipt{SessionId: "s1", OperationId: "op1", State: "running"}}})
				for _, text := range []string{"!unrelated", ""} {
					if text == "" {
						text = line
					}
					detail := protowire.AppendTag(nil, 1, protowire.BytesType)
					detail = protowire.AppendString(detail, text)
					ev := &pb.Event{SessionId: "s1", Extensions: []*anypb.Any{{TypeUrl: "type.googleapis.com/cyber.agent.CommandDetail", Value: detail}}, Payload: &pb.Event_Message{Message: &pb.Message{Role: "assistant", Content: []*pb.Content{{Value: &pb.Content_Text{Text: &pb.TextContent{Text: "native returned"}}}}}}}
					send("", &pb.ProtocolMessage{Message: &pb.ProtocolMessage_Event{Event: ev}})
				}
				continue
			}
			var core pb.ProtocolMessage
			if env.Payload.UnmarshalTo(&core) != nil {
				continue
			}
			if core.GetWatchEventsRequest() == nil {
				t.Errorf("unexpected non-native request: %T", core.Message)
				return
			}
		}
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := New(srv.URL, "").Dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.Watch(ctx, "s1", ""); err != nil {
		t.Fatal(err)
	}
	result, err := conn.Command(ctx, "s1", "!playwright sessions")
	if err != nil || !strings.Contains(result, "native returned") {
		t.Fatalf("native completion %q %v", result, err)
	}
}
