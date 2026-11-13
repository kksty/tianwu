package board

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

func TestTokenTotalsRetainUnknownUsageAndOldRuns(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	r, _ := s.AddRun(model.Run{Purpose: "commander"})
	_, _ = s.UpdateRun(r.ID, func(r *model.Run) { r.Status = model.RunDone; r.StartedAt = now.Add(-2 * time.Hour); r.Tokens = 40 })
	r, _ = s.AddRun(model.Run{})
	_, _ = s.UpdateRun(r.ID, func(r *model.Run) {
		r.Status = model.RunDone
		r.StartedAt = now.Add(-time.Minute)
		r.Tokens = 30
		r.Usage = model.Usage{Known: true, TotalTokens: 30, InputTokens: 20, OutputTokens: 10, Requests: 1}
	})
	u := Tokens(s, now)
	if u.TotalTokens != 70 || u.HourTokens != 30 || u.UnknownRuns != 1 || u.InputTokens != 20 || u.OutputTokens != 10 {
		t.Fatalf("usage=%+v", u)
	}
}
