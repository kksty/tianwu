package ioa

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSendResolvesSpaceAndTargetsAgent(t *testing.T) {
	var gotPath, gotAuth, gotNode string
	var got sendMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ioa/spaces":
			_, _ = w.Write([]byte(`[{"id":"sp-1","name":"default","nodes":[{"id":"agent-id","name":"local"}]}]`))
		case "/ioa/nodes":
			_, _ = w.Write([]byte(`[{"id":"web-id","name":"cyber.web"},{"id":"agent-id","name":"local"}]`))
		case "/ioa/spaces/sp-1/messages":
			gotPath, gotAuth, gotNode = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("X-Node-ID")
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Errorf("decode: %v", err)
			}
			_, _ = w.Write([]byte(`{"id":"msg-1"}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "tok", "local")
	id, err := c.SendEvent(context.Background(), "aop-session", "看登录口", true, "evt-stable-1")
	if err != nil {
		t.Fatal(err)
	}
	if id != "msg-1" || gotPath != "/ioa/spaces/sp-1/messages" {
		t.Fatalf("id=%q path=%q", id, gotPath)
	}
	if gotAuth != "Bearer tok" || gotNode != "web-id" {
		t.Fatalf("auth=%q node=%q", gotAuth, gotNode)
	}
	if got.Content["text"] != "看登录口" {
		t.Fatalf("content=%v", got.Content)
	}
	if got.Refs == nil || len(got.Refs.Nodes) != 1 || got.Refs.Nodes[0] != "agent-id" {
		t.Fatalf("refs=%+v", got.Refs)
	}
	if got.Meta["version"] != float64(1) || got.Meta["event"] != "message" || got.Meta["id"] != "evt-stable-1" {
		t.Fatalf("meta=%v", got.Meta)
	}
	target, _ := got.Meta["target"].(map[string]any)
	if target["session_id"] != "aop-session" || got.Meta["interrupt"] != true {
		t.Fatalf("meta=%v", got.Meta)
	}
}

func TestSendRejectsMissingAgent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ioa/spaces":
			_, _ = w.Write([]byte(`[{"id":"sp","name":"default"}]`))
		case "/ioa/nodes":
			_, _ = w.Write([]byte(`[{"id":"web-id","name":"cyber.web"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	if _, err := New(srv.URL, "tok", "missing").Send(context.Background(), "s", "x", false); err == nil {
		t.Fatal("expected missing agent error")
	}
}

// A host restart can change the IOA space id. The cached value used to be kept
// forever, so every say failed with 404 until tw itself restarted.
func TestSendReResolvesAfterTheHostRestarts(t *testing.T) {
	spaceID := "sp-1"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/ioa/spaces":
			_, _ = w.Write([]byte(`[{"id":"` + spaceID + `","name":"default"}]`))
		case r.URL.Path == "/ioa/nodes":
			_, _ = w.Write([]byte(`[{"id":"web-id","name":"cyber.web"},{"id":"agent-id","name":"local"}]`))
		case r.URL.Path == "/ioa/spaces/"+spaceID+"/messages":
			_, _ = w.Write([]byte(`{"id":"msg-` + spaceID + `"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "tok", "local")
	if id, err := c.Send(context.Background(), "s1", "first", false); err != nil || id != "msg-sp-1" {
		t.Fatalf("first send = %q, %v", id, err)
	}

	spaceID = "sp-2" // the host came back with a new database
	if id, err := c.Send(context.Background(), "s2", "second", false); err != nil || id != "msg-sp-2" {
		t.Fatalf("send after restart = %q, %v", id, err)
	}
}

func TestSendResultCarriesStableIDAndChain(t *testing.T) {
	var got sendMessage
	spaceID := "sp-r"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ioa/spaces":
			_, _ = w.Write([]byte(`[{"id":"` + spaceID + `","name":"default"}]`))
		case "/ioa/nodes":
			_, _ = w.Write([]byte(`[{"id":"web-id","name":"cyber.web"},{"id":"agent-id","name":"local"}]`))
		case "/ioa/spaces/" + spaceID + "/messages":
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Errorf("decode: %v", err)
			}
			_, _ = w.Write([]byte(`{"id":"msg-r"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "tok", "local")
	id, err := c.SendResult(context.Background(), "sess-r", "result text", "evt-result-1", []string{"task-a", "task-b"})
	if err != nil || id != "msg-r" {
		t.Fatalf("SendResult = %q/%v", id, err)
	}
	if got.Meta["event"] != "task.result" || got.Meta["id"] != "evt-result-1" {
		t.Fatalf("meta = %v", got.Meta)
	}
	chain, ok := got.Meta["chain"].([]any)
	if !ok || len(chain) != 2 || chain[0] != "task-a" || chain[1] != "task-b" {
		t.Fatalf("chain = %v", got.Meta["chain"])
	}
	if _, err := c.SendResult(context.Background(), "sess-r", "x", "", nil); err == nil {
		t.Fatal("SendResult without a stable id accepted")
	}
}
