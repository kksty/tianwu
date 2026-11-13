package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/access"
	"github.com/kksty/tianwu/internal/aop/command"
	pb "github.com/kksty/tianwu/internal/aop/pb"
	"github.com/kksty/tianwu/internal/ws"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

func TestOccupancyRemainsReadableAndCannotBeReturnedDuringNativeOperation(t *testing.T) {
	started, finish := make(chan struct{}), make(chan struct{})
	var once sync.Once
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := ws.Upgrade(w, r)
		if err != nil {
			return
		}
		defer conn.Close()
		send := func(reply string, msg proto.Message) {
			payload, _ := anypb.New(msg)
			raw, _ := proto.Marshal(&pb.Envelope{Id: "fixture", ReplyTo: reply, Payload: payload})
			_ = conn.WriteBinary(raw)
		}
		for {
			raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var env pb.Envelope
			if proto.Unmarshal(raw, &env) != nil {
				return
			}
			var cmd command.CommandProtocolMessage
			if env.Payload.UnmarshalTo(&cmd) == nil && cmd.GetRequest() != nil {
				close(started)
				<-finish
				send(env.Id, &command.CommandProtocolMessage{Message: &command.CommandProtocolMessage_Receipt{Receipt: &command.CommandReceipt{SessionId: "control", State: "running"}}})
				detail := protowire.AppendTag(nil, 1, protowire.BytesType)
				detail = protowire.AppendString(detail, cmd.GetRequest().Line)
				event := &pb.Event{SessionId: "control", Extensions: []*anypb.Any{{TypeUrl: "type.googleapis.com/cyber.agent.CommandDetail", Value: detail}}, Payload: &pb.Event_Message{Message: &pb.Message{Role: "assistant", Content: []*pb.Content{{Value: &pb.Content_Text{Text: &pb.TextContent{Text: "https://example.test/current"}}}}}}}
				send("", &pb.ProtocolMessage{Message: &pb.ProtocolMessage_Event{Event: event}})
				continue
			}
			var core pb.ProtocolMessage
			if env.Payload.UnmarshalTo(&core) != nil {
				return
			}
			if core.GetOpenSessionRequest() != nil {
				send(env.Id, &pb.ProtocolMessage{Message: &pb.ProtocolMessage_OpenSessionResponse{OpenSessionResponse: &pb.OpenSessionResponse{Outcome: &pb.OpenSessionResponse_Accepted{Accepted: &pb.Session{Id: "control", NodeId: "local"}}}}})
			}
		}
	}))
	defer func() { once.Do(func() { close(finish) }); host.Close() }()
	s, entry, _, _ := leaseFixture(t)
	s.Exec, s.LocalNode = termExec{url: host.URL}, "lab"
	id := leaseID(t, leaseCall(t, s, entry, nil, map[string]any{"action": "acquire"}))
	mux := http.NewServeMux()
	s.Register(mux)
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		mux.ServeHTTP(response, httptest.NewRequest("POST", "/api/access/"+entry.ID+"/browser", strings.NewReader(`{"action":"inspect","lease_id":"`+id+`"}`)))
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("native command did not start")
	}
	read := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/access", nil))
		read <- rec
	}()
	select {
	case rec := <-read:
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"busy":true`) {
			t.Fatalf("occupancy = %d %s", rec.Code, rec.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("occupancy read blocked on browser I/O")
	}
	if rec := leaseCall(t, s, entry, nil, map[string]any{"action": "release", "lease_id": id}); rec.Code != 409 {
		t.Fatal("returned a browser with an active command")
	}
	if s.WaitIdle(0) {
		t.Fatal("project deletion can race an active browser command")
	}
	once.Do(func() { close(finish) })
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("native command did not finish")
	}
	if response.Code != 200 {
		t.Fatalf("native response = %d %s", response.Code, response.Body.String())
	}
	current, err := access.Get(s.CaseRoot, entry.ID)
	if err != nil || current.Lease == nil || current.Lease.ID != id {
		t.Fatalf("operation lost ownership: %+v %v", current, err)
	}
	if !s.WaitIdle(0) {
		t.Fatal("completed browser command kept project busy")
	}
}
