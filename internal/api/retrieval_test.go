package api

import (
	"context"
	"encoding/json"
	"github.com/kksty/tianwu/internal/aopexec"
	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/retrieval"
	"github.com/kksty/tianwu/internal/store"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProjectReadsShareFilteringPagingAndHistory(t *testing.T) {
	dir := t.TempDir()
	st, err := store.OpenSQLite(filepath.Join(dir, "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a, _ := st.CreateIntent(model.Intent{Title: "a"})
	b, _ := st.CreateIntent(model.Intent{Title: "b"})
	f, _ := st.AddFact(model.Fact{IntentID: a.ID, Kind: "note", Title: "shared remote", Doc: "old body"})
	_, _ = st.AddFact(model.Fact{IntentID: b.ID, Kind: "note", Title: "shared remote"})
	s := New(st, nil, dir, dir)
	mux := http.NewServeMux()
	s.Register(mux)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest("GET", "/api/facts?q=shared&kind=note&intent="+a.ID+"&limit=1", nil))
	var page retrieval.FactPage
	if err = json.Unmarshal(rr.Body.Bytes(), &page); err != nil || rr.Code != 200 || len(page.Facts) != 1 || page.Total != 1 || page.Facts[0].ID != f.ID {
		t.Fatalf("filter=%s %v", rr.Body.String(), err)
	}
	before := st.ContentHash()
	_, _ = st.UpdateFact(f.ID, func(f *model.Fact) { f.Doc = "new body" })
	if st.ContentHash() == before {
		t.Fatal("doc revision did not stale content")
	}
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest("GET", "/api/facts/"+f.ID+"/history", nil))
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "old body") || !strings.Contains(rr.Body.String(), "new body") {
		t.Fatalf("history=%s", rr.Body.String())
	}
}

func TestToolOutputCanReadTheMiddleBeyondPreview(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(filepath.Join(dir, "ledger.json"))
	run, _ := st.AddRun(model.Run{IntentID: "in_fixture"})
	s := New(st, nil, dir, dir)
	if err := os.MkdirAll(filepath.Join(dir, "runs"), 0700); err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("a", 1200) + "critical-middle-observation" + strings.Repeat("z", 900)
	raw, _ := json.Marshal(map[string]any{"tool_result": map[string]any{"output": []any{map[string]any{"text": map[string]any{"text": text}}}}})
	ev := executor.Event{Kind: executor.KindToolResult, CallID: "call_fixture", Text: "short preview", Raw: raw}
	body, _ := json.Marshal(ev)
	if err := os.WriteFile(filepath.Join(dir, "runs", run.ID+".jsonl"), append(body, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.Register(mux)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest("GET", "/api/runs/"+run.ID+"/output?call_id=call_fixture&offset=1200&limit=27", nil))
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "critical-middle-observation") {
		t.Fatalf("output=%s", rr.Body.String())
	}
}

func TestCleanupUsesNativeInboxWithoutExtendingDeadline(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(filepath.Join(dir, "ledger.json"))
	in, _ := st.CreateIntent(model.Intent{Title: "fixture"})
	_, run, _ := st.StartIntentRun(in.ID, "local", "", model.Run{})
	work := filepath.Join(dir, in.ID)
	writeSayMarker(t, work)
	s := New(st, nil, dir, dir)
	// The last quarter includes session-file I/O and a durable SQLite write.
	// Give that work room on loaded Windows runners while still asserting that
	// delivery happens within the original, unextended hard deadline.
	s.Timeout = 2 * time.Second
	sent := make(chan string, 1)
	s.SayEvent = func(ctx context.Context, in model.Intent, sess aopexec.SessionInfo, text string, interrupt bool, id string) (string, error) {
		if !interrupt || id != "cleanup:"+run.ID {
			t.Error("wrong native delivery")
		}
		sent <- text
		return "accepted", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.Timeout)
	defer cancel()
	stop := s.startCleanupWindow(ctx, run.ID, in.ID, work)
	defer stop()
	select {
	case text := <-sent:
		if !strings.Contains(text, "事实交接契约") {
			t.Fatal("lost handoff contract")
		}
	case <-ctx.Done():
		t.Fatal("cleanup was not delivered before hard deadline")
	}
	<-ctx.Done()
	if ctx.Err() != context.DeadlineExceeded {
		t.Fatal("deadline changed")
	}
}
