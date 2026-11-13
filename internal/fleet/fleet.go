// Package fleet aggregates every project's board, commander status and recent
// runs into one snapshot.
//
// It is the instance view, not the project view: the 态势 page wants "who is
// thinking, what is running, for how long" across all engagements, and asking
// /p/<id>/api/board once per project would both fan out N requests and make the
// page render at N different instants. The hub already owns every kit, so the
// snapshot is built server-side from the same stores the deciders read.
package fleet

import (
	"sort"
	"time"

	"github.com/kksty/tianwu/internal/board"
	"github.com/kksty/tianwu/internal/decider"
	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// RecentRuns is how many runs per project travel to the page: enough for a
// multi-hour timeline without shipping the whole ledger on every poll.
const RecentRuns = 30

// Source is one project as the hub owns it.
type Source struct {
	ID      string
	Name    string
	Scope   string
	Store   *store.Store
	Decider decider.Status
}

// Snapshot is the wire shape of GET /api/fleet.
type Snapshot struct {
	GeneratedAt time.Time `json:"generated_at"`
	Projects    []Project `json:"projects"`
	Nodes       []Node    `json:"nodes"`
}

// Project is one engagement's live slice.
type Project struct {
	ID       string           `json:"id"`
	Name     string           `json:"name"`
	Scope    string           `json:"scope,omitempty"`
	Decider  decider.Status   `json:"decider"`
	Intents  board.Counts     `json:"intents"`
	Facts    board.FactStats  `json:"facts"`
	Evidence int              `json:"evidence"`
	Goals    board.GoalStats  `json:"goals"`
	Runs     board.RunCounts  `json:"runs"`
	Usage    board.TokenUsage `json:"usage"`
	// Running and Open are the task rows: live directions and queued ones.
	Running []Task `json:"running"`
	Open    []Task `json:"open"`
	// RecentRuns feeds the timeline; running runs are included in both lists.
	RecentRuns []RunRow `json:"recent_runs"`
	// TokenSeries is sparse (one point per minute that spent tokens), covering
	// the last three hours. It is attributed to the moment a run finished,
	// because that is when the harness reports its token total.
	TokenSeries []TokenPoint `json:"token_series,omitempty"`
	// Tokens is the ledger's sum over the runs carried here, not an estimate.
	Tokens int `json:"tokens"`
}

// TokenPoint is one minute of spend for one project on one node.
type TokenPoint struct {
	At     time.Time `json:"at"`
	Tokens int       `json:"tokens"`
	Runs   int       `json:"runs,omitempty"`
	Node   string    `json:"node,omitempty"`
}

// Task is a direction as the row list needs it (no doc, no facts).
type Task struct {
	ID        string          `json:"id"`
	Title     string          `json:"title"`
	Node      string          `json:"node,omitempty"`
	Status    string          `json:"status"`
	Attempts  int             `json:"attempts"`
	RunID     string          `json:"run_id,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	Pulse     *board.RunPulse `json:"pulse,omitempty"`
}

// RunRow is one timeline bar.
type RunRow struct {
	ID          string     `json:"id"`
	IntentID    string     `json:"intent_id"`
	Title       string     `json:"title"`
	Node        string     `json:"node"`
	Status      string     `json:"status"`
	Purpose     string     `json:"purpose,omitempty"`
	StopReason  string     `json:"stop_reason,omitempty"`
	CancelCause string     `json:"cancel_cause,omitempty"`
	StartedAt   time.Time  `json:"started_at"`
	EndedAt     *time.Time `json:"ended_at,omitempty"`
	Events      int        `json:"events"`
	Tokens      int        `json:"tokens,omitempty"`
	Declared    int        `json:"declared_facts,omitempty"`
	// ContextTokens/ContextWindow are the run's peak context pressure. Running
	// runs carry the live value; finished ones keep what they reached.
	ContextTokens int `json:"context_tokens,omitempty"`
	ContextWindow int `json:"context_window,omitempty"`
}

// Health is one node's liveness at the last probe.
type Health struct {
	Online    bool       `json:"online"`
	Agents    int        `json:"agents,omitempty"`
	LatencyMS int64      `json:"latency_ms,omitempty"`
	CheckedAt *time.Time `json:"checked_at,omitempty"`
	Error     string     `json:"error,omitempty"`
}

// Node is one execution location, how many directions it is running right now,
// and whether it answered the last health probe. Running is assignment, not a
// health signal: a host can be online while idle, an audit node is "online" when
// its binary is present.
type Node struct {
	ID        string `json:"id"`
	Transport string `json:"transport,omitempty"`
	Endpoint  string `json:"endpoint,omitempty"`
	Note      string `json:"note,omitempty"`
	Running   int    `json:"running"`
	Health    Health `json:"health"`
}

// Build assembles the snapshot. now is passed in so a test (and every project
// in one response) shares one clock; health is the hub's last node probe.
func Build(now time.Time, sources []Source, nodes []executor.NodeInfo, health map[string]Health) Snapshot {
	out := Snapshot{
		GeneratedAt: now,
		Projects:    make([]Project, 0, len(sources)),
		Nodes:       make([]Node, 0, len(nodes)),
	}
	byNode := map[string]int{}

	for _, src := range sources {
		b := board.Build(src.Store, 20)
		intents := map[string]model.Intent{}
		for _, in := range src.Store.ListIntents() {
			intents[in.ID] = in
		}
		runs := src.Store.Runs()
		byRun := make(map[string]model.Run, len(runs))
		for _, r := range runs {
			byRun[r.ID] = r
		}

		p := Project{
			ID: src.ID, Name: src.Name, Scope: src.Scope,
			Decider: src.Decider,
			Intents: b.Intents, Facts: b.Facts, Evidence: b.Evidence,
			Goals: b.Goals, Runs: b.Runs,

			Usage:       board.Tokens(src.Store, now),
			Running:     make([]Task, 0, len(b.Running)),
			Open:        make([]Task, 0, len(b.Open)),
			TokenSeries: tokenSeries(runs, now),
		}

		for _, it := range b.Running {
			t := Task{
				ID: it.ID, Title: it.Title, Status: it.Status,
				Attempts: it.Attempts, CreatedAt: it.CreatedAt, Pulse: it.Pulse,
			}
			if in, ok := intents[it.ID]; ok {
				t.Node = in.Node
			}
			if it.Pulse != nil {
				t.RunID = it.Pulse.Run
				// The run records where it actually ran; the intent's node is
				// only the request (it may have fallen back).
				if r, ok := byRun[it.Pulse.Run]; ok && r.Node != "" {
					t.Node = r.Node
				}
			}
			if t.Node != "" {
				byNode[t.Node]++
			}
			p.Running = append(p.Running, t)
		}
		for _, it := range b.Open {
			p.Open = append(p.Open, Task{
				ID: it.ID, Title: it.Title, Status: it.Status,
				Attempts: it.Attempts, CreatedAt: it.CreatedAt,
			})
		}

		sort.Slice(runs, func(i, j int) bool { return runs[i].StartedAt.After(runs[j].StartedAt) })
		for _, r := range runs {
			if len(p.RecentRuns) >= RecentRuns {
				break
			}
			title := ""
			if in, ok := intents[r.IntentID]; ok {
				title = in.Title
			}
			p.RecentRuns = append(p.RecentRuns, RunRow{
				ID: r.ID, IntentID: r.IntentID, Title: title, Node: r.Node,
				Status: r.Status, Purpose: r.Purpose, StopReason: r.StopReason,
				CancelCause: r.CancelCause,
				StartedAt:   r.StartedAt, EndedAt: r.EndedAt,
				Events: r.Events, Tokens: r.Tokens, Declared: r.DeclaredFacts,
				ContextTokens: r.ContextTokens, ContextWindow: r.ContextWindow,
			})
			p.Tokens += r.Tokens
		}

		out.Projects = append(out.Projects, p)
	}

	sort.Slice(out.Projects, func(i, j int) bool {
		if out.Projects[i].Name == out.Projects[j].Name {
			return out.Projects[i].ID < out.Projects[j].ID
		}
		return out.Projects[i].Name < out.Projects[j].Name
	})
	for _, n := range nodes {
		out.Nodes = append(out.Nodes, Node{
			ID: n.ID, Transport: n.Transport, Endpoint: n.Endpoint, Note: n.Note,
			Running: byNode[n.ID], Health: health[n.ID],
		})
	}
	return out
}

// tokenSeries buckets finished runs by their end minute (the moment the
// harness reports the total) and keeps the last three hours. Sparse on
// purpose: a quiet minute is not worth a JSON object, and the console fills the
// gaps for its window.
func tokenSeries(runs []model.Run, now time.Time) []TokenPoint {
	end := now.Truncate(time.Minute)
	start := end.Add(-179 * time.Minute)
	type key struct {
		at   time.Time
		node string
	}
	byBucket := map[key]*TokenPoint{}
	for _, r := range runs {
		if r.Tokens <= 0 {
			continue
		}
		at := r.StartedAt
		if r.EndedAt != nil {
			at = *r.EndedAt
		}
		at = at.Truncate(time.Minute)
		if at.Before(start) || at.After(end) {
			continue
		}
		k := key{at: at, node: r.Node}
		point := byBucket[k]
		if point == nil {
			point = &TokenPoint{At: at, Node: r.Node}
			byBucket[k] = point
		}
		point.Tokens += r.Tokens
		point.Runs++
	}
	out := make([]TokenPoint, 0, len(byBucket))
	for _, point := range byBucket {
		out = append(out, *point)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].At.Equal(out[j].At) {
			return out[i].Node < out[j].Node
		}
		return out[i].At.Before(out[j].At)
	})
	return out
}
