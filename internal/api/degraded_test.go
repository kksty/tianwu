package api

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/host"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// Degraded start (D101): the console serves while the local host's provider is
// not ready, but dispatch into that host is refused with one actionable error
// instead of a run that dies in milliseconds inside the harness.
func TestDegradedLocalHostRefusesDispatch(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, &blockingExec{}, dir, filepath.Join(dir, "cases"))
	s.LocalNode = "local"
	ready := false
	s.LocalHostStatus = func() host.HostStatus {
		return host.HostStatus{Ready: ready, Online: true, LastError: "API error (402): Insufficient Balance"}
	}
	in, err := st.CreateIntent(model.Intent{Title: "宿主未就绪"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.CancelAll()
		s.WaitIdle(5 * time.Second)
		time.Sleep(150 * time.Millisecond)
	})

	if _, err := s.StartRun(in.ID); err == nil || !strings.Contains(err.Error(), "本机宿主未就绪") {
		t.Fatalf("dispatch on a degraded host = %v, want the degraded refusal", err)
	}
	ready = true
	runID, err := s.StartRun(in.ID)
	if err != nil {
		t.Fatalf("dispatch after the host became ready: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool {
		return s.Lifecycle.RunActive(runID)
	})
}

// The commander is a purpose run on the same local host, so D101's gate must
// cover it too: dispatching a round while the host is restarting burned a
// failed round whose harness error ("agent provider is nil") told the operator
// nothing actionable (D111).
func TestCommanderRefusesAnUnreadyLocalHost(t *testing.T) {
	s, ex, st := newCommanderServer(t)
	s.LocalNode = "local"
	ready := false
	s.LocalHostStatus = func() host.HostStatus {
		return host.HostStatus{Ready: ready, Online: true, LastError: "API error (402): Insufficient Balance"}
	}

	if _, _, err := s.StartCommander(context.Background(), "## 本轮", false); err == nil ||
		!strings.Contains(err.Error(), "Insufficient Balance") {
		t.Fatalf("commander round on a degraded host = %v, want the degraded refusal", err)
	}
	if ex.runs != 0 {
		t.Fatalf("harness executor ran %d time(s) while the host was not ready", ex.runs)
	}
	if runs := st.Runs(); len(runs) != 0 {
		t.Fatalf("refused round left run rows: %+v", runs)
	}

	ready = true
	if _, _, err := s.StartCommander(context.Background(), "## 本轮", false); err != nil {
		t.Fatalf("commander round after the host became ready: %v", err)
	}
	if ex.runs != 1 {
		t.Fatalf("harness executor ran %d time(s), want 1", ex.runs)
	}
}

func TestNamedCommanderProfileDoesNotDependOnDefaultModelReadiness(t *testing.T) {
	s, ex := commanderWithProfiles(t, &usageCommanderExec{}, &ProfileDefaults{Strong: "strong"})
	s.LocalNode = "local"
	s.LocalHostStatus = func() host.HostStatus {
		return host.HostStatus{Online: true, Ready: false, LastError: "default model unavailable"}
	}
	if err := s.CheckCommanderHost(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.StartCommander(context.Background(), "round", false); err != nil {
		t.Fatal(err)
	}
	if ex.req.Profile != "strong" {
		t.Fatal("default host substituted for selection")
	}
}
