package store

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/model"
)

var (
	ErrInstructionConflict = errors.New("指令版本或幂等键冲突")
	ErrInstructionNotFound = errors.New("指令不存在")
	ErrInstructionInvalid  = errors.New("指令请求无效")
)

const MaxInstructionRunes = 4000
const MaxActiveInstructionBytes = 64 << 10

// ApplicableInstructions is a projection, not a second instruction store. The
// ancestry comes from recorded delegation, including children created later.
func ApplicableInstructions(db DB, intentID string, commander bool) []model.Instruction {
	ancestors := instructionAncestors(db, intentID)
	out := []model.Instruction{}
	for _, in := range db.Instructions {
		if in.State == model.InstructionActive && (commander || in.IntentID == "" || ancestors[in.IntentID]) {
			out = append(out, in)
		}
	}
	return out
}

func InstructionAppliesTo(db DB, in model.Instruction, intentID string) bool {
	if in.IntentID == "" {
		return true
	}
	return instructionAncestors(db, intentID)[in.IntentID]
}

func instructionAncestors(db DB, intentID string) map[string]bool {
	ancestors := map[string]bool{}
	parents := map[string]string{}
	for _, in := range db.Intents {
		parents[in.ID] = in.ParentID
	}
	for id := intentID; id != "" && !ancestors[id]; id = parents[id] {
		ancestors[id] = true
	}
	return ancestors
}

func instructionVersions(rows []model.Instruction) map[string]uint64 {
	out := map[string]uint64{}
	for _, in := range rows {
		out[in.ID] = in.Version
	}
	return out
}

func (s *Store) Instructions() []model.Instruction {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]model.Instruction{}, s.db.Instructions...)
}

func (s *Store) CreateInstruction(in model.Instruction) (model.Instruction, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in.Text, in.IdempotencyKey = strings.TrimSpace(in.Text), strings.TrimSpace(in.IdempotencyKey)
	if in.Text == "" || len([]rune(in.Text)) > MaxInstructionRunes || in.IdempotencyKey == "" || in.CreatedBy == "" {
		return model.Instruction{}, false, fmt.Errorf("%w：需要作者、幂等键和 1–4000 字的正文", ErrInstructionInvalid)
	}
	for _, old := range s.db.Instructions {
		if old.CreatedBy == in.CreatedBy && old.IdempotencyKey == in.IdempotencyKey {
			// Compare the original request, not the subsequently revised text.
			for _, c := range s.db.Memory.History {
				if c.Kind == "instruction" && c.ID == old.ID && c.Version == 1 {
					var original DB
					if err := appendRow(&original, "instruction", c.Value); err != nil {
						return model.Instruction{}, false, err
					}
					o := original.Instructions[0]
					if o.Text == in.Text && o.IntentID == in.IntentID && o.SourceMessageID == in.SourceMessageID {
						return old, false, nil
					}
					return model.Instruction{}, false, ErrInstructionConflict
				}
			}
			return model.Instruction{}, false, ErrInstructionConflict
		}
	}
	if in.IntentID != "" {
		if _, ok := s.intentLocked(in.IntentID); !ok {
			return model.Instruction{}, false, fmt.Errorf("%w：作用域意图不存在", ErrInstructionInvalid)
		}
	}
	if in.SourceMessageID != "" {
		found := false
		for _, m := range s.db.Messages {
			if m.ID == in.SourceMessageID && m.FromParticipantID == in.CreatedBy {
				found = true
			}
		}
		if !found {
			return model.Instruction{}, false, fmt.Errorf("%w：来源消息不存在或不是该作者的消息", ErrInstructionInvalid)
		}
	}
	if err := s.instructionCapacityLocked("", in.Text, in.IntentID); err != nil {
		return model.Instruction{}, false, err
	}
	now := time.Now()
	in.ID, in.Version, in.State, in.CreatedAt, in.UpdatedAt = NewID("instruction"), 1, model.InstructionActive, now, now
	s.db.Instructions = append(s.db.Instructions, in)
	s.queueInstructionNoticesLocked(in)
	if err := s.flush("instruction", "message", "run"); err != nil {
		return model.Instruction{}, false, err
	}
	return in, true, nil
}

func (s *Store) instructionCapacityLocked(except, text, target string) error {
	// Include reference/scope/version framing so many short instructions cannot
	// bypass the context capacity merely by keeping their bodies tiny.
	n := len(text) + len(target) + 256
	for _, in := range s.db.Instructions {
		if in.ID != except && in.State == model.InstructionActive {
			n += len(in.Text) + len(in.IntentID) + 256
		}
	}
	if n > MaxActiveInstructionBytes {
		return fmt.Errorf("%w：有效指令上下文合计超过 64 KiB（含正文与条目开销），请先整理或撤销旧指令；未保存或截断", ErrInstructionInvalid)
	}
	return nil
}

// Scope and provenance are immutable. Changing scope requires a new instruction.
func (s *Store) UpdateInstruction(id string, version uint64, text, state string) (model.Instruction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	text = strings.TrimSpace(text)
	if state != model.InstructionActive && state != model.InstructionRevoked {
		return model.Instruction{}, fmt.Errorf("%w：状态只能是 active 或 revoked", ErrInstructionInvalid)
	}
	if text == "" || len([]rune(text)) > MaxInstructionRunes {
		return model.Instruction{}, fmt.Errorf("%w：正文应为 1–4000 字", ErrInstructionInvalid)
	}
	for i, old := range s.db.Instructions {
		if old.ID != id {
			continue
		}
		if old.Version != version {
			return model.Instruction{}, ErrInstructionConflict
		}
		if old.State == state && old.Text == text {
			return old, nil
		}
		if state == model.InstructionActive {
			if err := s.instructionCapacityLocked(id, text, old.IntentID); err != nil {
				return model.Instruction{}, err
			}
		}
		old.Text, old.State, old.UpdatedAt, old.Version = text, state, time.Now(), old.Version+1
		s.db.Instructions[i] = old
		s.queueInstructionNoticesLocked(old)
		if err := s.flush("instruction", "message", "run"); err != nil {
			return model.Instruction{}, err
		}
		return old, nil
	}
	return model.Instruction{}, ErrInstructionNotFound
}

func (s *Store) runOwnsInstructionsLocked(run *model.Run) bool {
	if run.Status != model.RunRunning {
		return false
	}
	if run.Purpose == "commander" {
		return true
	}
	in, ok := s.intentLocked(run.IntentID)
	return ok && in.RunID == run.ID && in.Status == model.StatusRunning
}

// The commander's input is frozen before the turn, just as PrepareRunInput
// freezes an executor's input. These maps record preparation, not a read receipt.
func (s *Store) PrepareCommanderInstructions(runID string, policies ...string) ([]model.Instruction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.db.Runs {
		r := &s.db.Runs[i]
		if r.ID != runID {
			continue
		}
		if r.Purpose != "commander" || !s.runOwnsInstructionsLocked(r) {
			return nil, fmt.Errorf("指挥官运行已结束")
		}
		rows := ApplicableInstructions(s.db, "", true)
		if len(policies) > 0 {
			r.PremisePolicy = policies[0]
		}
		r.InstructionVersions = instructionVersions(rows)
		if err := s.flushNoRev("run"); err != nil {
			return nil, err
		}
		return rows, nil
	}
	return nil, fmt.Errorf("运行不存在")
}

// PreparePurposeInput freezes constraints and source data for auxiliary runs.
// A review follows its fact's subtree; a project report sees all scoped rules
// with their scope labels, since its source spans multiple directions.
func (s *Store) PreparePurposeInput(runID string, policies ...string) (DB, []model.Instruction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.db.Runs {
		r := &s.db.Runs[i]
		if r.ID != runID {
			continue
		}
		if r.Status != model.RunRunning || (r.Purpose != "verifier" && r.Purpose != "report") {
			return DB{}, nil, fmt.Errorf("辅助运行已结束或用途不符")
		}
		rows := ApplicableInstructions(s.db, r.IntentID, r.Purpose == "report")
		if len(policies) > 0 {
			r.PremisePolicy = policies[0]
		}
		r.InstructionVersions = instructionVersions(rows)
		if err := s.flushNoRev("run"); err != nil {
			return DB{}, nil, err
		}
		return s.db.clone(), rows, nil
	}
	return DB{}, nil, fmt.Errorf("运行不存在")
}

// The instruction and its outbox commit together. A crash after commit can
// delay IOA, but cannot lose the only notification or create a phantom one.
func (s *Store) queueInstructionNoticesLocked(in model.Instruction) {
	targets := map[string]int{collab.CommanderID: -1}
	for i := range s.db.Runs {
		r := &s.db.Runs[i]
		if !s.runOwnsInstructionsLocked(r) {
			continue
		}
		if r.Purpose == "commander" {
			targets[collab.CommanderID] = i
		} else if r.Purpose == "" && InstructionAppliesTo(s.db, in, r.IntentID) {
			targets[collab.ParticipantID(r.IntentID)] = i
		}
	}
	// Deterministic recipient ordering also makes the durable seq reproducible.
	people := make([]string, 0, len(targets))
	for to := range targets {
		people = append(people, to)
	}
	slices.Sort(people)
	for _, to := range people {
		id, now := NewID("m"), time.Now()
		m := model.Message{ID: id, EventID: id, Seq: s.nextMessageSeqLocked(), FromParticipantID: in.CreatedBy, ToParticipantID: to, Kind: "instruction", Durable: true, InstructionID: in.ID, InstructionVersion: in.Version, Text: fmt.Sprintf("持久指令 %s 已更新至 v%d（%s）。这是变更通知；当前有效口径以本轮指令全集及 GET /api/instructions/%s 为准，旧通知不能覆盖新版本。", in.ID, in.Version, in.State, in.ID), IdempotencyKey: fmt.Sprintf("instruction:%s:%d:%s", in.ID, in.Version, to), Status: model.MessageQueued, CreatedAt: now, UpdatedAt: now}
		if index := targets[to]; index >= 0 {
			r := &s.db.Runs[index]
			if r.InstructionNotices == nil {
				r.InstructionNotices = map[string]uint64{}
			}
			r.InstructionNotices[in.ID] = in.Version
			m.ToGeneration = r.Generation
		}
		s.db.Messages = append(s.db.Messages, m)
	}
}

func (s *Store) InstructionNotifications(id string, version uint64) []model.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []model.Message{}
	for _, m := range s.db.Messages {
		if m.InstructionID == id && m.InstructionVersion == version {
			out = append(out, m)
		}
	}
	return out
}

func (s *Store) AcknowledgeInstruction(runID, instructionID string, version uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.db.Runs {
		r := &s.db.Runs[i]
		if r.ID != runID {
			continue
		}
		if !s.runOwnsInstructionsLocked(r) {
			return fmt.Errorf("%w：运行不再拥有该方向", ErrInstructionConflict)
		}
		if version == 0 || (r.InstructionVersions[instructionID] != version && r.InstructionNotices[instructionID] != version) {
			return ErrInstructionConflict
		}
		for _, in := range s.db.Instructions {
			if in.ID != instructionID {
				continue
			}
			if in.Version != version {
				return ErrInstructionConflict
			}
			if r.InstructionAcks == nil {
				r.InstructionAcks = map[string]uint64{}
			}
			if r.InstructionAcks[instructionID] == version {
				return nil
			}
			r.InstructionAcks[instructionID] = version
			return s.flushNoRev("run")
		}
		return ErrInstructionNotFound
	}
	return fmt.Errorf("运行不存在")
}
