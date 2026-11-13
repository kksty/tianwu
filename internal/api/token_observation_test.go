package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/model"
)

func TestLargeTokenHistoryDoesNotRestrictWorkerOrCommander(t *testing.T) {
	s, p := newBindingServer(t)
	s.Exec = &recordExec{}
	r, _ := p.Store.AddRun(model.Run{})
	_, _ = p.Store.UpdateRun(r.ID, func(r *model.Run) { r.Status = model.RunDone; r.StartedAt = time.Now(); r.Tokens = 100000000 })
	in, _ := p.Store.CreateIntent(model.Intent{Title: "new work"})
	id, err := s.StartRun(in.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, p, id)
	if _, _, err = s.StartCommander(context.Background(), "manual round", false); err != nil {
		t.Fatal(err)
	}
	rec := call(t, s, http.MethodGet, "/api/board", nil)
	var body map[string]json.RawMessage
	if err = json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["budget"]; ok {
		t.Fatal("budget still exposed")
	}
	if _, ok := body["usd_budget"]; ok {
		t.Fatal("money budget still exposed")
	}
	if _, ok := body["usage"]; !ok {
		t.Fatal("token observation missing")
	}
}
