package store

import (
	"encoding/json"
	"fmt"
	"github.com/kksty/tianwu/internal/model"
)

func appendRow(db *DB, kind string, raw []byte) error {
	switch kind {
	case "route":
		var v model.RouteCheckpoint
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		db.Routes = append(db.Routes, v)
	case "instruction":
		var v model.Instruction
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		db.Instructions = append(db.Instructions, v)
	case "fact":
		var v model.Fact
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		db.Facts = append(db.Facts, v)
	case "intent":
		var v model.Intent
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		db.Intents = append(db.Intents, v)
	case "evidence":
		var v model.Evidence
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		db.Evidence = append(db.Evidence, v)
	case "goal":
		var v model.Goal
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		db.Goals = append(db.Goals, v)
	case "review":
		var v model.Review
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		db.Reviews = append(db.Reviews, v)
	case "run":
		var v model.Run
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		db.Runs = append(db.Runs, v)
	case "playbook":
		var v model.Playbook
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		db.Playbooks = append(db.Playbooks, v)
	case "message":
		var v model.Message
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		db.Messages = append(db.Messages, v)
	case "action":
		var v model.ExternalAction
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		db.Actions = append(db.Actions, v)
	default:
		return fmt.Errorf("unknown ledger row %s", kind)
	}
	return nil
}
