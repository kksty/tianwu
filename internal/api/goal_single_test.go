package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/collab"
)

// Goals are operator-defined: a harness session (commander or executor) only
// does the testing, so it cannot create or rewrite the endpoint. The operator
// surface still can, and it carries the operator's starting point.
func TestAgentCannotCreateGoal(t *testing.T) {
	s, _ := newBindingServer(t)

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

	agent := post(true, `{"statement":"指挥官自建的终点","start":"简报"}`)
	if agent.Code != http.StatusForbidden {
		t.Fatalf("agent goal status = %d, want 403: %s", agent.Code, agent.Body.String())
	}
	if !strings.Contains(agent.Body.String(), "goal_operator_only") {
		t.Fatalf("agent refusal lacks a structured code: %s", agent.Body.String())
	}
	if len(s.Store.Goals()) != 0 {
		t.Fatalf("agent goal was written: %+v", s.Store.Goals())
	}

	operator := post(false, `{"statement":"拿到一条可复核证据","start":"已有一个测试账号"} `)
	if operator.Code != http.StatusCreated {
		t.Fatalf("operator goal status = %d, want 201: %s", operator.Code, operator.Body.String())
	}
	goals := s.Store.Goals()
	if len(goals) != 1 {
		t.Fatalf("operator goal was not written: %+v", goals)
	}
	if goals[0].Start != "已有一个测试账号" || goals[0].Statement != "拿到一条可复核证据" {
		t.Fatalf("operator goal lost start/statement: %+v", goals[0])
	}
	if goals[0].QuantifiedTarget != "" {
		t.Fatalf("a new goal must not carry a quantified target: %+v", goals[0])
	}

	// Appending another goal is also an operator decision; the operator route is
	// separate (append-goal), but the plain create stays available to the
	// operator surface as before.
	second := post(false, `{"statement":"第二个终点"}`)
	if second.Code != http.StatusCreated || len(s.Store.Goals()) != 2 {
		t.Fatalf("operator second goal = %d / %d goals: %s", second.Code, len(s.Store.Goals()), second.Body.String())
	}
}
