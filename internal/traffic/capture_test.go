package traffic

import (
	"encoding/json"
	"os"
	"testing"

	native "github.com/kksty/tianwu/internal/aop/traffic"
	"google.golang.org/protobuf/proto"
)

func captureFixture() *native.Flow {
	return &native.Flow{Id: "flow-1", Complete: true, Request: &native.HttpRequest{Method: "GET", Url: "https://example.test/first"}, Response: &native.HttpResponse{StatusCode: 200, Body: []byte("original")}}
}

func TestMissingCommittedFlowBodyDoesNotAuthorizeReplacingItsObservation(t *testing.T) {
	dir := t.TempDir()
	flow := captureFixture()
	r, err := SaveCapture(dir, "run-1", "intent-1", "session-1", "event-1", flow, nil)
	if err != nil {
		t.Fatal(err)
	}
	path := captureBase(dir, r.Ref)
	before, _ := os.ReadFile(path + ".json")
	if err := os.Remove(path + ".pb"); err != nil {
		t.Fatal(err)
	}
	flow.Request.Url = "https://example.test/different"
	if _, err := SaveCapture(dir, "run-1", "intent-1", "session-1", "event-1", flow, nil); err == nil {
		t.Fatal("missing body silently replaced a committed observation")
	}
	after, _ := os.ReadFile(path + ".json")
	if string(before) != string(after) {
		t.Fatal("committed metadata changed")
	}
}

func TestReadingAFlowRejectsAnOriginalWithAnotherIdentity(t *testing.T) {
	dir := t.TempDir()
	flow := captureFixture()
	r, err := SaveCapture(dir, "run-1", "intent-1", "session-1", "event-1", flow, nil)
	if err != nil {
		t.Fatal(err)
	}
	flow.Id = "another-flow"
	b, _ := proto.Marshal(flow)
	if err := os.WriteFile(captureBase(dir, r.Ref)+".pb", b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadCapture(dir, r.Ref); err == nil {
		t.Fatal("wrong original displayed under recorded identity")
	}
}

func TestReadingAFlowRejectsSameLengthBodyDamage(t *testing.T) {
	dir := t.TempDir()
	flow := captureFixture()
	r, err := SaveCapture(dir, "run-1", "intent-1", "session-1", "event-1", flow, nil)
	if err != nil {
		t.Fatal(err)
	}
	flow.Response.Body = []byte("tampered")
	b, _ := proto.Marshal(flow)
	if err := os.WriteFile(captureBase(dir, r.Ref)+".pb", b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadCapture(dir, r.Ref); err == nil {
		t.Fatal("changed body was served as the original observation")
	}
}

func TestReadingLegacyFlowStillChecksOriginalIdentity(t *testing.T) {
	dir := t.TempDir()
	flow := captureFixture()
	r, err := SaveCapture(dir, "run-1", "intent-1", "session-1", "event-1", flow, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.PayloadSHA256 = ""
	b, _ := json.Marshal(r)
	if err := os.WriteFile(captureBase(dir, r.Ref)+".json", b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadCapture(dir, r.Ref); err != nil {
		t.Fatal("legacy observation became unreadable", err)
	}
	flow.Id = "other"
	b, _ = proto.Marshal(flow)
	if err := os.WriteFile(captureBase(dir, r.Ref)+".pb", b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadCapture(dir, r.Ref); err == nil {
		t.Fatal("legacy mismatched body accepted")
	}
}
