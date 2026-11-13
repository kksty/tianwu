package model

// The association router's protocol labels. They are model-level because they
// are what a stored row means, not because the store hardcodes them: a notice row
// is distinguishable from a participant's message by its kind and by the sender
// identity, without parsing the text.

// NoticeSenderID is the mechanical author of a routed fact notice. It is not a
// participant: no run, token or session belongs to it, and it must never be
// rendered as the commander or the operator speaking. The control plane wrote
// the row; calling it "ledger" keeps that visible in the conversation.
const NoticeSenderID = "ledger"

// NoticeKindFact marks a "the record you rely on changed" notice. The body is a
// pointer (fact id, record version, change operation), never the fact text.
const NoticeKindFact = "fact_notice"

const NoticeKindTask = "task_notice"

// RouteCheckpoint is transport bookkeeping, independent of graph content and
// model read receipts. Cursor only advances after queue admission or an explicit
// determination that this recorded change is unrelated to the recipient.
type RouteCheckpoint struct {
	ID       string `json:"id"`
	IntentID string `json:"intent_id"`
	Cursor   uint64 `json:"cursor"`
	Deferred bool   `json:"deferred"`
}
