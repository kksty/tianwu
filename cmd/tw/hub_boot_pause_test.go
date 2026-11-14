package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/node"
	"github.com/kksty/tianwu/internal/project"
)

// A restart is an operator action, not permission to resume spending. Every
// saved project must come up paused even when its manifest said running, and
// the pause has to be persisted so a second restart cannot silently resume the
// engagement either. The runtime gate must agree with the manifest: an
// automatic commander round can start before an operator sees the console.
func TestStartPausesEverySavedProject(t *testing.T) {
	dir := t.TempDir()
	mgr, err := project.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, err := mgr.Create("acme", "acme.example")
	if err != nil {
		t.Fatal(err)
	}
	if p.IsPaused() {
		t.Fatal("precondition: a new project starts running")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := &hub{cfg: hubConfig{Nodes: &node.Registry{}}, mgr: mgr, ctx: ctx, kits: map[string]*kit{}}
	h.start()

	// Boot paused both the persisted state and the live runtime.
	if !p.IsPaused() {
		t.Fatal("boot did not pause the project manifest")
	}
	reopened, err := project.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reopened.Get(p.ID).IsPaused() {
		t.Fatal("boot pause was not persisted")
	}
	k := h.kits[p.ID]
	if k == nil || k.dec == nil {
		t.Fatal("boot did not attach a decider")
	}
	if !k.dec.Paused() {
		t.Fatal("commander is not paused after restart")
	}
	if reason := k.dec.Status().PausedReason; !strings.Contains(reason, "重启后") {
		t.Fatalf("paused reason = %q, want the restart explanation", reason)
	}
	// The dispatch gate is the second half of the pause: a round that somehow
	// slips past the decider must still be refused.
	rec := httptest.NewRecorder()
	k.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/decide", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("paused project accepted a decision round: %d %s", rec.Code, rec.Body.String())
	}

	// The operator explicitly resumes. A later restart must pause again, so
	// "paused after restart" is the start condition, not a one-time flag.
	if err := p.SetPaused(false); err != nil {
		t.Fatal(err)
	}
	next, err := project.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	nextCtx, nextCancel := context.WithCancel(context.Background())
	defer nextCancel()
	h2 := &hub{cfg: hubConfig{Nodes: &node.Registry{}}, mgr: next, ctx: nextCtx, kits: map[string]*kit{}}
	h2.start()
	if !next.Get(p.ID).IsPaused() {
		t.Fatal("a second restart did not pause the project again")
	}
}

// A commander-requested stop is a pause with a cause. A control-plane restart
// must not relabel it as an operator pause: the console's badge and the
// append-and-continue contract both switch on the persisted machine code, and
// "who stopped the engagement" is exactly the question the operator asks first.
func TestCommanderStandbyPauseKindSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	mgr, err := project.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, err := mgr.Create("acme", "acme.example")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.SetPausedWithKind(true, project.PauseKindCommanderStandby); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := &hub{cfg: hubConfig{Nodes: &node.Registry{}}, mgr: mgr, ctx: ctx, kits: map[string]*kit{}}
	h.start()

	k := h.kits[p.ID]
	if k == nil || k.dec == nil {
		t.Fatal("boot did not attach the paused project")
	}
	if !k.dec.Paused() {
		t.Fatal("commander is not paused after restart")
	}
	// The runtime cause must be restored from the manifest, not guessed from prose.
	rec := httptest.NewRecorder()
	k.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/board", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("board = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"project_pause_kind":"commander_standby"`) {
		t.Fatalf("board lost the commander pause kind: %s", rec.Body.String())
	}
	// The boot pause write must not overwrite the persisted cause either.
	reopened, err := project.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Get(p.ID).PauseKindOf(); got != project.PauseKindCommanderStandby {
		t.Fatalf("persisted pause kind = %q after boot, want %q", got, project.PauseKindCommanderStandby)
	}
}

// Runtime attachment (creating a project, re-opening it after edits) keeps the
// manifest state: only the boot path forces a pause. Otherwise a newly created
// project could never start without a confusing resume step.
func TestRuntimeAttachKeepsManifestPauseState(t *testing.T) {
	mgr, err := project.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := mgr.Create("acme", "acme.example")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := &hub{cfg: hubConfig{Nodes: &node.Registry{}}, mgr: mgr, ctx: ctx, kits: map[string]*kit{}}

	k := h.attach(p)
	if k == nil || k.dec == nil {
		t.Fatal("runtime attach did not build a decider")
	}
	if k.dec.Paused() {
		t.Fatal("runtime attach paused a running manifest")
	}
}
