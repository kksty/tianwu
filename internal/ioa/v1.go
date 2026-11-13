package ioa

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// ProtocolVersion is the harness application metadata version this observer
// understands. Records without "event" are another application's IOA data and
// are not executable harness work.
const ProtocolVersion = 1

// Event is one application event carried directly in IOA meta.
type Event string

const (
	EventMessage          Event = "message"
	EventInboxRoute       Event = "inbox.route"
	EventSubagentDelegate Event = "subagent.delegate"
	EventSubagentReturn   Event = "subagent.return"
	EventTaskResult       Event = "task.result"
)

// Address identifies a task and/or a live session inside an IOA node.
type Address struct {
	NodeID    string `json:"node_id,omitempty"`
	TaskID    string `json:"task_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// Agent describes a delegated agent.
type Agent struct {
	Name  string `json:"name,omitempty"`
	Type  string `json:"type,omitempty"`
	Mode  string `json:"mode,omitempty"`
	Model string `json:"model,omitempty"`
}

// Envelope is the v1 application record carried in IOA meta.
type Envelope struct {
	Version   int      `json:"version"`
	Event     Event    `json:"event"`
	ID        string   `json:"id,omitempty"`
	Source    Address  `json:"source,omitempty"`
	Target    Address  `json:"target,omitempty"`
	Interrupt bool     `json:"interrupt,omitempty"`
	Chain     []string `json:"chain,omitempty"`
	Agent     *Agent   `json:"agent,omitempty"`
	Status    string   `json:"status,omitempty"`
	CallID    string   `json:"call_id,omitempty"`
}

var protocolFields = map[string]bool{
	"version": true, "event": true, "id": true, "source": true, "target": true,
	"interrupt": true, "chain": true, "agent": true, "status": true, "call_id": true,
}

// IsProtocolField reports whether a meta key belongs to the harness
// application protocol. Other keys are another application's data.
func IsProtocolField(key string) bool { return protocolFields[key] }

// ParseEnvelope recognizes direct v1 metadata. A record without "event" is not
// harness work and returns (nil, nil); malformed protocol metadata returns an
// error so the observer can retain it as a rejected record instead of dropping
// it silently.
func ParseEnvelope(meta map[string]any) (*Envelope, error) {
	if meta == nil {
		return nil, nil
	}
	if _, ok := meta["event"]; !ok {
		return nil, nil
	}
	fields := make(map[string]any, len(protocolFields))
	for key, value := range meta {
		if !IsProtocolField(key) {
			continue
		}
		if value == nil {
			return nil, fmt.Errorf("protocol field %s cannot be null", key)
		}
		fields[key] = value
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("collaboration metadata: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var env Envelope
	if err := decoder.Decode(&env); err != nil {
		return nil, fmt.Errorf("collaboration metadata: %w", err)
	}
	if err := env.Validate(); err != nil {
		return nil, err
	}
	return &env, nil
}

// Validate mirrors the harness's v1 constraints: only known events, only
// message may interrupt, lifecycle records need a stable id, and delegation
// records need a call id and an agent.
func (e *Envelope) Validate() error {
	if e.Version != ProtocolVersion {
		return fmt.Errorf("unsupported collaboration version %d", e.Version)
	}
	switch e.Event {
	case EventMessage, EventInboxRoute, EventSubagentDelegate, EventSubagentReturn, EventTaskResult:
	default:
		return fmt.Errorf("unknown collaboration event %q", e.Event)
	}
	if e.Interrupt && e.Event != EventMessage {
		return fmt.Errorf("only message may interrupt (event %s)", e.Event)
	}
	if e.Event != EventMessage && e.ID == "" {
		return fmt.Errorf("%s requires a stable id", e.Event)
	}
	if e.Event == EventSubagentDelegate || e.Event == EventSubagentReturn {
		if e.CallID == "" {
			return fmt.Errorf("%s requires call_id", e.Event)
		}
		if e.Agent == nil {
			return fmt.Errorf("%s requires agent", e.Event)
		}
	}
	if e.Event != EventTaskResult && len(e.Chain) > 0 {
		return fmt.Errorf("only task.result may carry chain (event %s)", e.Event)
	}
	if e.Event == EventTaskResult && (e.Agent != nil || e.CallID != "") {
		return fmt.Errorf("task.result cannot carry delegation fields")
	}
	return nil
}
