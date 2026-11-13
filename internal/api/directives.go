package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// maxDirectiveRunes is one bound for every operator text entry point. The
// instruction object has the same 4000-rune limit; messages are smaller in
// practice, and a single number keeps the composer honest.
const maxDirectiveRunes = 4000

// directiveRequest is the one operator text entry point: a transient hint and a
// durable constraint are the same sentence with different lifetime, not two
// different panels. The server maps it onto the existing message/instruction/
// steer primitives; nothing new is stored.
type directiveRequest struct {
	Text           string `json:"text"`
	Target         string `json:"target"` // commander|project|intent
	IntentID       string `json:"intent_id"`
	Persistent     bool   `json:"persistent"`
	Steer          bool   `json:"steer"`
	IdempotencyKey string `json:"idempotency_key"`
}

// directiveResult tells the composer exactly what happened. Exactly one of
// Message/Instruction/RunID is set, matching Kind.
type directiveResult struct {
	Kind        string             `json:"kind"` // message|instruction|steer
	Target      string             `json:"target"`
	IntentID    string             `json:"intent_id,omitempty"`
	Persistent  bool               `json:"persistent"`
	Created     bool               `json:"created"`
	Delivery    string             `json:"delivery,omitempty"`
	Message     *model.Message     `json:"message,omitempty"`
	Instruction *model.Instruction `json:"instruction,omitempty"`
	RunID       string             `json:"run_id,omitempty"`
}

// operatorDirective is the single operator "下发" entry:
//
//	persistent=false, target=commander -> one-shot durable message to the commander
//	persistent=false, target=intent    -> one-shot durable message to that direction
//	persistent=false, steer=true       -> cancel that live turn and continue with the note
//	persistent=true,  target=project   -> project-scope instruction (all future dispatches)
//	persistent=true,  target=intent    -> instruction scoped to that direction subtree
//
// A one-shot hint is a message, never an instruction: it is delivered once and
// must not be re-injected into future runs. A persistent constraint is an
// instruction, versioned and revocable. Mixing the two was the source of the
// "test this weak password" hint polluting every later dispatch.
func (s *Server) operatorDirective(w http.ResponseWriter, r *http.Request) {
	var req directiveRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求体不是合法 JSON", "code": "directive_invalid"})
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" || len([]rune(text)) > maxDirectiveRunes {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": fmt.Sprintf("下发正文必须是 1–%d 字", maxDirectiveRunes), "code": "directive_invalid"})
		return
	}
	target := strings.ToLower(strings.TrimSpace(req.Target))
	switch target {
	case "commander", "project", "intent":
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "target 只能是 commander、project 或 intent", "code": "directive_invalid"})
		return
	}
	if target == "commander" && (req.Persistent || req.Steer) {
		// A constraint is project/direction state, not a private chat with one
		// model; and a one-shot steer has no turn to replace.
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "发给指挥官只能是一次性提示；持续约束请选「整个项目」或某个方向", "code": "directive_invalid"})
		return
	}
	if target == "project" && !req.Persistent {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "一次性提示必须指定某个收件人（指挥官或方向）；整个项目只用于持续约束", "code": "directive_invalid"})
		return
	}
	if req.Persistent && req.Steer {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "持续约束不会换向；换向只用一次性下发", "code": "directive_invalid"})
		return
	}
	intentID := strings.TrimSpace(req.IntentID)
	if target == "intent" && intentID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "target=intent 时必须带 intent_id", "code": "directive_invalid"})
		return
	}
	if target == "commander" {
		s.writeCommanderDirective(w, r, text, req.IdempotencyKey)
		return
	}
	if target == "project" {
		// Validation above guarantees persistent=true here.
		s.writePersistentDirective(w, r, text, "project", "", req.IdempotencyKey)
		return
	}
	targetIntent, ok := s.Store.GetIntent(intentID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "方向不存在：" + intentID, "code": "directive_target_missing"})
		return
	}
	if req.Persistent {
		s.writePersistentDirective(w, r, text, target, intentID, req.IdempotencyKey)
		return
	}
	if req.Steer {
		runID, err := s.SteerIntent(intentID, text, "")
		if err != nil {
			writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "code": "directive_steer_unavailable"})
			return
		}
		s.recordAction("steer", intentID, text)
		writeJSON(w, http.StatusAccepted, directiveResult{
			Kind: "steer", Target: target, IntentID: intentID, Created: true, RunID: runID,
		})
		return
	}
	s.writeIntentDirective(w, r, text, targetIntent, req.IdempotencyKey)
}

func (s *Server) projectID() string {
	if s.Project == nil {
		return ""
	}
	return s.Project.ID
}

func directiveMessageKey(kind, recipient, key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	return "directive:" + key + ":" + kind + ":" + recipient
}

// writeDirectiveMessageError maps the message-admission errors of one-shot
// directives: idempotency conflict is 409, a full queue is the existing 429
// backpressure shape, and anything else is a real 500. It reports whether the
// response was written (always, for this path).
func (s *Server) writeDirectiveMessageError(w http.ResponseWriter, recipient string, err error) bool {
	switch {
	case errors.Is(err, store.ErrIdempotencyConflict):
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "code": "directive_idempotency_conflict"})
	case errors.Is(err, store.ErrMessageBackpressure):
		s.writeBackpressure(w, recipient, err)
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
	return true
}

func (s *Server) writeCommanderDirective(w http.ResponseWriter, r *http.Request, text, idem string) {
	msg, isNew, err := s.Store.AddMessage(model.Message{
		ProjectID:         s.projectID(),
		FromParticipantID: collab.OperatorID,
		ToParticipantID:   collab.CommanderID,
		Kind:              "directive",
		Text:              text,
		Durable:           true,
		IdempotencyKey:    directiveMessageKey("commander", "commander", idem),
		Status:            model.MessageQueued,
	})
	if err != nil {
		s.writeDirectiveMessageError(w, collab.CommanderID, err)
		return
	}
	if isNew {
		msg = s.deliverToCommander(r.Context(), msg)
	}
	s.recordAction("directive", collab.CommanderID, text)
	writeJSON(w, http.StatusAccepted, directiveResult{
		Kind: "message", Target: "commander", Created: isNew, Delivery: msg.Status, Message: &msg,
	})
}

func (s *Server) writeIntentDirective(w http.ResponseWriter, r *http.Request, text string, target model.Intent, idem string) {
	if target.Status == model.StatusDone || target.Status == model.StatusFailed || target.Status == model.StatusParked {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "目标方向已结束（" + target.Status + "）：显式 retry/reopen 之后再下发，一次性提示不能复活它",
			"code":  "directive_target_finished",
		})
		return
	}
	msg, isNew, err := s.Store.AddMessage(model.Message{
		ProjectID:         s.projectID(),
		FromParticipantID: collab.OperatorID,
		ToParticipantID:   collab.ParticipantID(target.ID),
		ToGeneration:      target.Attempts,
		Kind:              "directive",
		Text:              text,
		Durable:           true,
		IdempotencyKey:    directiveMessageKey("intent", target.ID, idem),
		Status:            model.MessageQueued,
	})
	if err != nil {
		s.writeDirectiveMessageError(w, collab.ParticipantID(target.ID), err)
		return
	}
	if isNew {
		msg = s.deliverMessage(r.Context(), target, msg)
	}
	s.recordAction("directive", target.ID, text)
	writeJSON(w, http.StatusAccepted, directiveResult{
		Kind: "message", Target: "intent", IntentID: target.ID, Created: isNew, Delivery: msg.Status, Message: &msg,
	})
}

func (s *Server) writePersistentDirective(w http.ResponseWriter, r *http.Request, text, target, intentID, idem string) {
	key := strings.TrimSpace(idem)
	if key == "" {
		key = store.NewID("directive")
	}
	in, created, err := s.Store.CreateInstruction(model.Instruction{
		Text:           text,
		IntentID:       strings.TrimSpace(intentID),
		IdempotencyKey: key,
		CreatedBy:      collab.OperatorID,
	})
	if err != nil {
		s.instructionError(w, err)
		return
	}
	if created {
		s.deliverInstructionNotices(r.Context(), in)
	}
	code := http.StatusOK
	if created {
		code = http.StatusCreated
	}
	scope := "project"
	if in.IntentID != "" {
		scope = "intent"
	}
	s.recordAction("directive", in.IntentID, text)
	writeJSON(w, code, directiveResult{
		Kind: "instruction", Target: scope, IntentID: in.IntentID, Persistent: true, Created: created, Instruction: &in,
	})
}
