package board

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Digest answers "has anything changed since last round" for the decider
// (docs/06, D20).
//
// It hashes counts, the bounded id lists and the ledger's write counter, so its
// cost is constant in graph size. A digest hit means the round can be skipped
// without a model call — which is the cheapest decision the decider ever makes.
//
// The write counter is what makes a hit mean "nothing changed" rather than
// "nothing changed shape" (D79): rewriting a fact's title/doc/summary, adding a
// ref, or editing a fact that sits outside the top-N leaves every count and
// every bounded id list identical, and used to be silently skipped. Any durable
// write now changes the digest; live pulse touches (store.NoteEvent and
// ProgressRun) do not, so a busy run still does not wake the decider per event.
func Digest(v View) string { return digest(v, true) }

// ContentDigest hashes only what a report is built from: intents, facts,
// evidence and goals. Run bookkeeping is left out on purpose, and so is the
// ledger write counter: it answers "should the decider wake up", not "is the
// report still true", and it restarts from zero with the process (D79).
//
// Verified and WithRefs are part of it because a report's verdicts come from
// fact.status: a review that turns candidate into confirmed changes what the
// report should say, so it must make the old report stale (D27/D52).
//
// Generating a report adds a run; a report that turns stale because it was
// written would be stale the moment it exists. The acceptance run found exactly
// that: nine of nine checks passed and the report still showed "过期".
func ContentDigest(v View) string {
	if v.ContentHash != "" {
		return v.ContentHash
	}
	return digest(v, false)
}

func digest(v View, withRuns bool) string {
	h := sha256.New()
	fmt.Fprintf(h, "i:%d,%d,%d,%d,%d,%d,%d,%d|f:%d,%d,%d,%d,%d,%d|e:%d|g:%d,%d",
		v.Intents.Total, v.Intents.Pending, v.Intents.Held, v.Intents.Claimed, v.Intents.Running,
		v.Intents.Done, v.Intents.Failed, v.Intents.Parked,
		v.Facts.Total, v.Facts.WithEvidence, v.Facts.Verified, v.Facts.WithRefs,
		v.Facts.Unverified, v.Facts.Rejected,
		v.Evidence,
		v.Goals.Total, v.Goals.Met)
	if withRuns {
		fmt.Fprintf(h, "|r:%d,%d,%d,%d|w:%d",
			v.Runs.Total, v.Runs.Running, v.Runs.Done, v.Runs.Canceled, v.Revision)
	}
	for _, it := range v.Open {
		fmt.Fprintf(h, "|o:%s", it.ID)
	}
	for _, it := range v.Running {
		fmt.Fprintf(h, "|r:%s", it.ID)
	}
	for _, it := range v.RecentFacts {
		fmt.Fprintf(h, "|f:%s:%d", it.ID, it.Evidence)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}
