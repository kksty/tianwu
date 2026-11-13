package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/kksty/tianwu/internal/aop/traffic"
	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
)

func TestHTTPObservationPersistsAndReadsThroughProjectAPIWithoutCreatingFacts(t *testing.T) {
	s, _ := newBindingServer(t)
	run, _ := s.Store.AddRun(model.Run{Purpose: "commander"})
	openTestJournal(t, s, run.ID)
	defer s.closeJournal(run.ID)
	flow := &traffic.Flow{Id: "1", Complete: true, Request: &traffic.HttpRequest{Method: "GET", Url: "https://example.test/path", Body: []byte{0, 255}}, Response: &traffic.HttpResponse{StatusCode: 200, Body: []byte("<script>untrusted</script>")}}
	if err := s.appendRunEvent(run.ID, executor.Event{Kind: "http", SessionID: "s1", NativeEventID: "e1", NativeFlow: flow}); err != nil {
		t.Fatal(err)
	}
	if len(s.Store.Snapshot().Facts) != 0 {
		t.Fatal("HTTP exchange promoted into fact")
	}
	rows := call(t, s, http.MethodGet, "/api/traffic/flows?q=example.test", nil)
	if rows.Code != 200 {
		t.Fatal(rows.Body.String())
	}
	var list struct {
		Flows []struct {
			Ref string `json:"ref"`
		} `json:"flows"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(rows.Body.Bytes(), &list); err != nil || list.Total != 1 {
		t.Fatalf("list: %s %v", rows.Body.String(), err)
	}
	detail := call(t, s, http.MethodGet, "/api/traffic/flow?ref="+url.QueryEscape(list.Flows[0].Ref), nil)
	if detail.Code != 200 {
		t.Fatal(detail.Body.String())
	}
	var body struct {
		Flow struct {
			Request struct {
				Body []byte `json:"body"`
			} `json:"request"`
		} `json:"flow"`
	}
	if err := json.Unmarshal(detail.Body.Bytes(), &body); err != nil || len(body.Flow.Request.Body) != 2 || body.Flow.Request.Body[1] != 255 {
		t.Fatal("binary bytes lost", err)
	}
	request := httptest.NewRequest("GET", "/api/traffic/flow?ref="+url.QueryEscape(list.Flows[0].Ref)+"&download=1", nil)
	rec := httptest.NewRecorder()
	s.httpFlow(rec, request)
	if rec.Header().Get("Content-Type") != "application/octet-stream" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("download can execute under console origin")
	}
	lines := readJournalFileLines(t, s.DataDir, run.ID)
	if len(lines) != 1 || lines[0]["flow_ref"] != list.Flows[0].Ref {
		t.Fatal("journal reference missing")
	}
	other, _ := newBindingServer(t)
	missing := call(t, other, http.MethodGet, "/api/traffic/flow?ref="+url.QueryEscape(list.Flows[0].Ref), nil)
	if missing.Code != 404 {
		t.Fatal("flow crossed project boundary")
	}
}
