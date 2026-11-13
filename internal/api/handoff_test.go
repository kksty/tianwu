package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// handoffExec writes a facts.jsonl the way a real agent would and finishes.
type handoffExec struct {
	lines string
	file  string // optional real file to create inside the work dir
}

func (h *handoffExec) Name() string { return "handoff" }

func (h *handoffExec) Run(ctx context.Context, req executor.Request, emit func(executor.Event)) (executor.Result, error) {
	if h.file != "" {
		if err := os.WriteFile(filepath.Join(req.WorkDir, h.file), []byte("proof"), 0o644); err != nil {
			return executor.Result{ExitCode: -1}, err
		}
	}
	dir := filepath.Join(req.WorkDir, ".tw")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return executor.Result{ExitCode: -1}, err
	}
	if err := os.WriteFile(filepath.Join(dir, "facts.jsonl"), []byte(h.lines), 0o644); err != nil {
		return executor.Result{ExitCode: -1}, err
	}
	return executor.Result{ExitCode: 0, StopReason: "completed"}, nil
}

// TestRunKeepsAFactWhoseOnlySourceIsAURL is the end-to-end version of the D44
// fix: dispatch → handoff → ledger, with no file anywhere.
func TestRunKeepsAFactWhoseOnlySourceIsAURL(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	ex := &handoffExec{lines: `{"kind":"vuln","title":"只在线上","summary":"当时看得到","doc":"GET /admin","evidence":[{"url":"https://target/admin"}],"confidence":"observed"}` + "\n"}
	s := New(st, ex, dir, filepath.Join(dir, "cases"))

	in, err := st.CreateIntent(model.Intent{Title: "查 /admin", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartRun(in.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		got, ok := st.GetIntent(in.ID)
		return ok && got.Status == model.StatusDone
	})

	db := st.Snapshot()
	if len(db.Evidence) != 0 {
		t.Fatalf("a url must not become evidence: %+v", db.Evidence)
	}
	var found *model.Fact
	for i := range db.Facts {
		if db.Facts[i].Kind == "vuln" {
			found = &db.Facts[i]
		}
	}
	if found == nil {
		t.Fatalf("the fact was dropped: %+v", db.Facts)
	}
	if len(found.EvidenceIDs) != 0 {
		t.Fatalf("evidence ids = %v", found.EvidenceIDs)
	}
	want := model.Ref{Scheme: model.RefURL, Locator: "https://target/admin"}
	if len(found.Refs) != 1 || found.Refs[0] != want {
		t.Fatalf("refs = %+v, want [%+v]", found.Refs, want)
	}
}

// A file that exists is still evidence, and its origin is still recorded.
func TestRunKeepsEvidenceWhenTheFileIsThere(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	ex := &handoffExec{
		file:  "admin-200.html",
		lines: `{"kind":"vuln","title":"文件在","evidence":[{"file":"admin-200.html","url":"https://target/admin"}]}` + "\n",
	}
	s := New(st, ex, dir, filepath.Join(dir, "cases"))

	in, err := st.CreateIntent(model.Intent{Title: "查 /admin", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartRun(in.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		got, ok := st.GetIntent(in.ID)
		return ok && got.Status == model.StatusDone
	})

	db := st.Snapshot()
	var f *model.Fact
	for i := range db.Facts {
		if db.Facts[i].Kind == "vuln" {
			f = &db.Facts[i]
		}
	}
	if f == nil || len(f.EvidenceIDs) != 1 {
		t.Fatalf("fact = %+v evidence = %+v", f, db.Evidence)
	}
	if len(f.Refs) != 0 {
		t.Fatalf("a resolved file must not add refs: %+v", f.Refs)
	}
	e, ok := st.EvidenceByID(f.EvidenceIDs[0])
	if !ok || e.From != "url:https://target/admin" {
		t.Fatalf("evidence = %+v ok=%v", e, ok)
	}
}
