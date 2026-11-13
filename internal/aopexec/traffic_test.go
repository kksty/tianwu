package aopexec

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/kksty/tianwu/internal/aop/operation"
	pb "github.com/kksty/tianwu/internal/aop/pb"
	"github.com/kksty/tianwu/internal/aop/traffic"
	"google.golang.org/protobuf/types/known/anypb"
)

func TestHTTPObservationPreservesBodyAndTypedOperationOutsideJournal(t *testing.T) {
	body := []byte("private response")
	payload, _ := anypb.New(&traffic.Flow{Id: "flow-1", Request: &traffic.HttpRequest{Method: "GET", Url: "https://example.test/"}, Response: &traffic.HttpResponse{StatusCode: 200, Body: body}})
	ref, _ := anypb.New(&operation.Ref{CallId: "call-1", OperationId: "op-1", Correlation: operation.Correlation_CORRELATION_EXPLICIT})
	ev := event(&pb.Event{Id: "ev-1", SessionId: "session-1", Extensions: []*anypb.Any{ref}, Payload: &pb.Event_Extension{Extension: payload}})
	if ev.Kind != "http" || ev.NativeFlow == nil || !bytes.Equal(ev.NativeFlow.Response.Body, body) || ev.NativeOperation.GetCallId() != "call-1" {
		t.Fatalf("observation lost: %+v", ev)
	}
	b, err := json.Marshal(ev)
	if err != nil || bytes.Contains(b, body) || bytes.Contains(b, []byte("cHJpdmF0ZSByZXNwb25zZQ==")) {
		t.Fatal("body duplicated into journal")
	}
}

func TestHTTPObservationRequiresExplicitSessionAndTurnOwnership(t *testing.T) {
	payload, _ := anypb.New(&traffic.Flow{Id: "1"})
	for _, test := range []struct {
		session, turn string
		want          bool
	}{
		{"s1", "t1", true}, {"", "", false}, {"s1", "", false}, {"s1", "old", false}, {"other", "t1", false},
	} {
		ev := &pb.Event{SessionId: test.session, TurnId: test.turn, Payload: &pb.Event_Extension{Extension: payload}}
		if got := belongsToTurn(ev, "s1", "t1"); got != test.want {
			t.Fatalf("session=%q turn=%q: %v", test.session, test.turn, got)
		}
	}
}
