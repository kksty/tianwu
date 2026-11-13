package model

import "time"

// Resource kinds name the execution-site things a direction can hold. The
// registry that projects them is a read model over the existing owners — browser
// leases in `.access/`, operator waits in the intent, external actions and runs
// in the ledger — so no new persisted table exists for these values.
const (
	ResourceKindBrowser  = "browser"
	ResourceKindMaterial = "material"
	ResourceKindWait     = "wait"
	ResourceKindAction   = "action"
	ResourceKindSession  = "session"
)

// Resource states are kind-agnostic. Callers should read them as:
//
//	free        nothing holds it and it is usable now
//	held        a live run/operator holds it
//	busy        the owner is executing a native operation right now
//	recovery    the holder is gone or an operation is unconfirmed; an operator
//	            must confirm the site is idle before it is reused
//	pending     a persisted wait has not been answered yet
//	completed   the wait/action recorded a result
//	unavailable the operator reported the prerequisite could not be provided
//	unknown     the owner recorded that the outcome is unknown; never replay it
//	retired     the persisted owner record is gone; only history remains
//
// "free" is not "safe": a browser with no lease is still an external site whose
// state nobody has verified. The registry only reports ownership facts.
const (
	ResourceStateFree        = "free"
	ResourceStateHeld        = "held"
	ResourceStateBusy        = "busy"
	ResourceStateRecovery    = "recovery"
	ResourceStatePending     = "pending"
	ResourceStateCompleted   = "completed"
	ResourceStateUnavailable = "unavailable"
	ResourceStateUnknown     = "unknown"
	ResourceStateRetired     = "retired"
)

// Resource is a bounded read-only projection of one execution-site resource.
//
// ID is built from the persisted identity of the owning record, not from a
// process, window title, port or output: "browser:<ac_id>", "material:<ac_id>",
// "wait:<op_id>", "action:<ax_id>", "session:<run_id>". Version is the fence
// token the owning mechanism already uses (browser lease id, action CAS version,
// operator receipt, run id); it is a string only because those mechanisms use
// different token shapes. Detail holds short non-secret labels — never material
// contents, tokens or free text an executor might paste.
type Resource struct {
	ID          string            `json:"id"`
	Kind        string            `json:"kind"`
	Ref         string            `json:"ref,omitempty"`
	IntentID    string            `json:"intent_id,omitempty"`
	HolderRunID string            `json:"holder_run_id,omitempty"`
	Generation  int               `json:"generation,omitempty"`
	State       string            `json:"state"`
	Version     string            `json:"version,omitempty"`
	UpdatedAt   time.Time         `json:"updated_at"`
	Detail      map[string]string `json:"detail,omitempty"`
	// Query is the read path that returns this same projection. It is a pointer,
	// not an identity: an owner record's persisted id remains the identity.
	Query string `json:"query,omitempty"`
}

// IsResourceKind reports whether kind is one of the registry's projection kinds.
func IsResourceKind(kind string) bool {
	switch kind {
	case ResourceKindBrowser, ResourceKindMaterial, ResourceKindWait,
		ResourceKindAction, ResourceKindSession:
		return true
	}
	return false
}
