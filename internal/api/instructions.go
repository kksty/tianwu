package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/retrieval"
	"github.com/kksty/tianwu/internal/store"
)

func instructionSection(rows []model.Instruction, runID string) string {
	var b strings.Builder
	b.WriteString(executor.InstructionSection(rows))
	fmt.Fprintf(&b, "当前 run_id=%s。处理后可 POST /api/instructions/{id}/ack，携带 version 和 run_id；确认表示已处理这版指令，不代表动作已执行或结论成立。\n", runID)
	return b.String()
}

func (s *Server) instructionError(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	if errors.Is(err, store.ErrInstructionInvalid) {
		code = http.StatusBadRequest
	}
	if errors.Is(err, store.ErrInstructionConflict) {
		code = http.StatusConflict
	}
	if errors.Is(err, store.ErrInstructionNotFound) {
		code = http.StatusNotFound
	}
	writeErr(w, code, err.Error())
}

func operatorInstructionWrite(w http.ResponseWriter, r *http.Request) bool {
	_, participant := identityOf(r)
	if participant || actorOf(r) != collab.OperatorID {
		http.NotFound(w, r)
		return false
	}
	return true
}

func (s *Server) createInstruction(w http.ResponseWriter, r *http.Request) {
	if !operatorInstructionWrite(w, r) {
		return
	}
	var req struct {
		Text            string `json:"text"`
		IntentID        string `json:"intent_id"`
		SourceMessageID string `json:"source_message_id"`
		IdempotencyKey  string `json:"idempotency_key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeErr(w, 400, "指令请求不是有效 JSON")
		return
	}
	in, created, err := s.Store.CreateInstruction(model.Instruction{Text: req.Text, IntentID: req.IntentID, SourceMessageID: req.SourceMessageID, IdempotencyKey: req.IdempotencyKey, CreatedBy: collab.OperatorID})
	if err != nil {
		s.instructionError(w, err)
		return
	}
	s.deliverInstructionNotices(r.Context(), in)
	code := http.StatusOK
	if created {
		code = http.StatusCreated
	}
	writeJSON(w, code, map[string]any{"instruction": in, "created": created, "notifications": s.Store.InstructionNotifications(in.ID, in.Version)})
}

func (s *Server) updateInstruction(w http.ResponseWriter, r *http.Request) {
	if !operatorInstructionWrite(w, r) {
		return
	}
	var req struct {
		Version uint64 `json:"version"`
		Text    string `json:"text"`
		State   string `json:"state"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeErr(w, 400, "指令请求不是有效 JSON")
		return
	}
	in, err := s.Store.UpdateInstruction(r.PathValue("id"), req.Version, req.Text, req.State)
	if err != nil {
		s.instructionError(w, err)
		return
	}
	s.deliverInstructionNotices(r.Context(), in)
	writeJSON(w, 200, in)
}

func (s *Server) listInstructions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	intent, commander := q.Get("intent"), true
	if intent != "" {
		commander = false
	}
	if id, ok := identityOf(r); ok {
		intent, commander = id.IntentID, false
	}
	out, err := retrieval.Instructions(s.Store, intent, q.Get("state"), q.Get("cursor"), limit, commander)
	if err != nil {
		retrievalError(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) getInstruction(w http.ResponseWriter, r *http.Request) {
	db := s.Store.Snapshot()
	for _, in := range db.Instructions {
		if in.ID != r.PathValue("id") {
			continue
		}
		if id, ok := identityOf(r); ok && !store.InstructionAppliesTo(db, in, id.IntentID) {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, 200, in)
		return
	}
	http.NotFound(w, r)
}

func (s *Server) instructionHistory(w http.ResponseWriter, r *http.Request) {
	if id, ok := identityOf(r); ok {
		found := false
		db := s.Store.Snapshot()
		for _, in := range db.Instructions {
			if in.ID == r.PathValue("id") && store.InstructionAppliesTo(db, in, id.IntentID) {
				found = true
			}
		}
		if !found {
			http.NotFound(w, r)
			return
		}
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	out, err := retrieval.History(s.Store, "instruction", r.PathValue("id"), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		retrievalError(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) acknowledgeInstruction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Version uint64 `json:"version"`
		RunID   string `json:"run_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "确认请求不是有效 JSON")
		return
	}
	if id, ok := identityOf(r); ok {
		if req.RunID != "" && req.RunID != id.RunID {
			writeErr(w, 403, "只能确认自己的当前运行")
			return
		}
		req.RunID = id.RunID
	} else {
		run, live := s.liveCommanderRun()
		if !live || run.ID != req.RunID {
			writeErr(w, 409, "只能由当前指挥官运行确认指令")
			return
		}
	}
	if err := s.Store.AcknowledgeInstruction(req.RunID, r.PathValue("id"), req.Version); err != nil {
		s.instructionError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"acknowledged": true, "run_id": req.RunID, "version": req.Version})
}

func (s *Server) deliverInstructionNotices(ctx context.Context, in model.Instruction) {
	// A live round can finish before draining IOA. Preserve one tracked decision
	// opportunity for the committed control change; Pause still gates admission.
	if s.Decider != nil {
		s.Decider.Wake("operator")
	}
	for _, msg := range s.Store.InstructionNotifications(in.ID, in.Version) {
		if msg.Status != model.MessageQueued {
			continue
		}
		if msg.ToParticipantID == collab.CommanderID {
			s.deliverToCommander(ctx, msg)
			continue
		}
		if s.paused.Load() || s.closing.Load() {
			continue
		}
		if id, ok := collab.IntentIDFromParticipant(msg.ToParticipantID); ok {
			if target, found := s.Store.GetIntent(id); found {
				s.deliverMessage(ctx, target, msg)
			}
		}
	}
}
