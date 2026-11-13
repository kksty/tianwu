package api

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/decider"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/project"
	"github.com/kksty/tianwu/internal/store"
)

// The decider bounds a commander round's wall clock (Runner.RoundTimeout). The
// cut is our policy, not the executor failing, so the round must be recorded as
// canceled with its own cause — the console and the fleet must not read it as a
// model error or a project stop (D91).
func TestCommanderRoundTimeoutIsRecordedAsACause(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	ex := &blockingExec{}
	s := New(st, ex, dir, filepath.Join(dir, "cases"))
	s.Project = &project.Project{Dir: dir, Manifest: project.Manifest{ID: "default"}}
	t.Cleanup(func() {
		s.CancelAll()
		s.WaitIdle(5 * time.Second)
	})

	ctx, cancel := context.WithTimeoutCause(context.Background(), 40*time.Millisecond,
		fmt.Errorf("%w（%s）", decider.ErrRoundTimeout, "40ms"))
	defer cancel()
	if _, _, err := s.StartCommander(ctx, "## 本轮", false); err != nil {
		t.Fatalf("a cut round must not be reported as an executor error: %v", err)
	}

	runs := st.Runs()
	if len(runs) != 1 {
		t.Fatalf("runs = %+v", runs)
	}
	r := runs[0]
	if r.Purpose != "commander" {
		t.Fatalf("purpose = %q", r.Purpose)
	}
	if r.Status != model.RunCanceled {
		t.Fatalf("status = %q, want canceled", r.Status)
	}
	if r.CancelCause != model.CancelCauseRoundTimeout {
		t.Fatalf("cancel cause = %q, want %q", r.CancelCause, model.CancelCauseRoundTimeout)
	}
	if !strings.Contains(r.ExitReason, "墙钟") {
		t.Fatalf("exit reason does not explain the cut: %q", r.ExitReason)
	}
}
