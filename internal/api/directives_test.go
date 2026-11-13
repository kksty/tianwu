package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/model"
)

// A one-shot hint to the commander is a message that wakes a round; it must not
// create an instruction row, or "test this weak password" would ride every
// future dispatch.
func TestOneShotCommanderDirectiveIsAMessageNotAnInstruction(t *testing.T) {
	s, p := newBindingServer(t)
	rec := call(t, s, http.MethodPost, "/api/directives", map[string]any{
		"text": "顺手测一下这个弱口令", "target": "commander", "idempotency_key": "hint-1",
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("directive = %d %s", rec.Code, rec.Body.String())
	}
	var out directiveResult
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Kind != "message" || out.Target != "commander" || out.Instruction != nil {
		t.Fatalf("one-shot commander result = %+v", out)
	}
	db := p.Store.Snapshot()
	if len(db.Instructions) != 0 {
		t.Fatalf("one-shot created an instruction: %+v", db.Instructions)
	}
	if len(db.Messages) != 1 || db.Messages[0].FromParticipantID != collab.OperatorID || !db.Messages[0].Durable {
		t.Fatalf("directive message = %+v", db.Messages)
	}
	// A retry with the same key and text is the same row, not a second one.
	again := call(t, s, http.MethodPost, "/api/directives", map[string]any{
		"text": "顺手测一下这个弱口令", "target": "commander", "idempotency_key": "hint-1",
	})
	if again.Code != http.StatusAccepted || !strings.Contains(again.Body.String(), `"created":false`) {
		t.Fatalf("idempotent retry = %d %s", again.Code, again.Body.String())
	}
	if len(p.Store.Snapshot().Messages) != 1 {
		t.Fatal("idempotent retry created a second message")
	}
	// A different payload under the same key is a conflict.
	diff := call(t, s, http.MethodPost, "/api/directives", map[string]any{
		"text": "换一句话", "target": "commander", "idempotency_key": "hint-1",
	})
	if diff.Code != http.StatusConflict {
		t.Fatalf("key reuse = %d %s", diff.Code, diff.Body.String())
	}
}

// A one-shot target direction gets exactly one message for that direction; it
// is not broadcast and it does not become a fact/intent/instruction.
func TestOneShotIntentDirectiveTargetsOnlyThatDirection(t *testing.T) {
	s, p := newBindingServer(t)
	a, _ := p.Store.CreateIntent(model.Intent{Title: "A"})
	b, _ := p.Store.CreateIntent(model.Intent{Title: "B"})
	rec := call(t, s, http.MethodPost, "/api/directives", map[string]any{
		"text": "只在这一条上试弱口令", "target": "intent", "intent_id": a.ID, "idempotency_key": "hint-a",
	})
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"delivery":"queued"`) {
		t.Fatalf("intent directive = %d %s", rec.Code, rec.Body.String())
	}
	if got := len(p.Store.PendingFor(collab.ParticipantID(a.ID))); got != 1 {
		t.Fatalf("target pending = %d", got)
	}
	if got := len(p.Store.PendingFor(collab.ParticipantID(b.ID))); got != 0 {
		t.Fatalf("unrelated direction received the hint: %d", got)
	}
	if len(p.Store.Snapshot().Instructions) != 0 || len(p.Store.Runs()) != 0 || len(p.Store.Snapshot().Facts) != 0 {
		t.Fatal("one-shot hint created an instruction/run/fact")
	}
	// A settled direction must be explicitly reopened; a hint cannot revive it.
	if _, err := p.Store.UpdateIntent(b.ID, func(i *model.Intent) { i.Status = model.StatusDone }); err != nil {
		t.Fatal(err)
	}
	settled := call(t, s, http.MethodPost, "/api/directives", map[string]any{
		"text": "别复活", "target": "intent", "intent_id": b.ID, "idempotency_key": "hint-b",
	})
	if settled.Code != http.StatusConflict || !strings.Contains(settled.Body.String(), "directive_target_finished") {
		t.Fatalf("settled direction = %d %s", settled.Code, settled.Body.String())
	}
}

// A persistent directive is a versioned instruction in the chosen scope. Its
// lifecycle (revoke, history, version CAS) stays the instruction lifecycle.
func TestPersistentDirectiveCreatesScopedInstruction(t *testing.T) {
	s, p := newBindingServer(t)
	in, _ := p.Store.CreateIntent(model.Intent{Title: "target"})

	rec := call(t, s, http.MethodPost, "/api/directives", map[string]any{
		"text": "整个项目从现在起只读优先", "target": "project", "persistent": true, "idempotency_key": "constraint-project",
	})
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"kind":"instruction"`) {
		t.Fatalf("project constraint = %d %s", rec.Code, rec.Body.String())
	}
	var project directiveResult
	_ = json.Unmarshal(rec.Body.Bytes(), &project)
	if project.Instruction == nil || project.Instruction.IntentID != "" {
		t.Fatalf("project instruction = %+v", project.Instruction)
	}

	scoped := call(t, s, http.MethodPost, "/api/directives", map[string]any{
		"text": "这条方向注意弱口令", "target": "intent", "intent_id": in.ID, "persistent": true, "idempotency_key": "constraint-intent",
	})
	if scoped.Code != http.StatusCreated {
		t.Fatalf("intent constraint = %d %s", scoped.Code, scoped.Body.String())
	}
	var intentResult directiveResult
	_ = json.Unmarshal(scoped.Body.Bytes(), &intentResult)
	if intentResult.Instruction == nil || intentResult.Instruction.IntentID != in.ID {
		t.Fatalf("intent instruction = %+v", intentResult.Instruction)
	}
	// Retry with the same key returns the original row (200, created=false).
	again := call(t, s, http.MethodPost, "/api/directives", map[string]any{
		"text": "这条方向注意弱口令", "target": "intent", "intent_id": in.ID, "persistent": true, "idempotency_key": "constraint-intent",
	})
	if again.Code != http.StatusOK || !strings.Contains(again.Body.String(), `"created":false`) {
		t.Fatalf("instruction retry = %d %s", again.Code, again.Body.String())
	}
	if got := len(p.Store.Snapshot().Instructions); got != 2 {
		t.Fatalf("instructions = %d, want 2", got)
	}
}

func TestDirectiveRejectsInvalidCombinations(t *testing.T) {
	s, p := newBindingServer(t)
	in, _ := p.Store.CreateIntent(model.Intent{Title: "idle"})
	cases := []struct {
		name string
		body map[string]any
		code int
		want string
	}{
		{"empty text", map[string]any{"text": " ", "target": "commander"}, 400, "directive_invalid"},
		{"bad target", map[string]any{"text": "x", "target": "everyone"}, 400, "directive_invalid"},
		{"persistent commander", map[string]any{"text": "x", "target": "commander", "persistent": true}, 400, "directive_invalid"},
		{"one-shot project", map[string]any{"text": "x", "target": "project"}, 400, "directive_invalid"},
		{"persistent steer", map[string]any{"text": "x", "target": "intent", "intent_id": in.ID, "persistent": true, "steer": true}, 400, "directive_invalid"},
		{"missing intent id", map[string]any{"text": "x", "target": "intent"}, 400, "directive_invalid"},
		{"unknown intent", map[string]any{"text": "x", "target": "intent", "intent_id": "in_missing"}, 404, "directive_target_missing"},
		{"steer idle", map[string]any{"text": "x", "target": "intent", "intent_id": in.ID, "steer": true}, 409, "directive_steer_unavailable"},
	}
	for _, tc := range cases {
		rec := call(t, s, http.MethodPost, "/api/directives", tc.body)
		if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.want) {
			t.Fatalf("%s = %d %s, want %d %s", tc.name, rec.Code, rec.Body.String(), tc.code, tc.want)
		}
	}
	if len(p.Store.Snapshot().Messages) != 0 || len(p.Store.Snapshot().Instructions) != 0 {
		t.Fatal("a rejected directive wrote a row")
	}
}
