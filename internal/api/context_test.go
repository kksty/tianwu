package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

func TestRunContextReadsCase(t *testing.T) {
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	in, err := st.CreateIntent(model.Intent{Title: "测试方向"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := st.AddRun(model.Run{ID: "run_x", IntentID: in.ID, Status: model.RunDone, StopReason: "completed", Tokens: 42})
	if err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(root, in.ID)
	tw := filepath.Join(dir, ".tw")
	if err := os.MkdirAll(tw, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(tw, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("prompt.txt", "## 角色\n测试")
	write("steer-01.txt", "补充一句")
	write("graph.json", `{"facts":[{"id":"f1"}]}`)
	write("facts.jsonl", "{\"kind\":\"vuln\",\"title\":\"a\"}\nnot-json\n{\"kind\":\"cred\",\"title\":\"b\"}\n")
	write("ask.jsonl", "{\"question\":\"要不要打 3306\"}\n")
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := &Server{Store: st, CaseRoot: root}
	req := httptest.NewRequest(http.MethodGet, "/api/runs/"+run.ID+"/context", nil)
	req.SetPathValue("id", run.ID)
	rec := httptest.NewRecorder()
	srv.runContext(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		RunID      string            `json:"run_id"`
		IntentID   string            `json:"intent_id"`
		Status     string            `json:"status"`
		StopReason string            `json:"stop_reason"`
		Prompt     string            `json:"prompt"`
		Steer      []string          `json:"steer"`
		Graph      json.RawMessage   `json:"graph"`
		Declared   []json.RawMessage `json:"declared"`
		Asks       []json.RawMessage `json:"asks"`
		Files      []contextFile     `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.RunID != run.ID || got.IntentID != in.ID || got.Status != run.Status || got.StopReason != "completed" {
		t.Fatalf("identity/status = %+v", got)
	}
	if got.Prompt == "" || len(got.Steer) != 1 || len(got.Graph) == 0 {
		t.Fatalf("input files missing: prompt=%q steer=%v graph=%q", got.Prompt, got.Steer, got.Graph)
	}
	if len(got.Declared) != 2 {
		t.Fatalf("declared = %d lines, want 2 valid", len(got.Declared))
	}
	if len(got.Asks) != 1 {
		t.Fatalf("asks = %d", len(got.Asks))
	}
	names := map[string]bool{}
	for _, f := range got.Files {
		names[f.Name] = true
	}
	if !names["notes.md"] || !names[".tw/prompt.txt"] {
		t.Fatalf("files = %+v", got.Files)
	}
}

func TestRunContextUnknownRun(t *testing.T) {
	srv := &Server{Store: nil}
	req := httptest.NewRequest(http.MethodGet, "/api/runs/nope/context", nil)
	req.SetPathValue("id", "nope")
	rec := httptest.NewRecorder()
	// A nil store must not panic the handler; the test store is cheap to build,
	// so use a real empty one.
	srv.Store, _ = store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	srv.runContext(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d", rec.Code)
	}
}
