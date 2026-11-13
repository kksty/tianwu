package retrieval

import (
	"fmt"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

type InstructionPage struct {
	Instructions []model.Instruction `json:"instructions"`
	PageInfo
}

func Instructions(s *store.Store, intent, state, token string, limit int, commander bool) (InstructionPage, error) {
	if state != "" && state != "all" && state != model.InstructionActive && state != model.InstructionRevoked {
		return InstructionPage{}, fmt.Errorf("invalid instruction state")
	}
	key := queryKey([]any{"instructions", intent, state, commander})
	db, c, err := read(s, token, key)
	if err != nil {
		return InstructionPage{}, err
	}
	rows := []model.Instruction{}
	for _, in := range db.Instructions {
		if state != "all" && in.State != state && !(state == "" && in.State == model.InstructionActive) {
			continue
		}
		if commander || store.InstructionAppliesTo(db, in, intent) {
			rows = append(rows, in)
		}
	}
	out := InstructionPage{Instructions: []model.Instruction{}, PageInfo: PageInfo{Total: len(rows), Version: c.Version}}
	if c.Offset > len(rows) {
		return out, fmt.Errorf("invalid_cursor")
	}
	end := min(c.Offset+pageLimit(limit), len(rows))
	out.Instructions = rows[c.Offset:end]
	out.HasMore = end < len(rows)
	if out.HasMore {
		c.Offset = end
		out.NextCursor = encode(c)
	}
	return out, nil
}
