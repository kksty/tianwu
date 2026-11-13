package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
)

type identityExec struct {
	ident  collab.Identity
	read   bool
	exists bool
	err    error
}

func (e *identityExec) Name() string { return "identity-recorder" }

func (e *identityExec) Run(_ context.Context, req executor.Request, _ func(executor.Event)) (executor.Result, error) {
	path := filepath.Join(req.WorkDir, ".tw", "identity.json")
	data, err := os.ReadFile(path)
	if err != nil {
		e.err = err
		return executor.Result{ExitCode: 0, StopReason: "completed"}, nil
	}
	e.exists = true
	e.err = json.Unmarshal(data, &e.ident)
	e.read = true
	return executor.Result{ExitCode: 0, StopReason: "completed"}, nil
}

// A dispatched run gets a fresh participant token scoped to that run; the file
// is gone and the token is revoked when the run settles.
func TestDispatchIssuesAndRevokesRunIdentity(t *testing.T) {
	s, p := newBindingServer(t)
	if err := p.SetNodeBinding([]string{"local"}, "local"); err != nil {
		t.Fatal(err)
	}
	s.LocalNode = "local"
	s.APIBase = "http://127.0.0.1:8080"
	s.Collab = collab.NewRegistry(time.Hour)
	ex := &identityExec{}
	s.Exec = ex
	in, err := p.Store.CreateIntent(model.Intent{Title: "查", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	runID, err := s.StartRun(in.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, p, runID)

	if !ex.read || ex.err != nil {
		t.Fatalf("identity read=%v err=%v exists=%v", ex.read, ex.err, ex.exists)
	}
	if ex.ident.ProjectID != p.ID || ex.ident.IntentID != in.ID || ex.ident.RunID != runID ||
		ex.ident.ParticipantID != collab.ParticipantID(in.ID) || ex.ident.Generation != 1 ||
		ex.ident.APIBase != s.APIBase || ex.ident.Token == "" {
		t.Fatalf("identity = %+v", ex.ident)
	}
	if _, ok := s.Collab.Resolve(ex.ident.Token); ok {
		t.Fatal("token still resolves after the run settled")
	}
	if _, err := os.Stat(filepath.Join(p.CaseDir, in.ID, ".tw", "identity.json")); !os.IsNotExist(err) {
		t.Fatalf("identity file survived the run: %v", err)
	}
}

// The token's generation is the claim count, so a message addressed to the
// previous holder is rejected even if the participant id is the same.
func TestRunIdentityGenerationFollowsAttempts(t *testing.T) {
	s, p := newBindingServer(t)
	if err := p.SetNodeBinding([]string{"local"}, "local"); err != nil {
		t.Fatal(err)
	}
	s.LocalNode = "local"
	s.Collab = collab.NewRegistry(time.Hour)
	ex := &identityExec{}
	s.Exec = ex
	in, err := p.Store.CreateIntent(model.Intent{Title: "查", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Store.UpdateIntent(in.ID, func(i *model.Intent) { i.Attempts = 4 }); err != nil {
		t.Fatal(err)
	}
	runID, err := s.StartRun(in.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, p, runID)
	if ex.ident.Generation != 5 {
		t.Fatalf("generation = %d, want claim count 5", ex.ident.Generation)
	}
}

func TestStaleFinishPreservesReplacementIdentity(t *testing.T) {
	s, p := newBindingServer(t)
	s.Collab = collab.NewRegistry(time.Hour)
	in, err := p.Store.CreateIntent(model.Intent{Title: "replacement identity"})
	if err != nil {
		t.Fatal(err)
	}
	_, old, err := p.Store.StartIntentRun(in.ID, "local", "", model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Store.SettleIntentForRun(in.ID, old.ID, model.RunCanceled, model.CancelCauseRetry); err != nil {
		t.Fatal(err)
	}
	_, current, err := p.Store.StartIntentRun(in.ID, "local", "", model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := s.Collab.Issue(collab.Identity{ProjectID: p.ID, IntentID: in.ID, ParticipantID: collab.ParticipantID(in.ID), RunID: current.ID})
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(p.CaseDir, in.ID)
	if err := s.publishRunIdentity(work, identity); err != nil {
		t.Fatal(err)
	}
	if err := s.publishRunIdentity(work, collab.Identity{IntentID: in.ID, RunID: old.ID}); err == nil {
		t.Fatal("stale dispatcher overwrote replacement identity")
	}
	s.finish(old.ID, in.ID, model.RunCanceled, "late old completion", executor.Result{}, 0)
	raw, err := os.ReadFile(identityPath(work))
	if err != nil {
		t.Fatalf("old finish removed replacement identity: %v", err)
	}
	var got collab.Identity
	if err := json.Unmarshal(raw, &got); err != nil || got.RunID != current.ID || got.Token != identity.Token {
		t.Fatalf("replacement identity changed: run=%s err=%v", got.RunID, err)
	}
	if _, ok := s.Collab.Resolve(identity.Token); !ok {
		t.Fatal("replacement credential was revoked")
	}
}
