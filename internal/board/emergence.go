package board

import (
	"math"
	"strings"
	"time"

	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// workerParticipantPrefix is the id shape of a participant minted from an
// intent: collab.ParticipantID("in_x") is "pt_in_x". The "in_" part comes from
// the ledger's intent-id namespace, so a prefix match means "created by a
// running executor", never by the commander or the operator.
const workerParticipantPrefix = "pt_in_"

// EmergenceStats is the aggregate that answers "what did the model organise on
// its own", instead of making the operator reconstruct it from logs.
//
// Every field is derived from one ledger snapshot inside Build, so two boards
// of the same ledger agree; the only non-snapshot input is the clock behind
// HorizonHours. This is an observation, not a control: no dispatch, park,
// message or review path reads these numbers, nothing is filtered out of the
// board because of them, and ForPrompt drops the aggregate so the commander's
// prompt never shows the model its own scoreboard. A zero really means "the
// ledger shows none", not "we ignored it".
type EmergenceStats struct {
	// CommanderCreatedIntents answers "did the model open directions itself?"
	// It counts intents whose CreatedBy is the commander participant. Rows
	// written before CreatedBy existed carry "" and stay uncounted (they are
	// indistinguishable from operator/legacy rows, so they must not inflate
	// this number).
	CommanderCreatedIntents int `json:"commander_created_intents"`
	// WorkerCreatedIntents answers "did an executor extend the work graph?"
	// It counts intents created by a participant minted from an intent
	// ("pt_in_…"): worker-created children, and workers spawned via the
	// participant API.
	WorkerCreatedIntents int `json:"worker_created_intents"`
	// ChildIntents answers "how much of the graph is delegation?" It counts
	// every intent with a ParentID, whoever created it (commander or worker).
	ChildIntents int `json:"child_intents"`
	// SpawnedWorkers answers "how many individuals did the model create?"
	// The snapshot cannot separate /api/spawn from a worker creating a child
	// direction: both stamp CreatedBy "pt_in_…" and neither has a dedicated
	// marker, so this intentionally reuses WorkerCreatedIntents as the upper
	// bound. Do not read it as a separate spawn counter.
	SpawnedWorkers int `json:"spawned_workers"`
	// Handoffs answers "did the model change model/session on its own?" It
	// counts intents whose HandoffVersion > 0, i.e. a replacement was scripted
	// for the direction rather than it simply failing.
	Handoffs int `json:"handoffs"`
	// WorkerKills answers "did the model stop work by itself?" It counts runs
	// whose structured CancelCause is worker_kill (a participant killing a
	// worker it owns), not operator cancels, pauses or watchdog stops.
	WorkerKills int `json:"worker_kills"`
	// PeerMessages answers "do workers talk to each other?" It counts messages
	// whose sender is an intent participant ("pt_in_…").
	PeerMessages int `json:"peer_messages"`
	// ModelMessages answers "how much conversation is model-authored?" It
	// counts messages from any participant that is neither empty nor the
	// operator channel. Peer messages are a subset of these.
	ModelMessages int `json:"model_messages"`
	// ProfilesUsed answers "is model tiering actually exercised?" It counts
	// distinct ResolvedProfile values among intent runs (Purpose == "").
	// Purpose runs (commander/verifier/report) are excluded because they are
	// the fixed control-plane roles, not the model choosing a tier for its
	// work; empty resolved profiles on old rows are not a tier and are skipped.
	ProfilesUsed int `json:"profiles_used"`
	// HorizonHours gives the counts a rate: hours from the earliest intent's
	// CreatedAt to now, rounded up. 0 means "no intent has a timestamp yet".
	// A non-empty ledger therefore starts at 1, so a count/hour division never
	// uses a zero horizon.
	HorizonHours int `json:"horizon_hours"`
	// ParkedWithReason answers "were the self-stops evidenced?" It counts
	// parked intents that carry a ParkReason; a park with no reason is still a
	// park (Intents.Parked) but not a documented judgment.
	ParkedWithReason int `json:"parked_with_reason"`
}

// emergenceStats derives EmergenceStats from one snapshot. now is passed in so
// HorizonHours uses the same instant as the board's GeneratedAt.
func emergenceStats(db store.DB, now time.Time) EmergenceStats {
	var e EmergenceStats
	profiles := make(map[string]bool, 8)
	var earliest time.Time
	for _, in := range db.Intents {
		switch {
		case in.CreatedBy == collab.CommanderID:
			e.CommanderCreatedIntents++
		case strings.HasPrefix(in.CreatedBy, workerParticipantPrefix):
			e.WorkerCreatedIntents++
		}
		if in.ParentID != "" {
			e.ChildIntents++
		}
		if in.HandoffVersion > 0 {
			e.Handoffs++
		}
		if in.Status == model.StatusParked && in.ParkReason != "" {
			e.ParkedWithReason++
		}
		// A zero CreatedAt is a missing timestamp, not 1970: including it would
		// report a horizon of half a million hours on a legacy row.
		if !in.CreatedAt.IsZero() && (earliest.IsZero() || in.CreatedAt.Before(earliest)) {
			earliest = in.CreatedAt
		}
	}
	// See SpawnedWorkers: no dedicated spawn marker exists in the snapshot.
	e.SpawnedWorkers = e.WorkerCreatedIntents

	for _, r := range db.Runs {
		if r.CancelCause == model.CancelCauseWorkerKill {
			e.WorkerKills++
		}
		if r.Purpose == "" && r.ResolvedProfile != "" {
			profiles[r.ResolvedProfile] = true
		}
	}
	e.ProfilesUsed = len(profiles)

	for _, m := range db.Messages {
		if strings.HasPrefix(m.FromParticipantID, workerParticipantPrefix) {
			e.PeerMessages++
		}
		if m.FromParticipantID != "" && m.FromParticipantID != collab.OperatorID {
			e.ModelMessages++
		}
	}

	if !earliest.IsZero() {
		if hours := math.Ceil(now.Sub(earliest).Hours()); hours > 0 {
			e.HorizonHours = int(hours)
		}
	}
	return e
}
