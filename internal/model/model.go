// Package model defines the ledger objects of Tianwu.
//
// Every object here is intentionally small and plain: no hashes, no CVSS, no
// version numbers, no leases. See docs/06-decisions.md for why.
package model

import "time"

// Intent status values. An intent is the unit of offensive work dispatched to
// an executor; it is claimed atomically, never leased.
const (
	StatusPending = "pending"
	StatusClaimed = "claimed"
	StatusRunning = "running"
	// StatusWaiting is a business state: the direction's current turn yielded
	// because it is waiting for child intents (or for a wake). It has no live
	// run, so the watchdog must not "recover" it and dispatch must not claim it;
	// only its wait condition (or its deadline) moves it back to pending.
	StatusWaiting = "waiting"
	StatusDone    = "done"
	StatusFailed  = "failed"
	StatusParked  = "parked"
)

// Run status values. A cancellation is our decision, not the executor's
// failure, so it must not feed the escalation ladder (docs/06, D19).
const (
	RunRunning  = "running"
	RunDone     = "done"
	RunFailed   = "failed"
	RunCanceled = "canceled"
)

// Run cancel causes. Canceled is one status but several very different events:
// a steer is a continuation, a restart is deployment, a stop-loss is policy, and
// a watchdog kill is supervision. Keeping the cause structured lets the console
// say "转续话" without re-parsing the free-text exit_reason.
const (
	CancelCauseSteer    = "steer"
	CancelCauseRetry    = "retry"
	CancelCauseStopLoss = "stop_loss"
	// CancelCauseHandoff is a planned replacement: the direction continues in a
	// new session with a handoff, so it must not read as a failure or steer.
	CancelCauseHandoff  = "handoff"
	CancelCauseRestart  = "restart"
	CancelCauseWatchdog = "watchdog"
	CancelCauseOperator = "operator"
	// CancelCauseWorkerKill is a participant stopping a worker it created (or a
	// descendant of its own direction): the direction is over, but the ledger
	// says it was a kill, not a failure or an operator cancel.
	CancelCauseWorkerKill = "worker_kill"
	// CancelCausePause is the whole-project switch. It is not the operator
	// canceling one direction: it stops every live intent run and the report
	// runs that dispatch never started. Verifier runs are deliberately not part
	// of it: reviews have their own switch (CancelCauseReviewPause) so an
	// operator can re-prove a finding while the engagement is paused (D137).
	CancelCausePause = "pause"
	// CancelCauseReviewPause is the review kill switch, independent of the
	// project pause: it stops live verifier runs and refuses new ones without
	// resuming the commander or the executor (D137).
	CancelCauseReviewPause = "review_pause"
	// OperatorWait yields a turn to a persisted human condition, preserving
	// the session and wait without running the model/evaluator while waiting.
	CancelCauseOperatorWait = "operator_wait"
	CancelCauseChildWait    = "child_wait"
	// CancelCauseModelStall is the liveness stop: a weak model kept answering
	// with empty assistant messages (each followed by another llm_request) and
	// never called a tool or ended the turn. It is not a task verdict — the
	// direction is held for an operator/decision retry, not marked done/failed
	// by the model's opinion (docs/17 §3.6 liveness, final-e2e I1).
	CancelCauseModelStall = "model_stall"
	// CancelCauseRoundTimeout is the commander round's wall-clock limit: the
	// decision did not finish in time, so the round was cut. It is neither a
	// project stop nor the model's verdict — the round's actions are recorded,
	// the harness session survives, and the next wake decides again (D91).
	CancelCauseRoundTimeout = "round_timeout"
)

// Fact origins. Declared facts come from <workdir>/.tw/facts.jsonl, derived
// facts are computed by the ledger from the filesystem, heuristic facts are the
// fallback extraction. The board ranks them in that order (docs/06, D18).
const (
	OriginDeclared  = "declared"
	OriginDerived   = "derived"
	OriginHeuristic = "heuristic"
)

// Interjection is one message a human or the commander delivered into a live
// run. MessageID is the IOA message id; delivery is best-effort from the
// control plane's point of view (the harness has no end-to-end ack for this
// path), so the UI calls it "accepted" rather than "read" (D69).
type Interjection struct {
	At        time.Time `json:"at"`
	Text      string    `json:"text"`
	RefFact   string    `json:"ref_fact,omitempty"`
	Interrupt bool      `json:"interrupt,omitempty"`
	MessageID string    `json:"message_id,omitempty"`
}

// Intent notes are commentary about a direction, not constraints on it. They
// are recorded on the intent for the operator and the next decision; the
// executor brief never reads this field, because a note is written by the
// decider (which saw the run's internals) about the executor, not to it.
const (
	IntentNoteRetry = "retry"
	IntentNoteSteer = "steer"
)

// IntentNote is one retry/steer remark kept next to the direction it explains.
// It used to be appended to Doc under a heading, which made the note part of
// the executor's constraint section and forced BuildPrompt to strip a heading by
// name — a content-shape patch that any "## " in the note could walk around.
type IntentNote struct {
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"` // retry|steer
	Text    string    `json:"text"`
	RefFact string    `json:"ref_fact,omitempty"`
}

// Intent is one direction of offensive work.
type Intent struct {
	// ContextFactIDs are explicit references the direction reads: recorded
	// observations that inform the work without being necessary premises.
	// FromFactIDs stays the hard prerequisite list; a reference never blocks a
	// dispatch, and the two are different edges in the graph.
	ContextFactIDs []string `json:"context_fact_ids,omitempty"`
	// PinnedFactIDs are references the operator wants kept in the hot layer
	// regardless of age or relevance ranking. They change focus, not validity.
	PinnedFactIDs []string `json:"pinned_fact_ids,omitempty"`
	// InterestEntityRefs names recorded entity identities (entity_ref values,
	// never prose) whose other observations should surface for this direction.
	InterestEntityRefs []string `json:"interest_entity_refs,omitempty"`
	// ContextVersion is the optimistic concurrency token of the selection above.
	ContextVersion uint64   `json:"context_version"`
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	Description    string   `json:"description"`
	Doc            string   `json:"doc,omitempty"`
	FromFactIDs    []string `json:"from_fact_ids,omitempty"`
	// FromFactClaims records the claim version of each premise when the
	// direction was created. A later revision of a premise is surfaced in the
	// board frontier as "premise_changed": it is a judgement input for the
	// commander, not an automatic block (the dispatch gate still checks the
	// records that exist now).
	FromFactClaims map[string]uint64 `json:"from_fact_claims,omitempty"`
	// Skills are extra harness skills for this intent: a name under
	// <data>/skills/ or an absolute SKILL.md path (docs/06, D23).
	Skills []string `json:"skills,omitempty"`
	// Eval is the optional acceptance criterion for the harness's own goal
	// evaluator (-e). Empty means "stop when the agent stops calling tools";
	// a criterion lets a long run keep going until it is actually met.
	Eval string `json:"eval,omitempty"`
	// MaxTurns is an optional cap on the harness agent loop. 0 means the
	// harness default; audit has no equivalent flag.
	MaxTurns int `json:"max_turns,omitempty"`
	// Node is the execution location this direction belongs on; empty means the
	// registry's default node (docs/14-nodes.md). The run records where it
	// actually ran, which may differ if the node disappeared.
	Node string `json:"node,omitempty"`
	// Profile is the saved model configuration this direction should run on;
	// empty means the instance default. Only the local node can honour it (a
	// remote host has its own configuration); the run freezes the resolved
	// profile and revision so a later edit cannot rewrite history.
	Profile string `json:"profile,omitempty"`
	// ParentID is the direction that delegated this one. It is empty for a
	// direction created by the commander/human. Parent and child are always in
	// the same project; the child has its own case, claim and run.
	ParentID string `json:"parent_intent_id,omitempty"`
	// CreatedBy is the participant id that created this direction (the
	// commander for an operator-created intent); DelegatedByRun is the run that
	// created it, for audit. Neither is a permission.
	CreatedBy      string `json:"created_by_participant_id,omitempty"`
	DelegatedByRun string `json:"delegated_by_run_id,omitempty"`
	// IdempotencyKey dedupes repeated child creation by the same parent run.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	// Wait is set while the direction is waiting for children. It is business
	// state, not a sleep: the run ends, the slot is released, and a result event
	// (or the deadline) moves the direction back to pending.
	Wait *Wait `json:"wait,omitempty"`
	// HandoffVersion is the optimistic-concurrency version of Handoff. A
	// replacement request names the version it saw; an old run cannot overwrite a
	// newer handoff.
	HandoffVersion   int      `json:"handoff_version,omitempty"`
	HandoffOperation string   `json:"handoff_operation,omitempty"`
	Handoff          *Handoff `json:"handoff,omitempty"`
	// Aborted marks a descendant whose subtree was cancelled. A cancelled run
	// normally returns the intent to pending (D19), but an aborted child must
	// settle as failed so its parent's wait does not hang on a direction nobody
	// will ever dispatch.
	Aborted bool   `json:"aborted,omitempty"`
	Status  string `json:"status"`
	// Hold is the operator/commander's "do not auto-dispatch" flag. A canceled
	// intent goes back to pending (D19), and without this the next round's
	// dispatchPending would immediately start it again — cancel would not
	// cancel. Held intents stay pending until an explicit retry/run clears it.
	Hold       bool `json:"hold,omitempty"`
	Attempts   int  `json:"attempts"`
	SteerCount int  `json:"steer_count,omitempty"`
	// SayCount records mid-flight messages independently of continuations.
	// Neither counter is an admission or fee limit.
	SayCount int `json:"say_count,omitempty"`
	// Interjections is the audit trail of messages delivered into this
	// direction while it was running. It is deliberately not a fact: it is an
	// instruction, not an observation.
	Interjections []Interjection `json:"interjections,omitempty"`
	// Notes is the retry/steer commentary about this direction. It is shown in
	// the console and read by the decider; BuildPrompt never reads it, so a
	// note cannot become an executor constraint (D72).
	Notes      []IntentNote `json:"notes,omitempty"`
	ClaimedBy  string       `json:"claimed_by,omitempty"`
	ParkReason string       `json:"park_reason,omitempty"`
	RunID      string       `json:"run_id,omitempty"`
	CreatedAt  time.Time    `json:"created_at"`
	UpdatedAt  time.Time    `json:"updated_at"`
}

// Wait is the persisted condition a direction yielded on. Evaluation, the
// transition to pending, and the wake flag all happen under the store lock, so a
// child that finishes just before registration cannot be lost: RegisterWait
// evaluates once while holding the same lock and returns ready immediately.
type Wait struct {
	// Actions is mutually exclusive with Children and Operator. Only these
	// explicitly selected external operations can release the waiting turn.
	Actions []string `json:"actions,omitempty"`
	// Operator is an external condition, completed only by an operator receipt.
	// It is mutually exclusive with Children and has no automatic deadline.
	Operator *OperatorWait `json:"operator,omitempty"`
	// Children are the child intent ids this wait covers. They must all name
	// this intent as ParentID.
	Children []string `json:"children"`
	// Mode is all (every child settled) or any (the first settled child).
	Mode string `json:"mode,omitempty"`
	// Deadline is optional. A ticker wakes the parent with a timeout result
	// instead of leaving it waiting forever.
	Deadline     *time.Time `json:"deadline,omitempty"`
	RegisteredAt time.Time  `json:"registered_at"`
	// RegisteredByRun is the run whose turn yielded. A later run's completion
	// must not consume this wait.
	RegisteredByRun string `json:"registered_by_run,omitempty"`
	// Implicit is the "you still have active children" wait the control plane
	// adds when a parent finishes without registering one, so a parent cannot
	// orphan children by ending its turn.
	Implicit bool `json:"implicit,omitempty"`
	// WakePending means the condition is satisfied (or the deadline fired) but
	// the parent has not been re-dispatched yet. The scheduler consumes it.
	WakePending bool   `json:"wake_pending,omitempty"`
	WakeReason  string `json:"wake_reason,omitempty"`
	// Results is the structured result event the parent wakes with. It is
	// persisted so a restart can rebuild the wake prompt.
	Results []WaitResult `json:"results,omitempty"`
}

// OperatorWait keeps a human handoff distinct from evidence and child results.
// A receipt distinguishes a completed action from an unavailable prerequisite;
// neither verifies login or any security claim.
type OperatorWait struct {
	ID          string     `json:"id"`
	Request     string     `json:"request"`
	AccessIDs   []string   `json:"access_ids,omitempty"`
	Response    string     `json:"response,omitempty"`
	Resolution  string     `json:"resolution,omitempty"` // completed|unavailable; empty on old completed receipts
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

const (
	OperatorCompleted   = "completed"
	OperatorUnavailable = "unavailable"
)

// Wait modes and wake reasons.
const (
	WaitAll = "all"
	WaitAny = "any"

	WaitReasonChildren = "children_settled"
	WaitReasonTimeout  = "timeout"
)

// WaitResult is one settled child's outcome. Status is the child intent's
// terminal status; StopReason is its last run's harness stop reason.
type WaitResult struct {
	ActionID    string    `json:"action_id,omitempty"`
	Result      string    `json:"result,omitempty"`
	ResultRef   string    `json:"result_ref,omitempty"`
	ChildID     string    `json:"child_id"`
	Status      string    `json:"status"`
	RunID       string    `json:"run_id,omitempty"`
	StopReason  string    `json:"stop_reason,omitempty"`
	FactIDs     []string  `json:"fact_ids,omitempty"`
	EvidenceIDs []string  `json:"evidence_ids,omitempty"`
	At          time.Time `json:"at"`
}

// Settled reports whether a status is terminal for waiting purposes. Waiting is
// deliberately not settled: a child that is itself waiting for grandchildren is
// still open work.
func Settled(status string) bool {
	switch status {
	case StatusDone, StatusFailed, StatusParked:
		return true
	}
	return false
}

// Fact is a recorded observation or conclusion produced by one intent.
//
// Confidence is observed (an executor saw it) or inferred (derived by the
// decider or by heuristic ingestion). Status is unverified until a verifier
// re-runs the proof; only report-bound facts are verified.
type Fact struct {
	// DeclarationID is the writer's stable identity for this observation: the
	// same id re-declared means "the same observation, updated"; a different id
	// is a different observation even with the same title. It defaults to the
	// facts.jsonl byte range, which makes re-reading a settled file idempotent.
	DeclarationID string `json:"declaration_id,omitempty"`
	// ClaimVersion starts at 1 and increments whenever the assertion changes
	// (summary/doc/evidence/tags/proof/context or observation time). Reviews
	// bind to the version they proved; a changed claim returns to unverified.
	// The display title and the review status are not part of the claim.
	ClaimVersion uint64 `json:"claim_version"`
	// ClaimHash fingerprints the proven assertion, so a review can detect that
	// it judged different content even when the version was not supplied.
	ClaimHash string `json:"claim_hash"`
	// ObservedAt is when the writer says it observed the thing; empty means
	// "when it was recorded". It is part of the claim: a redated observation is
	// a new assertion version.
	ObservedAt *time.Time `json:"observed_at,omitempty"`
	// ExecutionContextRef is the explicit site the observation came from (a
	// browser resource, a run context). It is recorded identity, never inferred
	// from prose, and it is part of the claim fingerprint.
	ExecutionContextRef string `json:"execution_context_ref,omitempty"`
	EntityRef           string `json:"entity_ref,omitempty"`
	SourceRunID         string `json:"source_run_id,omitempty"`
	DeclarationRef      string `json:"declaration_ref,omitempty"`
	ID                  string `json:"id"`
	IntentID            string `json:"intent_id"`
	Kind                string `json:"kind"` // host|service|cred|vuln|loot|artifact|note
	Title               string `json:"title"`
	Summary             string `json:"summary"`
	Doc                 string `json:"doc,omitempty"`
	Confidence          string `json:"confidence"`
	Origin              string `json:"origin,omitempty"` // declared|derived|heuristic
	// SourceRef is the stable identity of the thing this fact was translated
	// from, when the writer knows it: an audit finding is
	// "report:<reportID>#<findingID>". Two findings that cite the same file are
	// different findings; before this, the importer deduped by evidence overlap
	// and silently dropped the second one (D74).
	SourceRef string `json:"source_ref,omitempty"`
	// Proof says how the conclusion is reproduced: "dynamic" (default) re-runs
	// the attack, "static" replays the command over the same artifact. A static
	// finding has no live surface to attack, so re-running an exploit would
	// reject it for the wrong reason (D35).
	Proof       string   `json:"proof,omitempty"` // dynamic|static
	EvidenceIDs []string `json:"evidence_ids,omitempty"`
	// Tags are optional labels for reuse: playbooks use them for recall
	// (`product:thinkphp`, `technique:rce`, `port:8080`). They are not an
	// enum and never gate a write; the brief uses them to decide which
	// playbooks to put in front of the next run.
	Tags []string `json:"tags,omitempty"`
	// Refs are the citations this fact made that the ledger has no bytes for:
	// a URL (only a human can open it), a tool artifact (waiting for the
	// artifact store), or a file the run named that is gone. They are recorded
	// instead of dropping the fact: a finding whose only support was a live URL
	// still deserves a line in "scope and limitations" (D44).
	Refs      []Ref     `json:"refs,omitempty"`
	Status    string    `json:"status"` // unverified|verified|rejected
	CreatedAt time.Time `json:"created_at"`
}

// Ref is where a claim says its support lives, when that place is not a file
// the ledger can open. Scheme is one of the Ref* constants below.
type Ref struct {
	Scheme  string `json:"scheme"`
	Locator string `json:"locator"`
}

// Reference schemes. Only "file" can produce bytes today; "artifact" will when
// there is an artifact store, and "url" never will — fetching it at report time
// would attack the target again, outside any authorised run.
const (
	RefFile     = "file"
	RefURL      = "url"
	RefArtifact = "artifact"
)

// Playbook is project knowledge, not a finding: a reusable tactic that outlived
// the run which discovered it. It is a separate table because a fact is an
// observation with evidence, while a playbook is a recipe with provenance — it
// is never verified, never reported, never counted as coverage (D70).
//
// Two writers, two tiers:
//   - the executor that used the tactic writes the confirmed row in the same
//     run (Origin=executor, Status=confirmed);
//   - the commander may only nominate a candidate (Origin=commander,
//     Status=candidate), and it must cite the facts it inferred it from.
//
// Doc is prompt text handed to a later executor. It is never executed, and it
// is treated as untrusted: the brief says "核对再用".
type Playbook struct {
	Archived         bool               `json:"archived,omitempty"`
	SourceRunID      string             `json:"source_run_id,omitempty"`
	FromFactVersions map[string]uint64  `json:"from_fact_versions,omitempty"`
	Pattern          *ExperiencePattern `json:"pattern,omitempty"`
	RequiredTags     []string           `json:"required_tags,omitempty"`
	ID               string             `json:"id"`
	Title            string             `json:"title"`
	// When is one line: what this buys you and when it applies.
	When string `json:"when"`
	// Doc is steps/commands/limits, written by whoever ran it.
	Doc  string   `json:"doc,omitempty"`
	Tags []string `json:"tags,omitempty"`
	// Outcome is success (try it) or dead_end (do not retry without a new
	// premise). A dead end is knowledge too.
	Outcome string `json:"outcome,omitempty"`
	Origin  string `json:"origin"` // executor|commander
	Status  string `json:"status"` // confirmed|candidate|retired
	// FromFactIDs is provenance. A commander nomination without at least one
	// existing fact is refused: no observation, no knowledge.
	FromFactIDs []string  `json:"from_fact_ids,omitempty"`
	IntentID    string    `json:"intent_id,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ExperiencePattern describes recorded relationships to look up. Variable
// names and entity bindings are authored explicitly, never inferred from titles.
type ExperiencePattern struct {
	Nodes []PatternNode `json:"nodes"`
	Edges []PatternEdge `json:"edges"`
}
type PatternNode struct {
	Name      string   `json:"name"`
	Type      string   `json:"type"`
	Kind      string   `json:"kind,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	EntityVar string   `json:"entity_var,omitempty"`
}
type PatternEdge struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Kind   string `json:"kind"`
}

// Playbook origins and tiers. They are not fact origins: a playbook is never
// "declared evidence", so mixing the two vocabularies would blur the line this
// table exists to draw.
const (
	PlaybookFromExecutor  = "executor"
	PlaybookFromCommander = "commander"

	PlaybookConfirmed = "confirmed"
	PlaybookCandidate = "candidate"
	PlaybookRetired   = "retired"

	PlaybookSuccess = "success"
	PlaybookDeadEnd = "dead_end"
)

// Message is one participant-to-participant message. It is a record of a
// conversation, not a fact: nothing in the fact, board, review or report paths
// reads it, and a message never becomes evidence.
//
// Participants are derived from intents (one logical participant per intent,
// stable across run generations); the ids are assigned by the control plane, not
// supplied by the caller. Seq is the per-project read cursor; IdempotencyKey is
// the caller's retry key and is unique per sender.
type Message struct {
	NoticeIntentID string `json:"notice_intent_id,omitempty"`
	NoticeRunID    string `json:"notice_run_id,omitempty"`
	NoticeFactID   string `json:"notice_fact_id,omitempty"`
	NoticeVersion  uint64 `json:"notice_version,omitempty"`
	Coalesced      int    `json:"coalesced,omitempty"`
	// Durable messages survive the ordinary chat queue retention policy.
	Durable            bool   `json:"durable,omitempty"`
	InstructionID      string `json:"instruction_id,omitempty"`
	InstructionVersion uint64 `json:"instruction_version,omitempty"`
	ID                 string `json:"id"`
	Seq                uint64 `json:"seq"`

	ProjectID string `json:"project_id"`

	FromParticipantID string `json:"from_participant_id"`
	FromRunID         string `json:"from_run_id,omitempty"`
	FromGeneration    int    `json:"from_generation,omitempty"`

	ToParticipantID string `json:"to_participant_id"`
	// ToGeneration is the recipient generation the sender addressed. It is
	// rejected when the target has moved on, so an old run cannot deliver into
	// a new holder of the same intent.
	ToGeneration int `json:"to_generation,omitempty"`

	// Kind describes semantics, not a role. A task result defaults to durable
	// retention; other kinds can explicitly request Durable without text parsing.
	Kind string `json:"kind,omitempty"`
	Text string `json:"text"`

	FactRefs  []string `json:"fact_refs,omitempty"`
	InReplyTo string   `json:"in_reply_to,omitempty"`

	// Interrupt is the old say's interrupt flag carried as a message property:
	// it asks the harness to stop the current model request instead of draining
	// the text before the next one (D69). It is only meaningful for a message
	// delivered into a live turn.
	Interrupt bool `json:"interrupt,omitempty"`

	IdempotencyKey string `json:"idempotency_key,omitempty"`

	// EventID is the application-level identity of this message. It is assigned
	// once when the message enters the queue and is reused by every retry and
	// every process restart, so the harness can dedupe a redelivery by id
	// instead of guessing from retry counts. Empty on rows written before the
	// field existed; the delivery path fills it from ID on first claim.
	EventID string `json:"event_id,omitempty"`
	// Chain is the delivery chain for result semantics (task.result v1). It is
	// empty for ordinary messages, which the protocol forbids from carrying it.
	Chain []string `json:"chain,omitempty"`

	// Status is the delivery lifecycle: queued (stored, not delivered),
	// accepted (IOA accepted, or the next run carried it in its prompt; the
	// model may not have read it yet), confirmed (the recipient acknowledged
	// it), or unknown (the send timed out; it may or may not have arrived —
	// never retry automatically). A withdrawn or swept message is gone, not a
	// status: there is no retracted/expired row.
	Status string `json:"status"`
	// DeliveryID is the IOA message id, when there was one, or the synthetic
	// "prompt:<run>" marker when the run's prompt carried the text.
	DeliveryID string `json:"delivery_id,omitempty"`
	// DeliveredRunID is the run this message was handed to: the prompt that
	// carried it (DeliveryPath=prompt) or the live run it was said into
	// (DeliveryPath=ioa). Empty means never delivered. It is how a failed
	// run's possibly-unread messages are found and requeued.
	DeliveredRunID string `json:"delivered_run_id,omitempty"`
	// DeliveryPath says how the bytes reached the recipient: "prompt" (the
	// run's prompt/inbox section) or "ioa" (said into the live session). The
	// redelivery rules use it: an IOA message may still be only in the live
	// session's in-memory inbox, while a prompt message is already part of the
	// session transcript and comes back with a resume.
	DeliveryPath string `json:"delivery_path,omitempty"`
	// Redeliveries counts how many times the control plane put this message
	// back on the queue after a failed run. >0 makes the injected text carry a
	// [重投] marker so the model can dedupe by message_id instead of acting
	// twice on the same instruction.
	Redeliveries int `json:"redeliveries,omitempty"`
	// Error is the transport failure, kept verbatim for the operator.
	Error string `json:"error,omitempty"`

	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	AckedAt   *time.Time `json:"acked_at,omitempty"`
}

// Message delivery states. Sending is persisted before transport I/O; a crash
// leaves an unknown outcome. Accepted is transport acceptance, confirmed is a
// recipient receipt. Only queued messages can still be retracted or swept.
const (
	MessageQueued    = "queued"
	MessageSending   = "sending"
	MessageAccepted  = "accepted"
	MessageConfirmed = "confirmed"
	MessageUnknown   = "unknown"
)

// Message kinds used by the JSON contract and the console. They are labels,
// not an enum: a participant may send any short kind.
const (
	MessageQuestion = "question"
	MessageUpdate   = "update"
	MessageResult   = "result"
	MessageNote     = "note"
)

// Evidence is an original artifact. New rows point URI at a byte-for-byte
// preserved copy under the case root and record its Digest, so a later rewrite
// of the working path cannot silently change what the ledger proves. Rows
// written before preservation have no Digest: for those URI is whatever the
// working path held when they were admitted, and that limitation is explicit
// rather than repaired by a startup scan (docs/06 D150).
type Evidence struct {
	ID       string `json:"id"`
	RunID    string `json:"run_id"`
	IntentID string `json:"intent_id"`
	Name     string `json:"name"`
	Mime     string `json:"mime,omitempty"`
	Size     int64  `json:"size"`
	URI      string `json:"uri"`
	// Digest is the SHA-256 (hex) of the preserved bytes URI opens. Empty means
	// the row predates preservation or the source had no stable bytes.
	Digest string `json:"digest,omitempty"`
	// SourceURI is where the bytes were found when admitted (case-relative
	// working path, or the original imported/captured file). It is provenance,
	// not an address: readers open URI, never SourceURI.
	SourceURI string `json:"source_uri,omitempty"`
	// From records where the bytes came from when the executor declared a source:
	// "url:<u>" or "artifact:<id>". Empty for a plain file. It is provenance, not
	// an address: the ledger still opens URI (D44).
	From      string    `json:"from,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Goal is a statement the engagement must prove. QuantifiedTarget is a
// free-text reminder ("all endpoints in scope tested"), never auto-scored.
//
// met=true requires an evidence_id that resolves to a real evidence row: a
// claim nobody can click through is not a finding (docs/03, iron rule 4).
//
// A stamp is also a decision someone has to be able to read later, so it
// carries its receipt: Note is the one-line reason, From is the cited facts and
// CompletedAt is when it was closed. Without them the console can only say
// "已达成" — which is exactly the question ("why?") the operator asks next.
type Goal struct {
	CreatedByRun      string            `json:"created_by_run_id,omitempty"`
	CompletionPolicy  string            `json:"completion_policy,omitempty"`
	ID                string            `json:"id"`
	Statement         string            `json:"statement"`
	Met               bool              `json:"met"`
	EvidenceID        string            `json:"evidence_id,omitempty"`
	Note              string            `json:"note,omitempty"`
	From              []string          `json:"from,omitempty"`
	FromClaimVersions map[string]uint64 `json:"from_claim_versions,omitempty"`
	CompletedAt       *time.Time        `json:"completed_at,omitempty"`
	QuantifiedTarget  string            `json:"quantified_target,omitempty"`
	CreatedAt         time.Time         `json:"created_at"`
	// ReceiptStale/ReceiptChanged are computed, never persisted: they say that a
	// fact the completion cited has changed since the stamp, so the operator (or
	// commander) must re-judge the goal. The stamp itself is never rewritten
	// automatically — history keeps what was actually claimed then.
	ReceiptStale   bool     `json:"receipt_stale,omitempty"`
	ReceiptChanged []string `json:"receipt_changed,omitempty"`
	// IdempotencyKey makes an explicit "append a goal and continue" retry-safe:
	// a repeated request with the same key returns the goal it already created
	// instead of appending a second one. Empty on goals created by the commander
	// or by the plain create endpoint.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// Review is one verifier verdict on one fact.
type Review struct {
	// ClaimVersion/ClaimHash are the assertion the verdict actually proved. An
	// asynchronous verifier carries the version it started with; if the claim
	// changed meanwhile the store refuses the stale verdict. A zero version on
	// input means a synchronous human verdict recorded under the store lock.
	ClaimVersion uint64    `json:"claim_version"`
	ClaimHash    string    `json:"claim_hash"`
	ID           string    `json:"id"`
	FactID       string    `json:"fact_id"`
	Verdict      string    `json:"verdict"` // pass|needs_evidence|reject
	Notes        string    `json:"notes,omitempty"`
	VerifierRun  string    `json:"verifier_run,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// Usage is the token accounting of one turn. Known=false means the harness did
// not report usage at all (do not print 0 as if it were measured); Missing counts
// model calls inside the turn that returned no usage.
type Usage struct {
	InputTokens       int  `json:"input_tokens,omitempty"`
	OutputTokens      int  `json:"output_tokens,omitempty"`
	CachedInputTokens int  `json:"cached_input_tokens,omitempty"`
	CacheWriteTokens  int  `json:"cache_write_tokens,omitempty"`
	TotalTokens       int  `json:"total_tokens,omitempty"`
	Requests          int  `json:"requests,omitempty"`
	Missing           int  `json:"missing,omitempty"`
	Known             bool `json:"known"`
}

// Pricing preserves legacy ledger data. New runs do not calculate or store prices.
type Pricing struct {
	Currency             string  `json:"currency" yaml:"currency"`
	InputPerMToken       float64 `json:"input_per_mtoken" yaml:"input_per_mtoken"`
	OutputPerMToken      float64 `json:"output_per_mtoken" yaml:"output_per_mtoken"`
	CachedInputPerMToken float64 `json:"cached_input_per_mtoken" yaml:"cached_input_per_mtoken"`
}

// Cost preserves historical estimates when old ledgers are read and saved.
type Cost struct {
	Currency   string   `json:"currency"`
	Input      float64  `json:"input"`
	Output     float64  `json:"output"`
	CachedRead float64  `json:"cached_read,omitempty"`
	Total      float64  `json:"total"`
	Estimated  bool     `json:"estimated"`
	Pricing    *Pricing `json:"pricing,omitempty"`
}

// Handoff is a task handover snapshot: what a replacement model needs to
// continue without copying the old conversation. It is history/state, not an
// operator constraint and not a fact; the new session reads it as a brief.
type Handoff struct {
	ID      string `json:"id"`
	Version int    `json:"version"`

	FromRun        string `json:"from_run,omitempty"`
	FromProfile    string `json:"from_profile,omitempty"`
	FromGeneration int    `json:"from_generation,omitempty"`
	ToProfile      string `json:"to_profile,omitempty"`
	ToGeneration   int    `json:"to_generation,omitempty"`

	// Note is the caller's short account of what the next model should know.
	// It is history, not an executor constraint.
	Note string `json:"note,omitempty"`
	// Facts are confirmed/evidenced fact ids; Evidence are their case-relative
	// files. Attempts are prior run stop reasons, newest first.
	Facts    []string `json:"facts,omitempty"`
	Evidence []string `json:"evidence,omitempty"`
	Attempts []string `json:"attempts,omitempty"`
	Children []string `json:"children,omitempty"`
	Wait     *Wait    `json:"wait,omitempty"`
	// MessageCursor is the highest message seq visible when the handoff was
	// written, so the next session can read only newer peer messages.
	MessageCursor uint64    `json:"message_cursor,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// Run is one executor invocation for one intent.
type Run struct {
	// ContextFactVersions records the record revisions of the intent's explicit
	// references/pins at the moment this run's input was frozen; it answers
	// "what did this run actually read" after later edits.
	ContextFactVersions map[string]uint64 `json:"context_fact_versions,omitempty"`
	// PremiseClaimVersions records the assertion versions of the required
	// premises, so a reviewer or the operator can tell whether the conclusion
	// was based on the claim that still stands.
	PremiseClaimVersions map[string]uint64 `json:"premise_claim_versions,omitempty"`
	// ResourceRefs names the registered execution-site resources this run
	// referenced (browser/action/wait/session projections). It is a reference
	// list, not ownership: the resource's live state and holder are read from
	// its own owner record.
	ResourceRefs        []string          `json:"resource_refs,omitempty"`
	InstructionVersions map[string]uint64 `json:"instruction_versions,omitempty"`
	InstructionNotices  map[string]uint64 `json:"instruction_notices,omitempty"`
	InstructionAcks     map[string]uint64 `json:"instruction_acks,omitempty"`
	DeclarationStart    *int64            `json:"declaration_start,omitempty"`
	PremiseVersions     map[string]uint64 `json:"premise_versions,omitempty"`
	PremisePolicy       string            `json:"premise_policy,omitempty"`
	CleanupPhase        string            `json:"cleanup_phase,omitempty"`
	ID                  string            `json:"id"`
	IntentID            string            `json:"intent_id"`
	// Node is the execution location that actually ran (not the one requested):
	// a vanished node must be visible in the ledger, not silently substituted.
	Node string `json:"node"`
	// Purpose is "" for an intent run, "verifier" for a review, "report" for a
	// report draft. It used to be encoded in Node, which stopped being true once
	// there was more than one node.
	Purpose string `json:"purpose,omitempty"`
	// Profile is the model configuration the intent asked for ("" means the
	// instance default). ResolvedProfile is the saved entry that actually ran,
	// and ProfileRevision fingerprints the binding at dispatch time, so a later
	// edit of the same profile does not rewrite history. Provider/Model are the
	// resolved values; Model is also corrected from the harness's session event
	// when it reports the model it really used.
	Profile         string `json:"profile,omitempty"`
	ResolvedProfile string `json:"resolved_profile,omitempty"`
	ProfileRevision string `json:"profile_revision,omitempty"`
	// Tier is the resolved binding's routing tier ("strong" / "worker") frozen
	// at dispatch, alongside the profile it belongs to. It is duplicated from
	// the settings catalogue on purpose: the catalogue is editable, and a run
	// must keep saying which tier it actually spent even after the profile is
	// retiered or deleted. Empty on old rows and on bindings without a tier
	// (omitempty), so those rows simply show no tier instead of "unknown".
	Tier     string `json:"tier,omitempty"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	// ProfileSource is where the binding came from: "explicit" when the intent
	// named a profile, "project_worker" when an empty profile fell back to the
	// project's worker-tier default, "default" for the instance active profile,
	// "commander_strong" for an explicit project commander profile, "fallback" when the control plane
	// switched to the profile's configured fallback after its credentials died.
	// A "_fallback" suffix on an inherited default records that the configured
	// default was paused and this new call selected another available profile.
	// Empty on old rows (D89).
	ProfileSource string `json:"profile_source,omitempty"`
	// FallbackFrom is set on the one new run created after a credential failure
	// switched profiles: the profile that ran out of keys. It bounds the switch
	// to a single hop (a fallback run never falls back again) and lets the
	// console answer "why did this run use a different model". Empty otherwise.
	FallbackFrom string `json:"fallback_from,omitempty"`
	// CredentialGeneration is the shared profile's credential generation this
	// run was started against. A provider failure is bound to the generation it
	// actually used, so a duplicate callback or an old key failing after a
	// rotation can never pause a project that already runs on a new key.
	CredentialGeneration uint64 `json:"credential_generation,omitempty"`
	// Usage records provider token observations. Pricing/Cost only round-trip
	// historical rows; new runs leave them nil.
	Usage   Usage    `json:"usage,omitempty"`
	Pricing *Pricing `json:"pricing,omitempty"`
	Cost    *Cost    `json:"cost,omitempty"`
	// HandoffFrom / HandoffVersion record that this run started as a replacement
	// session after a handoff, and which snapshot it consumed.
	HandoffFrom    string `json:"handoff_from,omitempty"`
	HandoffVersion int    `json:"handoff_version,omitempty"`
	// Generation is the intent's claim count at this run (matches the participant
	// token generation); a later replacement session increments it.
	Generation int        `json:"generation,omitempty"`
	Status     string     `json:"status"` // running|done|failed|canceled
	StartedAt  time.Time  `json:"started_at"`
	EndedAt    *time.Time `json:"ended_at,omitempty"`
	ExitReason string     `json:"exit_reason,omitempty"`
	// CancelCause is structured only when Status is canceled: steer / retry /
	// stop_loss / restart / watchdog / operator / worker_kill / pause /
	// review_pause. Empty for old rows.
	CancelCause string `json:"cancel_cause,omitempty"`
	Events      int    `json:"events"`
	// SessionID is the native harness identity, independent of this run ID.
	SessionID string `json:"session_id,omitempty"`
	// ToolFailures counts structured failed tool results, never prose matches.
	ToolFailures int `json:"tool_failures,omitempty"`
	// LastEventAt is a live pulse, not bookkeeping history: it is touched in
	// memory as events arrive so the board can tell a thinking run from a dead
	// one. The authoritative Events count is written when the run settles.
	LastEventAt *time.Time `json:"last_event_at,omitempty"`
	// LastTurnStartedAt is when the control plane saw this run's last
	// turn_started event. A failed run uses it to decide which messages were
	// delivered after the last turn began and may therefore not have been
	// drained/read yet (accepted messages UpdatedAt >= this time). Zero means the run
	// never started a turn: nothing it carried could have been read.
	LastTurnStartedAt *time.Time `json:"last_turn_started_at,omitempty"`
	DeclaredFacts     int        `json:"declared_facts"`
	// StopReason is the harness's own verdict on the turn (completed / canceled
	// / error). It sits next to the exit code because a harness can exit 0 after
	// a failed turn (docs/11-protocol.md).
	StopReason string `json:"stop_reason,omitempty"`
	// LastState is the last harness status payload (compaction, context
	// budget). It is diagnostic, not a ledger fact.
	LastState string `json:"last_state,omitempty"`
	// Compacted records that the harness compressed this run's session context
	// during the turn. Compaction is lossy, so a later continue treats the
	// session's memory as clipped and leans on `.tw/worker-note.md` and facts.
	Compacted bool `json:"compacted,omitempty"`
	// LastEventKind / LastEventTool / LastEventText are the most recent
	// meaningful executor event, touched in memory as events arrive. They are
	// the board's one-line glance ("it is calling bash, not stuck"); the whole
	// tail is the run_events read tool. Usage/delta events do not overwrite
	// them: "still alive" is not "doing something" (D57).
	LastEventKind string `json:"last_event_kind,omitempty"`
	LastEventTool string `json:"last_event_tool,omitempty"`
	LastEventText string `json:"last_event_text,omitempty"`
	// Warnings are non-fatal handoff problems the control plane noticed while
	// settling the run — a declared evidence path that is out of bounds or does
	// not exist, for example. They are operator diagnostics and the next
	// round's hint; they never change the run's outcome, they are deduped and
	// capped, and old rows simply have none (omitempty). They do not replace
	// the run event log: each one is also a `warning` event there.
	Warnings []string `json:"warnings,omitempty"`
	Tokens   int      `json:"tokens,omitempty"`
	// ContextTokens is the model's context size after the last call, touched in
	// memory while the run is live and written when it settles. ContextWindow
	// is the budget it is measured against, captured when the run starts.
	ContextTokens int `json:"context_tokens,omitempty"`
	// FinalContextTokens is the last context size the harness itself reported in
	// the turn (usage.detail.context_tokens / turn_ended.context_tokens). After
	// a harness compaction it is the post-compaction size while ContextTokens
	// keeps the pre-compaction peak; a continuation must budget against this
	// number because it is what the harness session still holds. 0 means the
	// harness did not report one (unknown, not an empty session).
	FinalContextTokens int    `json:"final_context_tokens,omitempty"`
	ContextWindow      int    `json:"context_window,omitempty"`
	LogPath            string `json:"log_path,omitempty"`
	// BriefPath is the archived snapshot of the dispatch prompt for a NEW
	// dispatch, relative to the case root (`<case>/.tw/briefs/<runID>.md`).
	// Resumes/wakes/handoffs do not write one (their prompt is already the
	// session's own continuation), and old rows have none (omitempty). Known
	// credentials are redacted before the file is written.
	BriefPath string `json:"brief_path,omitempty"`
	// ContinuedFrom is the run this one continues (a steer), "" for a fresh
	// dispatch. Two runs on one intent are otherwise indistinguishable from two
	// independent attempts in the ledger (D42).
	ContinuedFrom string `json:"continued_from,omitempty"`
}
