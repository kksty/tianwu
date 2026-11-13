package ioa

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseEnvelope(t *testing.T) {
	valid := map[string]any{
		"version": 1, "event": "message", "id": "e1",
		"source": map[string]any{"session_id": "s0"},
		"target": map[string]any{"session_id": "s1"}, "interrupt": true,
	}
	env, err := ParseEnvelope(valid)
	if err != nil || env == nil || env.Event != EventMessage || !env.Interrupt {
		t.Fatalf("valid message = %+v/%v", env, err)
	}
	if env.Source.SessionID != "s0" || env.Target.SessionID != "s1" {
		t.Fatalf("addresses = %+v", env)
	}

	if env, err := ParseEnvelope(map[string]any{"hello": "ioA"}); env != nil || err != nil {
		t.Fatalf("non-protocol record = %+v/%v", env, err)
	}
	if _, err := ParseEnvelope(map[string]any{"version": 2, "event": "message"}); err == nil {
		t.Fatal("unknown version accepted")
	}
	if _, err := ParseEnvelope(map[string]any{"version": 1, "event": "message", "target": map[string]any{"bogus": "x"}}); err == nil {
		t.Fatal("unknown address field accepted")
	}
	if _, err := ParseEnvelope(map[string]any{"version": 1, "event": "message", "id": nil}); err == nil {
		t.Fatal("null protocol field accepted")
	}
	if _, err := ParseEnvelope(map[string]any{"version": 1, "event": "subagent.delegate", "id": "x"}); err == nil {
		t.Fatal("delegation without call_id/agent accepted")
	}
	if _, err := ParseEnvelope(map[string]any{"version": 1, "event": "task.result", "id": "x", "interrupt": true}); err == nil {
		t.Fatal("task.result interrupt accepted")
	}
}

type fakeIOA struct {
	after string
}

func (f *fakeIOA) server(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ioa/spaces":
			_, _ = w.Write([]byte(`[{"id":"sp-1","name":"default","nodes":[{"id":"agent-id","name":"local"}]}]`))
		case "/ioa/nodes":
			_, _ = w.Write([]byte(`[{"id":"web-id","name":"cyber.web"},{"id":"agent-id","name":"local"}]`))
		case "/ioa/spaces/sp-1/messages":
			f.after = r.URL.Query().Get("after")
			if f.after != "" {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			out := []map[string]any{
				{"id": "m1", "sender": "web-id", "content": map[string]any{"text": "first"},
					"meta": map[string]any{"version": 1, "event": "message", "id": "e1", "target": map[string]any{"session_id": "s1"}}},
				// Same stable envelope id through a different transport id: dedupe.
				{"id": "m2", "sender": "web-id", "content": map[string]any{"text": "retry"},
					"meta": map[string]any{"version": 1, "event": "message", "id": "e1"}},
				// Another application's record: visible in IOA, not harness work.
				{"id": "m3", "sender": "web-id", "content": map[string]any{"text": "foreign"}, "meta": map[string]any{"other.app": true}},
				// Claims to be protocol but is invalid: retained as rejected.
				{"id": "m4", "sender": "web-id", "content": map[string]any{"text": "bad"},
					"meta": map[string]any{"version": 99, "event": "message"}},
			}
			_ = json.NewEncoder(w).Encode(out)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestObserverDedupesAndPersistsCursor(t *testing.T) {
	fake := &fakeIOA{}
	srv := fake.server(t)
	defer srv.Close()

	dir := t.TempDir()
	client := New(srv.URL, "tok", "local")
	obs, err := NewObserver(client, filepath.Join(dir, "ioa"))
	if err != nil {
		t.Fatal(err)
	}
	n, err := obs.Step(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 || fake.after != "" {
		t.Fatalf("first step appended %d (after=%q), want 2", n, fake.after)
	}
	records, err := obs.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %d, want valid + rejected", len(records))
	}
	if records[0].Envelope == nil || records[0].Envelope.ID != "e1" {
		t.Fatalf("first record = %+v", records[0])
	}
	if records[1].Error == "" || records[1].Envelope != nil {
		t.Fatalf("rejected record = %+v", records[1])
	}
	if obs.Cursor() != "m4" {
		t.Fatalf("cursor = %q, want m4", obs.Cursor())
	}
	if _, err := os.Stat(filepath.Join(dir, "ioa", "cursor")); err != nil {
		t.Fatalf("cursor file: %v", err)
	}

	// A restart resumes at m4 and cannot re-append e1.
	obs2, err := NewObserver(client, filepath.Join(dir, "ioa"))
	if err != nil {
		t.Fatal(err)
	}
	if obs2.Cursor() != "m4" {
		t.Fatalf("reopened cursor = %q", obs2.Cursor())
	}
	n, err = obs2.Step(context.Background())
	if err != nil || n != 0 {
		t.Fatalf("second step = %d/%v, want 0", n, err)
	}
	if fake.after != "m4" {
		t.Fatalf("second read after = %q", fake.after)
	}
	records, err = obs2.Records()
	if err != nil || len(records) != 2 {
		t.Fatalf("records after restart = %d/%v", len(records), err)
	}
	if !strings.Contains(records[0].Content["text"].(string), "first") {
		t.Fatalf("content lost: %+v", records[0].Content)
	}
}

func TestReadUsesCursorAndLimit(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ioa/spaces":
			_, _ = w.Write([]byte(`[{"id":"sp-1","name":"default","nodes":[{"id":"agent-id","name":"local"}]}]`))
		case "/ioa/nodes":
			_, _ = w.Write([]byte(`[{"id":"web-id","name":"cyber.web"},{"id":"agent-id","name":"local"}]`))
		case "/ioa/spaces/sp-1/messages":
			gotQuery = r.URL.RawQuery
			_, _ = w.Write([]byte(`[{"id":"m9","sender":"web-id","content":{"text":"x"}}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "tok", "local")
	msgs, err := c.Read(context.Background(), "m8", 25)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].ID != "m9" || msgs[0].Content["text"] != "x" {
		t.Fatalf("messages = %+v", msgs)
	}
	if !strings.Contains(gotQuery, "after=m8") || !strings.Contains(gotQuery, "limit=25") {
		t.Fatalf("query = %q", gotQuery)
	}
}
