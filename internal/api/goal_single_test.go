package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/model"
)

// The commander maintains one endpoint goal. A second goal is an operator
// decision (append-goal), so the agent surface must refuse it; the operator
// surface must keep working.
func TestAgentCannotCreateSecondGoal(t *testing.T) {
	s, _ := newBindingServer(t)
	if _, err := s.Store.AddGoal(model.Goal{Statement: "唯一终点：拿到一条可复核证据"}); err != nil {
		t.Fatal(err)
	}

	post := func(withAgent bool, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/goals", strings.NewReader(body))
		if withAgent {
			req = req.WithContext(WithIdentity(req.Context(), collab.Identity{
				ProjectID:     s.Project.ID,
				ParticipantID: collab.CommanderID,
			}))
		}
		s.createGoal(rec, req)
		return rec
	}

	agent := post(true, `{"statement":"第二个目标"}`)
	if agent.Code != http.StatusConflict {
		t.Fatalf("agent second goal status = %d, want 409: %s", agent.Code, agent.Body.String())
	}
	if !strings.Contains(agent.Body.String(), "goal_exists") {
		t.Fatalf("agent second goal lacks a structured code: %s", agent.Body.String())
	}
	if len(s.Store.Goals()) != 1 {
		t.Fatalf("agent second goal was written: %+v", s.Store.Goals())
	}

	operator := post(false, `{"statement":"操作员追加的目标"}`)
	if operator.Code != http.StatusCreated {
		t.Fatalf("operator goal status = %d, want 201: %s", operator.Code, operator.Body.String())
	}
	if len(s.Store.Goals()) != 2 {
		t.Fatalf("operator goal was not written: %+v", s.Store.Goals())
	}

	// A project without goals still lets the commander write the first one.
	empty, _ := newBindingServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/goals", strings.NewReader(`{"statement":"唯一终点"}`))
	req = req.WithContext(WithIdentity(req.Context(), collab.Identity{ProjectID: empty.Project.ID, ParticipantID: collab.CommanderID}))
	empty.createGoal(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first agent goal status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
}
