package aopexec

import (
	"strings"
	"testing"

	pb "github.com/kksty/tianwu/internal/aop/pb"
	"github.com/kksty/tianwu/internal/aop/tool"
	"google.golang.org/protobuf/types/known/anypb"
)

func TestArtifactEventRetainsOriginalOutsideJournalJSON(t *testing.T) {
	value := &tool.Artifact{ResultId: "result-1", Tool: "neutron", Kind: "finding", Data: []byte("private native bytes")}
	ext, err := anypb.New(value)
	if err != nil {
		t.Fatal(err)
	}
	ev := event(&pb.Event{Id: "event-1", Payload: &pb.Event_Extension{Extension: ext}})
	if ev.Native == nil || ev.Native.ResultId != "result-1" || string(ev.Native.Data) != "private native bytes" || ev.NativeEventID != "event-1" || ev.Kind != "artifact" {
		t.Fatalf("native artifact lost: %+v", ev)
	}
	if strings.Contains(string(ev.Raw), "private native bytes") || strings.Contains(string(ev.Raw), "aop.tool.Artifact") {
		t.Fatal("raw artifact duplicated into journal")
	}
}
