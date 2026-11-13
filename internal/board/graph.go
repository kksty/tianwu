package board

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/retrieval"
	"github.com/kksty/tianwu/internal/store"
)

// GraphVersion is the executor snapshot format. Bump only when an old reader
// would misread a newer file.
const GraphVersion = 1

// Budgets and caps. A snapshot is a file, not a prompt, but a run that reads it
// whole pays for every byte, so it stays bounded and says when it was cut.
//
// The budgets are sized for the model window the run may be on, not for a
// campaign's total facts: a 128k-window worker must be able to read the file and
// still have room for its brief and tool results. A long campaign therefore
// truncates the oldest low-value facts (host/service/vuln/cred survive first)
// instead of growing the snapshot without limit — the board stays the summary
// layer and facts remain readable by id.
const (
	graphFactsBudget = 192 << 10
	graphNotesBudget = 32 << 10
	graphMaxBytes    = 256 << 10

	graphOpenCap    = 40
	graphRunningCap = 20
	graphParkedCap  = 15
	graphDoneCap    = 30
	graphGoalCap    = 20

	graphTitleRunes   = 160
	graphDescRunes    = 400
	graphSummaryRunes = 240
	graphNoteRunes    = 600
	graphReasonRunes  = 200
)

// Graph is the executor's read-only snapshot of the ledger, written to
// <workdir>/.tw/graph.json at dispatch.
//
// It carries raw observations and the conclusions someone actually wrote down:
// facts, notes, the other intents and the goals. It deliberately carries no
// port aggregate and no "family" verdict — grouping services is the agent's
// judgement (D54), and a snapshot that pre-grouped them would be the program
// deciding what belongs together. It carries no target either: an intent says
// what it is doing in prose, and nothing here derives a host lease.
type Graph struct {
	Context     *TaskContext `json:"context,omitempty"`
	Version     int          `json:"version"`
	GeneratedAt time.Time    `json:"generated_at"`
	Scope       string       `json:"scope,omitempty"`
	// EvidenceBase says how to read every evidence URI in this snapshot. The
	// ledger stores URIs relative to the cases root ("in_x/file"), but the
	// executor's cwd is one case below it, and AOP resolves paths from the host
	// root. Writing "case_dir" plus the "../" prefix in each row is the only
	// form that both transports can resolve without knowing the other's cwd.
	EvidenceBase string         `json:"evidence_base,omitempty"`
	Goals        []GraphGoal    `json:"goals,omitempty"`
	Intents      GraphIntents   `json:"intents"`
	Facts        []GraphFact    `json:"facts,omitempty"`
	Notes        []GraphNote    `json:"notes,omitempty"`
	Totals       GraphTotals    `json:"totals"`
	Truncated    GraphTruncated `json:"truncated"`
}

type TaskContext struct {
	IntentID      string `json:"intent_id"`
	Prerequisites int    `json:"prerequisites"`
	Manifest      string `json:"manifest"`
	Query         string `json:"query"`
}

// GraphIntents is the intent list split by the only distinction a run cares
// about: what is queued, what is moving now, what was set aside, what is done.
type GraphIntents struct {
	Open    []GraphIntent `json:"open,omitempty"`
	Running []GraphIntent `json:"running,omitempty"`
	Parked  []GraphIntent `json:"parked,omitempty"`
	Done    []GraphIntent `json:"done,omitempty"`
}

// GraphIntent is one direction. Running intents keep their description and
// from_facts because that prose is the only place "what host is this about"
// lives: the program does not derive a target.
type GraphIntent struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Status      string   `json:"status"`
	Description string   `json:"description,omitempty"`
	FromFacts   []string `json:"from_facts,omitempty"`
	Node        string   `json:"node,omitempty"`
	Run         string   `json:"run,omitempty"`
	Attempts    int      `json:"attempts,omitempty"`
	ParkReason  string   `json:"park_reason,omitempty"`
	LastStop    string   `json:"last_stop,omitempty"`
}

// GraphFact is one compact fact: the summary layer plus evidence URIs. The doc
// body (commands, raw output) is deliberately absent: it stays in the ledger and
// is read by id (docs/17 §3.1). Shipping it made every snapshot carry pages of
// prose that each new short-lived run had to pay for.
type GraphFact struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`
	Title      string    `json:"title"`
	Summary    string    `json:"summary,omitempty"`
	Status     string    `json:"status,omitempty"`
	Confidence string    `json:"confidence,omitempty"`
	Origin     string    `json:"origin,omitempty"`
	Intent     string    `json:"intent,omitempty"`
	Tags       []string  `json:"tags,omitempty"`
	Evidence   []string  `json:"evidence,omitempty"`
	At         time.Time `json:"at"`
}

// GraphNote is a note fact in full enough to be reused: dead ends, questions
// and the decider's family judgements all live here as prose.
type GraphNote struct {
	ID      string    `json:"id"`
	Title   string    `json:"title"`
	Summary string    `json:"summary,omitempty"`
	Intent  string    `json:"intent,omitempty"`
	At      time.Time `json:"at"`
}

type GraphGoal struct {
	ID         string `json:"id"`
	Statement  string `json:"statement"`
	Start      string `json:"start,omitempty"`
	Quantified string `json:"quantified_target,omitempty"`
	Met        bool   `json:"met"`
}

// GraphTotals is what existed before truncation, so a cut is visible as a
// number instead of a silent absence.
type GraphTotals struct {
	Facts   int `json:"facts"`
	Notes   int `json:"notes"`
	Open    int `json:"open"`
	Running int `json:"running"`
	Parked  int `json:"parked"`
	Done    int `json:"done"`
}

type GraphTruncated struct {
	Facts   bool `json:"facts,omitempty"`
	Notes   bool `json:"notes,omitempty"`
	Intents bool `json:"intents,omitempty"`
}

// BuildGraph is the exported form used by dispatch. now is a parameter of the
// internal builder so a test can compare two snapshots byte for byte.
func BuildGraph(db store.DB, scope string) Graph {
	return buildGraph(db, scope, time.Now(), graphFactsBudget, graphNotesBudget)
}

func BuildTaskGraph(db store.DB, scope, intentID string) Graph {
	// Put the task itself ahead of presentation-only intent caps.
	db.Intents = append([]model.Intent(nil), db.Intents...)
	for i, in := range db.Intents {
		if in.ID == intentID {
			db.Intents = append(append(db.Intents[:i:i], db.Intents[i+1:]...), in)
			break
		}
	}
	g := buildGraph(db, scope, time.Now(), graphFactsBudget, graphNotesBudget, retrieval.TaskSources(db, intentID)...)
	count := 0
	for _, in := range db.Intents {
		if in.ID == intentID {
			count = len(in.FromFactIDs)
		}
	}
	g.Context = &TaskContext{IntentID: intentID, Prerequisites: count, Manifest: ".tw/context.json", Query: "/api/graph?root=intent:" + intentID + "&direction=ancestors&depth=0"}
	g.Goals = nil
	return g
}

func buildGraph(db store.DB, scope string, now time.Time, factsBudget, notesBudget int, pinnedIDs ...string) Graph {
	pinned := map[string]int{}
	for i, id := range pinnedIDs {
		pinned[id] = i + 1
	}
	g := Graph{
		Version:      GraphVersion,
		GeneratedAt:  now,
		Scope:        graphClip(scope, 500),
		EvidenceBase: "case_dir",
	}

	for _, goal := range db.Goals {
		if len(g.Goals) >= graphGoalCap {
			break
		}
		g.Goals = append(g.Goals, GraphGoal{
			ID:         goal.ID,
			Statement:  graphClip(goal.Statement, graphDescRunes),
			Start:      graphClip(goal.Start, graphDescRunes),
			Quantified: graphClip(goal.QuantifiedTarget, graphDescRunes),
			Met:        goal.Met,
		})
	}

	runs := make(map[string]model.Run, len(db.Runs))
	for _, r := range db.Runs {
		runs[r.ID] = r
	}
	for i := len(db.Intents) - 1; i >= 0; i-- {
		in := db.Intents[i]
		switch in.Status {
		case model.StatusPending, model.StatusClaimed, model.StatusFailed:
			g.Totals.Open++
			if len(g.Intents.Open) < graphOpenCap {
				g.Intents.Open = append(g.Intents.Open, graphIntentOf(in, runs))
			}
		case model.StatusRunning:
			g.Totals.Running++
			if len(g.Intents.Running) < graphRunningCap {
				g.Intents.Running = append(g.Intents.Running, graphIntentOf(in, runs))
			}
		case model.StatusParked:
			g.Totals.Parked++
			if len(g.Intents.Parked) < graphParkedCap {
				g.Intents.Parked = append(g.Intents.Parked, graphIntentOf(in, runs))
			}
		case model.StatusDone:
			g.Totals.Done++
			if len(g.Intents.Done) < graphDoneCap {
				g.Intents.Done = append(g.Intents.Done, GraphIntent{
					ID:     in.ID,
					Title:  graphClip(in.Title, graphTitleRunes),
					Status: in.Status,
				})
			}
		}
	}
	if g.Totals.Open > len(g.Intents.Open) ||
		g.Totals.Running > len(g.Intents.Running) ||
		g.Totals.Parked > len(g.Intents.Parked) ||
		g.Totals.Done > len(g.Intents.Done) {
		g.Truncated.Intents = true
	}

	byEvidence := make(map[string]string, len(db.Evidence))
	for _, e := range db.Evidence {
		byEvidence[e.ID] = e.URI
	}
	facts := make([]model.Fact, 0, len(db.Facts))
	for _, f := range db.Facts {
		if f.Kind != "note" {
			facts = append(facts, f)
		}
	}
	// Retention order, not importance: the map (host/service) and findings
	// survive a cut first, then everything else newest-first. Nothing here is a
	// judgement about what a fact means.
	sort.SliceStable(facts, func(i, j int) bool {
		pi, pj := pinned[facts[i].ID], pinned[facts[j].ID]
		if pi != pj && (pi > 0 || pj > 0) {
			if pi == 0 {
				return false
			}
			if pj == 0 {
				return true
			}
			return pi < pj
		}
		ri, rj := graphKindRank(facts[i].Kind), graphKindRank(facts[j].Kind)
		if ri != rj {
			return ri < rj
		}
		if !facts[i].CreatedAt.Equal(facts[j].CreatedAt) {
			return facts[i].CreatedAt.After(facts[j].CreatedAt)
		}
		return facts[i].ID < facts[j].ID
	})
	g.Totals.Facts = len(facts)
	used := 0
	for _, f := range facts {
		row := graphFactOf(f, byEvidence)
		b, err := json.Marshal(row)
		if err != nil {
			continue
		}
		if used+len(b)+2 > factsBudget {
			g.Truncated.Facts = true
			break
		}
		g.Facts = append(g.Facts, row)
		used += len(b) + 2
	}

	notes := make([]model.Fact, 0, 16)
	for i := len(db.Facts) - 1; i >= 0; i-- {
		if db.Facts[i].Kind == "note" {
			notes = append(notes, db.Facts[i])
		}
	}
	g.Totals.Notes = len(notes)
	sort.SliceStable(notes, func(i, j int) bool {
		pi, pj := pinned[notes[i].ID], pinned[notes[j].ID]
		if pi == pj {
			return false
		}
		if pi == 0 {
			return false
		}
		if pj == 0 {
			return true
		}
		return pi < pj
	})
	used = 0
	for _, f := range notes {
		row := GraphNote{
			ID:      f.ID,
			Title:   graphClip(f.Title, graphTitleRunes),
			Summary: graphClip(f.Summary, graphNoteRunes),
			Intent:  f.IntentID,
			At:      f.CreatedAt,
		}
		b, err := json.Marshal(row)
		if err != nil {
			continue
		}
		if used+len(b)+2 > notesBudget {
			g.Truncated.Notes = true
			break
		}
		g.Notes = append(g.Notes, row)
		used += len(b) + 2
	}

	// The per-section budgets already keep the file well under the cap; this is
	// the backstop for descriptions and timestamps that grow unexpectedly.
	for len(g.Facts) > 0 {
		b, err := json.Marshal(g)
		if err != nil || len(b) <= graphMaxBytes {
			break
		}
		g.Facts = g.Facts[:len(g.Facts)*9/10]
		g.Truncated.Facts = true
	}
	return g
}

func graphIntentOf(in model.Intent, runs map[string]model.Run) GraphIntent {
	g := GraphIntent{
		ID:          in.ID,
		Title:       graphClip(in.Title, graphTitleRunes),
		Status:      in.Status,
		Description: graphClip(in.Description, graphDescRunes),
		Node:        in.Node,
		Run:         in.RunID,
		Attempts:    in.Attempts,
		ParkReason:  graphClip(in.ParkReason, graphReasonRunes),
	}
	g.FromFacts = append([]string(nil), in.FromFactIDs...)
	if r, ok := runs[in.RunID]; ok {
		// The snapshot is read by the executor. Keep the structured state, not
		// the harness exit log: the log is for the operator and can contain
		// evaluator internals ("model did not call verdict tool") that are not
		// instructions for the next run.
		reason := r.StopReason
		if r.Status == model.RunFailed && strings.TrimSpace(r.LastState) != "" {
			reason = r.LastState
		}
		if reason == "" {
			reason = r.LastState
		}
		g.LastStop = graphClip(reason, graphReasonRunes)
	}
	return g
}

func graphFactOf(f model.Fact, byEvidence map[string]string) GraphFact {
	row := GraphFact{
		ID:         f.ID,
		Kind:       f.Kind,
		Title:      graphClip(f.Title, graphTitleRunes),
		Summary:    graphClip(f.Summary, graphSummaryRunes),
		Status:     f.Status,
		Confidence: f.Confidence,
		Origin:     f.Origin,
		Intent:     f.IntentID,
		Tags:       append([]string(nil), f.Tags...),
		At:         f.CreatedAt,
	}
	for _, id := range f.EvidenceIDs {
		if uri, ok := byEvidence[id]; ok && uri != "" {
			// The ledger URI is cases-root relative ("in_x/file"); the executor
			// resolves from its own case dir, so the parent hop is what makes it
			// openable as-is. AOP has the same shape (the prompt names the case
			// dir and the agent joins relative to it).
			uri = strings.TrimPrefix(uri, "./")
			if !strings.HasPrefix(uri, "../") {
				uri = "../" + uri
			}
			row.Evidence = append(row.Evidence, uri)
		}
	}
	return row
}

// graphKindRank is retention order only: the host/service map first, findings
// next, the rest after. It does not claim one kind is more true than another.
func graphKindRank(kind string) int {
	switch kind {
	case "host":
		return 0
	case "service":
		return 1
	case "vuln":
		return 2
	case "cred":
		return 3
	case "loot":
		return 4
	case "artifact":
		return 5
	default:
		return 6
	}
}

func graphClip(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
