package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/api"
	"github.com/kksty/tianwu/internal/decider"
	"github.com/kksty/tianwu/internal/ioa"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/project"
	"github.com/kksty/tianwu/internal/store"
)

func TestProjectIOAIncludesNativeMessagesAcrossRestartWithoutRedelivery(t *testing.T) {
	root := t.TempDir()
	mgr, err := project.Open(filepath.Join(root, "projects"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := mgr.Create("project", "")
	if err != nil {
		t.Fatal(err)
	}
	srv := api.New(p.Store, nil, root, p.CaseDir)
	in, _ := p.Store.CreateIntent(model.Intent{Title: "worker"})
	_, _ = p.Store.AddRun(model.Run{IntentID: in.ID, SessionID: "historical"})
	markerDir := filepath.Join(p.CaseDir, in.ID, ".tw")
	if err := os.MkdirAll(markerDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(markerDir, "aop-session.json"), []byte(`{"session":"current"}`), 0600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "ioa")
	obs, err := ioa.NewObserver(ioa.New("http://127.0.0.1:1", "", "local"), dir)
	if err != nil {
		t.Fatal(err)
	}
	var lines []byte
	for i, session := range []string{"historical", "current", "another-project"} {
		rec := ioa.Record{Seq: int64(i + 1), IOAMessageID: session, Envelope: &ioa.Envelope{Version: 1, Event: ioa.EventMessage, Source: ioa.Address{SessionID: session}}, Content: map[string]any{"text": "peer message"}}
		raw, _ := json.Marshal(rec)
		lines = append(append(lines, raw...), '\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), lines, 0600); err != nil {
		t.Fatal(err)
	}
	h := &hub{cfg: hubConfig{IOA: obs}}
	k := &kit{proj: p, srv: srv}
	rev := p.Store.Revision()
	for range 2 {
		w := httptest.NewRecorder()
		h.serveProjectIOA(w, httptest.NewRequest(http.MethodGet, "/api/ioa/events", nil), k)
		var got struct {
			Records  []ioa.Record      `json:"records"`
			Sessions map[string]string `json:"sessions"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || len(got.Records) != 2 || got.Sessions["historical"] != "worker" || got.Sessions["current"] != "worker" {
			t.Fatalf("bad projection: %s", w.Body.String())
		}
	}
	if p.Store.Revision() != rev || len(p.Store.Messages(0)) != 0 || len(p.Store.ListFacts("", 0)) != 0 {
		t.Fatal("observation mutated project")
	}
	w := httptest.NewRecorder()
	h.serveProjectIOA(w, httptest.NewRequest(http.MethodPost, "/api/ioa/events", nil), k)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatal("observation accepted a mutation")
	}
}

func TestNativeSchedulingFailureReplaysObservationAfterRestart(t *testing.T) {
	root := t.TempDir()
	mgr, err := project.Open(filepath.Join(root, "projects"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := mgr.Create("native retry", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Store.Close() })
	srv := api.New(p.Store, nil, root, p.CaseDir)
	dir := filepath.Join(srv.CommanderDir(), ".tw")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "aop-session.json"), []byte(`{"session":"commander-retry"}`), 0600); err != nil {
		t.Fatal(err)
	}
	runner := decider.NewRunner(p.Store, nil, "")
	h := &hub{kits: map[string]*kit{p.ID: {proj: p, srv: srv, dec: runner}}}
	live := false
	native := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ioa/spaces":
			fmt.Fprint(w, `[{"id":"space","name":"default"}]`)
		case "/ioa/nodes":
			fmt.Fprint(w, `[{"id":"web","name":"cyber.web"},{"id":"agent","name":"local"}]`)
		case "/ioa/spaces/space/messages":
			if live {
				fmt.Fprint(w, `[{"id":"mail","meta":{"version":1,"event":"message","id":"event","target":{"session_id":"commander-retry"}}}]`)
			} else {
				fmt.Fprint(w, `[]`)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer native.Close()
	logDir := filepath.Join(root, "ioa")
	observer, err := ioa.NewObserver(ioa.New(native.URL, "", "local"), logDir)
	if err != nil {
		t.Fatal(err)
	}
	observer.OnRecord = h.onNativeIOA
	if _, err := observer.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	path := p.Store.Path()
	if err := p.Store.Close(); err != nil {
		t.Fatal(err)
	}
	live = true
	if n, err := observer.Step(context.Background()); n != 1 || err == nil {
		t.Fatalf("ledger failure did not reach observer: n=%d err=%v", n, err)
	}
	p.Store, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	observer, err = ioa.NewObserver(ioa.New(native.URL, "", "local"), logDir)
	if err != nil {
		t.Fatal(err)
	}
	observer.OnRecord = h.onNativeIOA
	if _, err := observer.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a := p.Store.CommanderAttention(); len(a.Reasons) != 1 || a.Reasons[0].Code != "native_request" {
		t.Fatalf("restart lost native scheduling: %+v", a)
	}
	if err := p.Store.CompleteCommanderAttention(p.Store.CommanderAttention()); err != nil {
		t.Fatal(err)
	}
	// Simulate losing the observer receipt after the ledger has consumed mail.
	if err := os.Remove(filepath.Join(logDir, "notified")); err != nil {
		t.Fatal(err)
	}
	observer, err = ioa.NewObserver(ioa.New(native.URL, "", "local"), logDir)
	if err != nil {
		t.Fatal(err)
	}
	observer.OnRecord = h.onNativeIOA
	if _, err := observer.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a := p.Store.CommanderAttention(); len(a.Reasons) != 0 {
		t.Fatalf("callback retry revived consumed native mail: %+v", a)
	}
}

func TestDirectedNativeCommanderMailUsesPausedTrackedScheduler(t *testing.T) {
	root := t.TempDir()
	mgr, _ := project.Open(filepath.Join(root, "projects"))
	p, err := mgr.Create("native", "")
	if err != nil {
		t.Fatal(err)
	}
	srv := api.New(p.Store, nil, root, p.CaseDir)
	in, _ := p.Store.CreateIntent(model.Intent{Title: "open"})
	dir := filepath.Join(srv.CommanderDir(), ".tw")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "aop-session.json"), []byte(`{"session":"commander-native"}`), 0600); err != nil {
		t.Fatal(err)
	}
	r := decider.NewRunner(p.Store, nil, "")
	r.ModelName, r.Debounce, r.Heartbeat = "fixture", time.Millisecond, time.Hour
	calls := make(chan struct{}, 4)
	r.Commander = func(context.Context, string, bool) (decider.RoundResult, error) {
		calls <- struct{}{}
		return decider.RoundResult{}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Pause()
	r.Start(ctx)
	h := &hub{kits: map[string]*kit{p.ID: {proj: p, srv: srv, dec: r}}}
	mail := ioa.Record{Envelope: &ioa.Envelope{Version: 1, Event: ioa.EventMessage, Target: ioa.Address{SessionID: "commander-native"}}}
	h.onNativeIOA(mail)
	select {
	case <-calls:
		t.Fatal("paused native mail started a model")
	case <-time.After(20 * time.Millisecond):
	}
	r.Resume()
	for n := range 2 {
		mail.IOAMessageID = fmt.Sprintf("native-%d", n)
		h.onNativeIOA(mail)
		select {
		case <-calls:
		case <-time.After(time.Second):
			t.Fatal("native message was not scheduled")
		}
	}
	_, _ = p.Store.UpdateIntent(in.ID, func(in *model.Intent) { in.Status = model.StatusDone })
	h.onNativeIOA(mail)
	select {
	case <-calls:
		t.Fatal("native mail revived completed project")
	case <-time.After(20 * time.Millisecond):
	}
	if len(p.Store.Messages(0)) != 0 || len(p.Store.ListFacts("", 0)) != 0 {
		t.Fatal("native notification became ledger content")
	}
}

func TestInstanceIOAViewLabelsEveryProjectsSessions(t *testing.T) {
	root := t.TempDir()
	mgr, err := project.Open(filepath.Join(root, "projects"))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "ioa")
	obs, err := ioa.NewObserver(ioa.New("http://127.0.0.1:1", "", "local"), dir)
	if err != nil {
		t.Fatal(err)
	}
	rec := ioa.Record{Seq: 1, IOAMessageID: "m1", Envelope: &ioa.Envelope{Version: 1, Event: ioa.EventMessage, ID: "e1"}}
	raw, _ := json.Marshal(rec)
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	h := &hub{cfg: hubConfig{IOA: obs}, kits: make(map[string]*kit)}
	want := make(map[string]string)
	for _, spec := range []struct{ name, title, session string }{
		{"第一项目", "侦察方向", "session-one"},
		{"第二项目", "验证方向", "session-two"},
	} {
		p, err := mgr.Create(spec.name, "")
		if err != nil {
			t.Fatal(err)
		}
		srv := api.New(p.Store, nil, root, p.CaseDir)
		in, err := p.Store.CreateIntent(model.Intent{Title: spec.title})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.Store.AddRun(model.Run{IntentID: in.ID, SessionID: spec.session}); err != nil {
			t.Fatal(err)
		}
		h.kits[p.ID] = &kit{proj: p, srv: srv}
		want[spec.session] = spec.title
	}
	recorder := httptest.NewRecorder()
	h.serveIOAEvents(recorder, httptest.NewRequest(http.MethodGet, "/api/ioa/events", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("instance view: %d %s", recorder.Code, recorder.Body.String())
	}
	var got struct {
		Records  []ioa.Record      `json:"records"`
		Sessions map[string]string `json:"sessions"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Records) != 1 {
		t.Fatalf("instance view lost records: %s", recorder.Body.String())
	}
	for id, label := range want {
		if got.Sessions[id] != label {
			t.Fatalf("instance view cannot label %s: sessions=%+v", id, got.Sessions)
		}
	}
}

func TestArchivedProfileIOAIsVisibleWithoutStartingAHost(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "ioa", "hosts", "archived-profile")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	rec := ioa.Record{Seq: 1, IOAMessageID: "archived", Envelope: &ioa.Envelope{Version: 1, Event: ioa.EventMessage, ID: "archived-event"}}
	raw, _ := json.Marshal(rec)
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	h := &hub{cfg: hubConfig{DataDir: root}}
	rows, err := h.nativeIOARecords()
	if err != nil || len(rows) != 1 || rows[0].IOAMessageID != "archived" {
		t.Fatalf("archived observations: %v %v", rows, err)
	}
	if len(h.ioaSources) != 0 {
		t.Fatal("read materialized an observer/host")
	}
}
