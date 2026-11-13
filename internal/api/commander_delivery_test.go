package api

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/decider"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

func TestCommanderDeliveryIsBoundBeforeExecutorStarts(t *testing.T) {
	s, ex, st := newCommanderServer(t)
	if _, err := st.AddGoal(model.Goal{Statement: "unfinished"}); err != nil {
		t.Fatal(err)
	}
	r := decider.NewRunner(st, nil, "")
	r.ModelName = "fixture"
	s.Decider = r
	r.Commander = func(ctx context.Context, prompt string, resume bool) (decider.RoundResult, error) {
		res, actions, err := s.StartCommander(ctx, prompt, resume)
		return decider.RoundResult{Tokens: res.Tokens, Actions: actions}, err
	}
	var deliveryID, previousRun string
	ex.during = func() {
		d, ok := st.PendingDelivery("commander")
		if !ok || d.LastShownRun == "" || d.LastShownRun == previousRun || d.Attempts != ex.runs {
			t.Fatalf("executor started without current presentation: %+v", d)
		}
		run, ok := st.GetRun(d.LastShownRun)
		if !ok || run.Purpose != "commander" || run.Status != model.RunRunning {
			t.Fatalf("binding does not name a running commander: %+v", run)
		}
		if deliveryID != "" && d.ID != deliveryID {
			t.Fatal("retry replaced delivery identity")
		}
		deliveryID, previousRun = d.ID, d.LastShownRun
	}
	for range 2 {
		if d := r.Run(context.Background(), "manual", true); d == nil || d.Error != "" || !d.AttentionDelivered {
			t.Fatalf("round failed: %+v", d)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(st.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	d, ok := reopened.PendingDelivery("commander")
	if !ok || d.ID != deliveryID || d.LastShownRun != previousRun || d.Attempts != 2 {
		t.Fatalf("restart lost presentation metadata: %+v", d)
	}
}

func TestCommanderDeliveryBindingWriteFailureStopsBeforeExecutor(t *testing.T) {
	s, ex, st := newCommanderServer(t)
	if _, err := st.AddGoal(model.Goal{Statement: "unfinished"}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", st.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TRIGGER fail_delivery_run BEFORE UPDATE ON meta
		WHEN json_extract(NEW.payload,'$.memory.deliveries.commander.last_shown_run') IS NOT NULL
		BEGIN SELECT RAISE(ABORT,'fixture delivery run failure'); END`); err != nil {
		t.Fatal(err)
	}
	r := decider.NewRunner(st, nil, "")
	r.ModelName = "fixture"
	s.Decider = r
	r.Commander = func(ctx context.Context, prompt string, resume bool) (decider.RoundResult, error) {
		_, _, err := s.StartCommander(ctx, prompt, resume)
		return decider.RoundResult{}, err
	}
	d := r.Run(context.Background(), "manual", true)
	if d == nil || !strings.Contains(d.Error, "fixture delivery run failure") || d.AttentionDelivered || ex.runs != 0 {
		t.Fatalf("binding failure called executor or consumed demand: %+v, calls=%d", d, ex.runs)
	}
	pending, ok := st.PendingDelivery("commander")
	if !ok || pending.LastShownRun != "" {
		t.Fatalf("failed binding leaked run reference: %+v", pending)
	}
	runs := st.Runs()
	if len(runs) != 1 || runs[0].Status != model.RunFailed {
		t.Fatalf("preparation failure left an active run: %+v", runs)
	}
	if _, err := db.Exec("DROP TRIGGER fail_delivery_run"); err != nil {
		t.Fatal(err)
	}
	if d := r.Run(context.Background(), "manual", true); d == nil || d.Error != "" || ex.runs != 1 {
		t.Fatalf("explicit retry did not recover: %+v", d)
	}
	got, _ := st.PendingDelivery("commander")
	if got.ID != pending.ID || got.LastShownRun == "" || got.LastShownRun == runs[0].ID {
		t.Fatalf("retry failed to bind the preserved page to its new run: %+v", got)
	}
}
