// Package executor runs one intent and streams events back to the ledger.
//
// The Executor interface is the escape hatch: an intent normally runs on a
// harness host over AOP (internal/aopexec), and cyber-audit is the one local
// process left (internal/executor/audit.go). The ledger and the API do not care
// which one answered.
package executor

import (
	"context"
	"encoding/json"
	"github.com/kksty/tianwu/internal/aop/operation"
	"github.com/kksty/tianwu/internal/aop/tool"
	"github.com/kksty/tianwu/internal/aop/traffic"
	"strings"
	"time"
)

// MaxTurnsCap bounds Request.MaxTurns. Zero means "no cap" (the harness's own
// default); the bound exists because the value travels to the transport as a
// uint32, where a negative int becomes four billion turns.
const MaxTurnsCap = 500

// Event kinds. They mirror the payload names of cyber-harness's AOP Event
// (aop.Event oneof), so a reader can map one to the other without guessing
// (docs/11-protocol.md).
const (
	KindMessage      = "message"       // message
	KindMessageDelta = "message_delta" // streamed token; folded into one message by runProcess
	KindToolCall     = "tool_call"     // tool_call
	KindToolResult   = "tool_result"   // tool_result
	KindTurnStart    = "turn_started"
	KindTurnEnd      = "turn_ended"
	KindUsage        = "usage"
	KindError        = "error"
	KindStatus       = "status"
	KindSession      = "session_started"
	KindSessionEnd   = "session_ended"
	KindRaw          = "raw" // not parseable as an AOP event; kept verbatim
)

// Event is the smallest common shape every executor can produce.
type Event struct {
	// Native is carried only between execution and capture. The journal stores
	// its opaque reference, never a second copy of potentially sensitive bytes.
	Native          *tool.Artifact `json:"-"`
	NativeEventID   string         `json:"-"`
	ArtifactRef     string         `json:"artifact_ref,omitempty"`
	NativeFlow      *traffic.Flow  `json:"-"`
	NativeOperation *operation.Ref `json:"-"`
	FlowRef         string         `json:"flow_ref,omitempty"`
	Kind            string         `json:"kind"`
	SessionID       string         `json:"session_id,omitempty"`
	At              time.Time      `json:"at"`
	// Tool is the tool name for tool_call / tool_result.
	Tool string `json:"tool,omitempty"`
	// CallID pairs a tool_result with the tool_call it answers. The AOP
	// protocol carries it (ToolResult.call_id); older journals may not, so the
	// console pairs those by recorded order instead.
	CallID string `json:"call_id,omitempty"`
	// DurationMs is the tool's own execution time as the harness measured it
	// (ToolResult.duration_ms). 0 means it was not reported, not that the tool
	// finished instantly.
	DurationMs int `json:"duration_ms,omitempty"`
	// Role is "assistant" / "user" / "tool" for message events.
	Role string `json:"role,omitempty"`
	// Text is the human-readable payload, clipped.
	Text string `json:"text,omitempty"`
	// OK is meaningful for tool_result (is_error) and turn_ended (no error).
	OK bool `json:"ok,omitempty"`
	// StopReason comes from turn_ended ("completed", "canceled", "error", …).
	StopReason string `json:"stop_reason,omitempty"`
	// State is the harness's status payload (compaction, context budget, …).
	// It is not a fact; it is why the decider may decide to steer or split.
	State string `json:"state,omitempty"`
	// Tokens is the turn total when the event carries usage.
	Tokens int `json:"tokens,omitempty"`
	// InputTokens / OutputTokens / CachedInputTokens are the provider-reported
	// breakdown; 0 means the provider did not report that field for this event,
	// not that the call used nothing.
	InputTokens       int `json:"input_tokens,omitempty"`
	OutputTokens      int `json:"output_tokens,omitempty"`
	CachedInputTokens int `json:"cached_input_tokens,omitempty"`
	CacheWriteTokens  int `json:"cache_write_tokens,omitempty"`
	// UsageRequests / UsageMissing count model calls and calls that returned no
	// usage, so a partially reported turn stays partially unknown.
	UsageRequests int `json:"usage_requests,omitempty"`
	UsageMissing  int `json:"usage_missing,omitempty"`
	// ContextTokens is the model context size after the call (usage.detail's
	// context_tokens, or turn_ended.context_tokens). The console turns it into a
	// context-pressure bar while the run is live.
	ContextTokens int `json:"context_tokens,omitempty"`
	// ContextWindow is the model's window as the harness resolved it
	// (usage.detail's context_window). 0 means the harness did not report one;
	// the control plane then has to fall back to its own profile setting rather
	// than re-deriving the harness's model table.
	ContextWindow int             `json:"context_window,omitempty"`
	Raw           json.RawMessage `json:"raw,omitempty"`
}

// Request is one dispatch. Prompt is built by the ledger from the intent;
// Skills are absolute skill file paths; the transport carries references to
// those files without reimplementing harness skill expansion.
type Request struct {
	IntentID string
	Prompt   string
	// Goal is the control-plane goal shown to a harness evaluator. It stays
	// short; Prompt remains the executor's full brief. A transport that cannot
	// carry a separate goal may put Prompt in a case file and send Goal instead;
	// it must never feed the executor brief to the evaluator as Goal.
	Goal    string
	WorkDir string
	// Node is the execution location: an id from the node registry, "" for the
	// default one (docs/14-nodes.md). The executor that receives the request
	// resolves it and reports back which node actually ran.
	Node string
	// Purpose says why the harness runs: "" for an intent, "verifier" for a
	// review, "report" for a report draft. It is not a location.
	Purpose string
	// Resume asks for a continued turn on the case's existing trajectory
	// (docs/06, D42): the prompt is then the new instruction, not the whole
	// brief, and the harness rebuilds context from session.aop.jsonl. There is
	// no silent fallback here: without a trajectory the run is refused, because
	// a lone note is not a brief (the caller degrades to a fresh dispatch).
	Resume bool
	Skills []string
	// Eval is the optional harness goal-evaluation criterion (-e). Empty keeps
	// the default stop condition (the agent stops calling tools).
	Eval string
	// MaxTurns is an optional AOP turn cap. Audit has no equivalent flag; a
	// transport that cannot honour it ignores it.
	MaxTurns int
	// Profile is the saved model configuration the intent asked for. Empty
	// means the instance default. Only the local node can honour it (it owns a
	// per-profile host pool); a remote host has its own configuration and a
	// transport that cannot bind must refuse rather than run with another
	// model.
	Profile string
}

// NodeInfo is one execution location as the console sees it.
type NodeInfo struct {
	ID   string `json:"id"`
	Note string `json:"note,omitempty"`
	// Default marks the node an unspecified request lands on.
	Default bool `json:"default,omitempty"`
	// Transport is how a request reaches this node: "aop" (a harness host,
	// which the local machine also is) or "audit" (a local cyber-audit run).
	Transport string `json:"transport,omitempty"`
	// Endpoint is the AOP host URL, when the transport is aop.
	Endpoint string `json:"endpoint,omitempty"`
	// Bin is the audit binary; empty for an AOP node.
	Bin string `json:"bin,omitempty"`
}

// NodeAware is implemented by executors that route by node id. The API uses it
// to reject an intent that names a node nobody has, instead of silently running
// it somewhere else.
type NodeAware interface {
	Nodes() []NodeInfo
	// ResolveNode maps a requested id to the id that would actually run: the
	// default node for "", and for an id that is not registered.
	ResolveNode(requested string) string
}

// HasNode reports whether ex knows this node id. A single-location executor
// only knows "local" (or ""), which is what the first implementation was.
func HasNode(ex Executor, id string) bool {
	if na, ok := ex.(NodeAware); ok {
		if id == "" {
			return true
		}
		for _, n := range na.Nodes() {
			if n.ID == id {
				return true
			}
		}
		return false
	}
	return id == "" || id == "local"
}

// TerminalNode is implemented by executors that can open a shell on a node.
// An AOP host exposes one directly; the local machine is such a host because tw
// starts an `aiscan web` for it (D64).
type TerminalNode interface {
	TerminalEndpoint(nodeID string) (baseURL, token, agent string, ok bool)
}

// Resumable is implemented by executors that know, per node, whether a case has
// something to continue. "Something" is transport-specific: the host keeps a
// session, audit has nothing to continue (D42/D43). Asking the executor instead
// of checking one filename keeps steer working without the API knowing which
// transport is behind the node.
type Resumable interface {
	CanResume(node, workDir string) bool
}

// ProfileResumable verifies the selected host, not just presence of a marker.
type ProfileResumable interface {
	CanResumeProfile(node, profile, workDir string) bool
}

func CanResumeProfile(ex Executor, node, profile, workDir string) bool {
	if r, ok := ex.(ProfileResumable); ok {
		return r.CanResumeProfile(node, profile, workDir)
	}
	if profile == "" {
		return CanResume(ex, node, workDir)
	}
	return false
}

// CanResume asks the executor. An executor that does not answer has no
// trajectory the API could name, so the answer is "no" rather than a guess.
func CanResume(ex Executor, node, workDir string) bool {
	if r, ok := ex.(Resumable); ok {
		return r.CanResume(node, workDir)
	}
	return false
}

// IsCompactionState reports whether a harness status payload is about context
// compaction. The protocol states are compact_start / compact_end /
// compact_error; older harnesses wrote "compacting context (82%)", so the match
// is on the word, not on an exact enum. A status that is not about compaction
// (token_budget_warning, interrupted, …) must not be read as one.
func IsCompactionState(state string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(state)), "compact")
}

// Result is what the executor reports after the process exits.
type Result struct {
	ExitCode int
	Events   int
	Output   string // stderr, trimmed
	// The fields below come from the AOP stream, not from the exit code: a
	// harness can exit 0 after a failed turn, and that must not read as success.
	StopReason string
	IsError    bool
	Tokens     int
	// Token breakdown and completeness from the harness turn_ended usage.
	InputTokens       int
	OutputTokens      int
	CachedInputTokens int
	CacheWriteTokens  int
	UsageRequests     int
	UsageMissing      int
	UsageKnown        bool
	// ContextTokens is the largest context size reported during the turn; it is
	// the peak pressure the run reached, not a completion percentage.
	ContextTokens int
	// ContextWindow is the model window the harness resolved for this turn. It
	// is the same number the harness compacts against, so the 85% handoff line
	// does not depend on a duplicated model table (D80).
	ContextWindow int
	// FinalContextTokens is the last context size the harness reported in the
	// turn. It is what the next commander round measures pressure against:
	// ContextTokens can be a pre-compaction peak and would schedule a second
	// handoff after the context was already reset.
	FinalContextTokens int
	// Compacted reports that the harness compressed the session's context during
	// this turn (status compact_start/compact_end). The next commander round is
	// then a "continue from your note" round, not a fresh investigation.
	Compacted bool
	// Text is the last assistant message: what the run said it did.
	Text string
	// LastState is the last status payload (compaction / context-budget
	// warnings). It is recorded on the run so a human can see why it was
	// steered or split, without replaying the whole log.
	LastState string
	// ResumeGone is set when the run asked to continue a session the host no
	// longer has (restart / DB replaced: the host answers NOT_FOUND). It is
	// structured, not parsed from prose, so dispatch can fall back to a fresh
	// session once and record that it did.
	ResumeGone bool
}

type Executor interface {
	Name() string
	Run(ctx context.Context, req Request, emit func(Event)) (Result, error)
}

// ResolveNode asks an executor which node a request would land on. Callers that
// record a run use it so the ledger says where work happened, not where it was
// hoped to happen.
func ResolveNode(ex Executor, requested string) string {
	if na, ok := ex.(NodeAware); ok {
		return na.ResolveNode(requested)
	}
	if strings.TrimSpace(requested) == "" {
		return "local"
	}
	return requested
}
