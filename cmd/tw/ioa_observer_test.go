package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/kksty/tianwu/internal/ioa"
	"github.com/kksty/tianwu/internal/project"
)

func TestIOALifecycleRemainsAnObservationWithoutChangingProjectFacts(t *testing.T) {
	root := t.TempDir()
	mgr, err := project.Open(filepath.Join(root, "projects"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := mgr.Create("IOA 观察", "")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "ioa")
	obs, err := ioa.NewObserver(ioa.New("http://127.0.0.1:1", "", "local"), dir)
	if err != nil {
		t.Fatal(err)
	}
	record := ioa.Record{Seq: 1, IOAMessageID: "m1", Envelope: &ioa.Envelope{
		Version: 1, Event: ioa.EventSubagentDelegate, ID: "e1", CallID: "call-1",
		Source: ioa.Address{SessionID: "parent"}, Agent: &ioa.Agent{Name: "scout"},
	}}
	raw, _ := json.Marshal(record)
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	h := &hub{cfg: hubConfig{IOA: obs}, kits: map[string]*kit{p.ID: {proj: p}}}
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		h.serveIOAEvents(rec, httptest.NewRequest(http.MethodGet, "/api/ioa/events", nil))
		var got struct {
			Records []ioa.Record `json:"records"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if rec.Code != 200 || len(got.Records) != 1 || got.Records[0].Envelope.ID != "e1" {
			t.Fatalf("observer record was lost: %s", rec.Body.String())
		}
		if facts := p.Store.ListFacts("", 50); len(facts) != 0 {
			t.Fatalf("runtime lifecycle became project facts: %+v", facts)
		}
	}
}
