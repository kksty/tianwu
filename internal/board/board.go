// Package board builds the decider's fixed-shape view of the engagement.
//
// The board's output is bounded: counts plus a bounded top-N. Depth comes
// from explicit queries (see /api/facts, /api/evidence), never from stuffing
// the whole graph into a prompt. This is what replaces Tessera's hot/cold
// projection budgets.
package board

import (
	"net"
	"sort"
	"strings"
	"time"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

type View struct {
	ContentHash string `json:"-"`
	// Supervision retains all live intent pulses before the presentation top-N.
	Supervision []RunPulse `json:"-"`
	GeneratedAt time.Time  `json:"generated_at"`
	// Revision is the ledger's durable-write counter. It is not part of the
	// console's board (json:"-"), only of the wake-up digest: counts cannot see
	// a fact's title/doc/summary being rewritten, or a row edited after it fell
	// out of the top-N, so the digest asks the store "has anything been written"
	// instead of inferring it from the shape (D79). See store.Revision.
	Revision uint64    `json:"-"`
	Intents  Counts    `json:"intents"`
	Facts    FactStats `json:"facts"`
	Evidence int       `json:"evidence"`
	Runs     RunCounts `json:"runs"`
	Goals    GoalStats `json:"goals"`
	Open     []Item    `json:"open_intents"`
	// Held are pending directions the operator/commander explicitly stopped.
	// They stay visible (the work still exists) but dispatchPending skips them
	// until an explicit retry/run clears Hold.
	Held []Item `json:"held_intents,omitempty"`
	// Waiting are directions whose turn yielded for child intents. They are not
	// in Open (nothing should dispatch them) but the commander still needs to
	// see what the project is blocked on.
	Waiting     []Item `json:"waiting_intents,omitempty"`
	Running     []Item `json:"running_intents"`
	RecentFacts []Item `json:"recent_facts"`
	// Done is titles only: the decider needs to know what has already been
	// covered, not the details of it.
	Done []string `json:"done_intents"`
	// Frontier is the graph as memory: each open direction says whether the
	// facts it derives from still exist and still stand. A direction whose
	// premise was rejected is not "pending work", it is dead work.
	Frontier []FrontierItem `json:"frontier,omitempty"`
	// Ports is what the ledger observed: which hosts hold a service fact on a
	// given port. It is an aggregate, not a family judgment — two hosts on the
	// same port are not necessarily one product, and one product may sit on
	// several ports. Deciding that is the decider's job, from facts (D54).
	Ports []PortGroup `json:"ports,omitempty"`
	// Coverage is the known-host map: which hosts the ledger has seen, how many
	// services, when, and whether any open/running direction mentions them. It
	// is derived from facts, not from the scope text: the program does not
	// parse scope (D59), it only shows what was actually observed.
	Coverage []CoverageItem `json:"coverage,omitempty"`
	// Playbooks is the size of the project's knowledge table, never its text.
	// It is not a fact count: a playbook is not a finding, is never verified
	// and never enters a report (D70). Candidates are shown because the
	// commander is the only one who can dedupe or promote them.
	Playbooks PlaybookStats `json:"playbooks"`
	// Redispatch counts starts beyond a direction's first attempt: retries,
	// wake/steer resumes and post-handoff generations. It is the sapper
	// watchdog's "taskbook was sent again" metric made observable, and unlike
	// a bare run count it does not inflate on purpose runs (verifier/report),
	// which never claim an intent.
	Redispatch RedispatchStats `json:"redispatch"`
	// Emergence is what the model organised on its own: directions it created,
	// workers it spawned, messages it sent, handoffs and self-stops. It is an
	// observation aggregate — no path reads it back to gate or filter rows —
	// and it is deliberately absent from ForPrompt: a model that can read its
	// own autonomy score treats the score as a target. Only the API/console
	// board carries it.
	Emergence *EmergenceStats `json:"emergence,omitempty"`
	// PromptDigest is set only on the commander round prompt's copy of the view
	// (ForPrompt). It labels the lists above as bounded top-N digests and gives
	// the complete enumeration entries, so a model cannot read a 20-row list as
	// the whole ledger. Build leaves it nil: the API/console board keeps its
	// exact existing shape.
	PromptDigest *PromptDigest `json:"prompt_digest,omitempty"`
}

// RedispatchStats is how much re-dispatch a project has paid for. Directions
// counts intents that needed at least one re-dispatch; Total sums the extra
// attempts across them (Attempts-1, since the first attempt is not a resend).
type RedispatchStats struct {
	Total      int `json:"total"`
	Directions int `json:"directions"`
}

// PlaybookStats is how much reusable knowledge the project has accumulated.
type PlaybookStats struct {
	Archived   int `json:"archived,omitempty"`
	Total      int `json:"total"`
	Confirmed  int `json:"confirmed"`
	Candidates int `json:"candidates"`
	Retired    int `json:"retired,omitempty"`
}

// FrontierItem is one open direction plus the state of its premise.
type FrontierItem struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Status is pending / claimed / failed. A failed direction is still shown
	// (its last stop is why it died) but Ready is false: it needs an explicit
	// retry, not the auto-dispatcher.
	Status string `json:"status,omitempty"`
	// Ready is false when a from_fact is missing or rejected, or when the
	// direction has failed. Dispatch refuses those intents, so this is the
	// reason a queued direction is not moving.
	Ready   bool     `json:"ready"`
	Blocked []string `json:"blocked_on,omitempty"`
	// Prerequisites is how many from_fact_ids the direction names. NoPremise
	// marks a direction that was created without any recorded premise: legal for
	// a first authorized reconnaissance, but the commander should see it instead
	// of discovering later that the graph has no causal edge there.
	Prerequisites int  `json:"prerequisites"`
	NoPremise     bool `json:"no_premise,omitempty"`
	// PremiseChanged lists premises whose claim version differs from the
	// snapshot taken when the direction was created. It is a judgement input
	// ("re-read that fact before relying on it"), never an automatic block.
	PremiseChanged []string `json:"premise_changed,omitempty"`
	// Descendants is the number of open descendant directions. It makes a
	// parent direction's real weight visible without pulling the whole tree.
	Descendants int `json:"descendants,omitempty"`
	// LastStop and Declared summarise the previous attempt: what the harness
	// said (completed / canceled / error) and how many facts it declared. They
	// are what the evidence ladder keys on — not the attempt count.
	LastStop string `json:"last_stop,omitempty"`
	Declared int    `json:"last_declared,omitempty"`
}

// PortGroup is one port and the hosts that have a service fact on it. The
// members are the service titles, kept as the raw observation.
type PortGroup struct {
	Port    string   `json:"port"`
	Hosts   []string `json:"hosts"`
	Members []string `json:"members"`
}

// CoverageItem is one host the ledger has observed. Open/Running are derived
// from intent text mentioning the host, not from a target assignment: the
// program does not decide what a run is aimed at (D58/D59).
type CoverageItem struct {
	Host     string    `json:"host"`
	Services int       `json:"services,omitempty"`
	LastSeen time.Time `json:"last_seen"`
	// LastIntent is the intent that produced the most recent fact for this host.
	LastIntent string `json:"last_intent,omitempty"`
	// Open means a pending/claimed direction mentions it; Running means a live
	// run does. Neither is a lease.
	Open    bool `json:"open,omitempty"`
	Running bool `json:"running,omitempty"`
}

type Counts struct {
	Total   int `json:"total"`
	Pending int `json:"pending"`
	Held    int `json:"held,omitempty"`
	Claimed int `json:"claimed"`
	Running int `json:"running"`
	Waiting int `json:"waiting,omitempty"`
	Done    int `json:"done"`
	Failed  int `json:"failed"`
	Parked  int `json:"parked"`
}

type FactStats struct {
	Total        int `json:"total"`
	WithEvidence int `json:"with_evidence"`
	// WithRefs counts facts that cited something the ledger cannot open (a url,
	// an artifact, a file that is gone). They are not evidence, but the
	// distinction matters to the decider: "cited but unbacked" is a candidate
	// for one capture run, "no citation at all" usually is not (D44).
	WithRefs int `json:"with_refs,omitempty"`
	Verified int `json:"verified"`
	// Unverified / Rejected are part of the digest, not just decoration: a
	// verifier verdict (or a human reject) changes what the commander and the
	// report should say, and without a count in the digest the review wake was
	// skipped as "nothing changed" (2026-11 audit).
	Unverified int            `json:"unverified,omitempty"`
	Rejected   int            `json:"rejected,omitempty"`
	ByKind     map[string]int `json:"by_kind"`
	ByOrigin   map[string]int `json:"by_origin"`
}

type RunCounts struct {
	Total    int `json:"total"`
	Running  int `json:"running"`
	Done     int `json:"done"`
	Failed   int `json:"failed"`
	Canceled int `json:"canceled"`
}

// GoalStats keeps unmet goals visible: the decider's first question is "is the
// goal satisfied", and answering it needs the goal id it may name in a
// `complete`, not just a count.
type GoalStats struct {
	NeedsReview []GoalItem `json:"needs_review,omitempty"`
	Total       int        `json:"total"`
	Met         int        `json:"met"`
	Open        []GoalItem `json:"open"`
}

// GoalItem is one unmet goal: the id is what a complete action names, and the
// quantified target is the thing the model must compare against measurements.
type GoalItem struct {
	ChangedFacts []string `json:"changed_facts,omitempty"`
	ID           string   `json:"id"`
	Statement    string   `json:"statement"`
	Quantified   string   `json:"quantified_target,omitempty"`
}

// Item is the L1/L2 projection row: identity, label, gist. Never a full doc.
type Item struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Summary string `json:"summary,omitempty"`
	Status  string `json:"status,omitempty"`
	Kind    string `json:"kind,omitempty"`
	Origin  string `json:"origin,omitempty"`
	// SourceRef is the fact's stable external identity (an audit finding, or an
	// IOA event projected from a subagent record). The console uses the "ioa:"
	// prefix to mark facts whose session lives outside the ledger.
	SourceRef string `json:"source_ref,omitempty"`
	Evidence  int    `json:"evidence,omitempty"`
	// Refs is how many citations this fact made that have no bytes. It is not
	// folded into Evidence: the two answer different questions (D44).
	Refs      int       `json:"refs,omitempty"`
	Attempts  int       `json:"attempts,omitempty"`
	IntentID  string    `json:"intent_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	// Profile/Tier say which model binding the direction is on, so the
	// commander can see a strong direction next to a worker one before it
	// decides to raise, lower or compare them. A running direction reports the
	// binding frozen on its run (ResolvedProfile/Tier); a direction that has
	// not started reports the profile it asked for, and no tier — the tier
	// lives in the settings catalogue, not in the intent. Empty on old rows and
	// on directions without a binding (omitempty).
	Profile string `json:"profile,omitempty"`
	Tier    string `json:"tier,omitempty"`
	// Pulse is attached to running intents only: how long the run has been
	// quiet tells the decider whether it is thinking or stuck (D56).
	Pulse *RunPulse `json:"pulse,omitempty"`
}

// RunPulse is the live heartbeat of one run. IdleSeconds is measured from the
// last executor event (or the run start when there has been none yet).
type RunPulse struct {
	Run           string     `json:"run"`
	StartedAt     time.Time  `json:"started_at"`
	LastEventAt   *time.Time `json:"last_event_at,omitempty"`
	IdleSeconds   int        `json:"idle_seconds"`
	Events        int        `json:"events"`
	DeclaredFacts int        `json:"declared_facts,omitempty"`
	ToolFailures  int        `json:"tool_failures,omitempty"`
	// ContextTokens is the last model call's context size; ContextWindow is the
	// configured budget. Together they are the only honest live "progress" a run
	// has: the harness exposes no completion percentage.
	ContextTokens int `json:"context_tokens,omitempty"`
	ContextWindow int `json:"context_window,omitempty"`
	// State is the last harness status payload (compaction / context budget);
	// empty until the harness reports one.
	State string `json:"state,omitempty"`
	// LastKind / LastTool / LastText are the most recent meaningful event: the
	// one-line glance at what the run is doing. Without them the decider only
	// knows that a run moved, not whether it is exploiting or stuck in a
	// retry loop; the full tail is the run_events tool (D57).
	LastKind string `json:"last_kind,omitempty"`
	LastTool string `json:"last_tool,omitempty"`
	LastText string `json:"last_text,omitempty"`
}

func pulseOf(r model.Run) *RunPulse {
	last := r.StartedAt
	if r.LastEventAt != nil {
		last = *r.LastEventAt
	}
	idle := int(time.Since(last).Seconds())
	if idle < 0 {
		idle = 0
	}
	return &RunPulse{
		Run:           r.ID,
		StartedAt:     r.StartedAt,
		LastEventAt:   r.LastEventAt,
		IdleSeconds:   idle,
		Events:        r.Events,
		DeclaredFacts: r.DeclaredFacts,
		ToolFailures:  r.ToolFailures,
		ContextTokens: r.ContextTokens,
		ContextWindow: r.ContextWindow,
		State:         r.LastState,
		LastKind:      r.LastEventKind,
		LastTool:      r.LastEventTool,
		LastText:      r.LastEventText,
	}
}

// HasIdleRun reports whether a running intent has been quiet for at least d.
// It is why a stalled decider still wakes up: silence from a live run is a
// signal, unlike an unchanged board with nothing running.
func (v View) HasIdleRun(d time.Duration) bool {
	for _, it := range v.Running {
		if it.Pulse != nil && time.Duration(it.Pulse.IdleSeconds)*time.Second >= d {
			return true
		}
	}
	return false
}

// PromptSummaryRunes is the one-line budget for a free-text summary in the
// commander's round prompt. The ledger's summaries are model-written and can be
// pages long; the prompt carries the gist, and the full text stays behind the
// fact/intent API (docs/17 §3.1: title → summary → doc, the doc never rides a
// prompt). It mirrors sapper/nvwabase's split, where the commander sees a compact
// board and reads details on demand.
const PromptSummaryRunes = 240

// ForPrompt returns the summary-layer copy of the view for the commander's round
// prompt: same shape, counts and ids, but the prose fields are clipped and the
// emergence aggregate is dropped. The API and the console keep serving the
// untouched View, so what the operator reads is still complete; slots are copied
// per item so the input view is not mutated.
func ForPrompt(v View) View {
	out := v
	out.Open = promptItems(v.Open)
	out.Held = promptItems(v.Held)
	out.Waiting = promptItems(v.Waiting)
	out.Running = promptItems(v.Running)
	out.RecentFacts = promptItems(v.RecentFacts)
	// No scoreboard for the player: a visible count of directions opened or
	// workers spawned becomes a thing to optimise, not a thing to observe. The
	// nil pointer disappears from the JSON entirely, so the prompt does not
	// carry zeros that could be read as "the ledger shows none".
	out.Emergence = nil
	// The lists above are bounded top-N; label them and give the complete
	// enumeration entries. Only the prompt copy carries this, so the API/console
	// board shape is unchanged.
	out.PromptDigest = promptDigest(out)
	return out
}

func promptItems(items []Item) []Item {
	if len(items) == 0 {
		return items
	}
	out := make([]Item, len(items))
	for i, it := range items {
		it.Summary = promptClip(it.Summary, PromptSummaryRunes)
		out[i] = it
	}
	return out
}

func promptClip(s string, n int) string {
	r := []rune(s)
	if n <= 0 || len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func Build(s *store.Store, limit int) View {
	return BuildFromDB(s.Snapshot(), limit)
}

// BuildFromDB builds the same view from a ledger clone a caller already froze.
// A report's snapshot, source digest and validation must all describe one input
// moment; a second Snapshot() would be "the same view, one transaction later" —
// exactly the race the one-freeze path exists to close.
//
// A clone from Store.PreparePurposeInput carries the ledger's content-revision
// watermark on Memory rather than DB.ContentHash (Snapshot lifts it for its
// callers), so lift it the same way here. Without this a report would store a
// count-based fingerprint while the report list compares the content watermark,
// and every fresh report would read as stale the moment it was listed.
func BuildFromDB(db store.DB, limit int) View {
	if limit <= 0 {
		limit = 20
	}
	if db.ContentHash == "" && db.Memory != nil {
		db.ContentHash = db.Memory.ContentHash
	}
	factsByID := make(map[string]model.Fact, len(db.Facts))
	for _, f := range db.Facts {
		factsByID[f.ID] = f
	}
	runsByID := make(map[string]model.Run, len(db.Runs))
	for _, r := range db.Runs {
		runsByID[r.ID] = r
	}
	now := time.Now()
	emergence := emergenceStats(db, now)
	v := View{
		ContentHash: db.ContentHash,
		GeneratedAt: now,
		Revision:    db.ReadRevision,
		Facts:       FactStats{ByKind: map[string]int{}, ByOrigin: map[string]int{}},
		Goals:       GoalStats{Open: []GoalItem{}},
		Open:        []Item{},
		Held:        []Item{},
		Waiting:     []Item{},
		Running:     []Item{},
		RecentFacts: []Item{},
		Done:        []string{},
		Frontier:    []FrontierItem{},
		Ports:       []PortGroup{},
		Coverage:    []CoverageItem{},
		Emergence:   &emergence,
	}

	for _, in := range db.Intents {
		v.Intents.Total++
		if in.Attempts > 1 {
			v.Redispatch.Total += in.Attempts - 1
			v.Redispatch.Directions++
		}
		switch in.Status {
		case model.StatusPending:
			v.Intents.Pending++
			if in.Hold {
				v.Intents.Held++
			}
		case model.StatusClaimed:
			v.Intents.Claimed++
		case model.StatusRunning:
			v.Intents.Running++
		case model.StatusWaiting:
			v.Intents.Waiting++
		case model.StatusDone:
			v.Intents.Done++
		case model.StatusFailed:
			v.Intents.Failed++
		case model.StatusParked:
			v.Intents.Parked++
		}
	}

	// Newest first for bounded lists.
	limitDone := limit / 2
	if limitDone < 5 {
		limitDone = 5
	}
	for i := len(db.Intents) - 1; i >= 0 && (len(v.Open) < limit || len(v.Held) < limit || len(v.Waiting) < limit || len(v.Running) < limit || len(v.Done) < limitDone); i-- {
		in := db.Intents[i]
		it := Item{
			ID:        in.ID,
			Title:     in.Title,
			Summary:   in.Description,
			Status:    in.Status,
			Attempts:  in.Attempts,
			IntentID:  in.ID,
			CreatedAt: in.CreatedAt,
		}
		if in.Wait != nil && in.Wait.Operator != nil {
			it.Summary = "等待操作员：" + in.Wait.Operator.Request
			if in.Wait.Operator.CompletedAt != nil {
				it.Summary = "操作员已完成，等待继续；尚未验证现场"
				if in.Wait.Operator.Resolution == model.OperatorUnavailable {
					it.Summary = "人工操作无法完成，等待继续评估；登录前提未满足"
				}
			}
		}
		if in.Status == model.StatusRunning {
			if r, ok := runsByID[in.RunID]; ok {
				it.Pulse = pulseOf(r)
				v.Supervision = append(v.Supervision, *it.Pulse)
				// The run row is the frozen binding: the intent's requested
				// profile may be empty (project/instance default) or may have
				// been overridden by a fallback.
				it.Profile = r.ResolvedProfile
				it.Tier = r.Tier
			}
		}
		if it.Profile == "" {
			// Not started yet (or the run row is gone): show what the
			// direction asked for. Its tier is not in the ledger.
			it.Profile = in.Profile
		}
		switch in.Status {
		case model.StatusPending:
			if in.Hold {
				if len(v.Held) < limit {
					v.Held = append(v.Held, it)
				}
			} else if len(v.Open) < limit {
				v.Open = append(v.Open, it)
			}
		case model.StatusClaimed:
			if len(v.Open) < limit {
				v.Open = append(v.Open, it)
			}
		case model.StatusRunning:
			if len(v.Running) < limit {
				v.Running = append(v.Running, it)
			}
		case model.StatusWaiting:
			if len(v.Waiting) < limit {
				v.Waiting = append(v.Waiting, it)
			}
		case model.StatusDone:
			if len(v.Done) < limitDone {
				v.Done = append(v.Done, in.Title)
			}
		}
	}

	// The frontier: open directions and whether their premise still stands. A
	// from_fact that is gone or rejected makes the direction dead work, not
	// queued work — dispatch refuses it, and the decider should see why.
	childrenByID := make(map[string][]string, len(db.Intents))
	intentStatus := make(map[string]string, len(db.Intents))
	for _, in := range db.Intents {
		intentStatus[in.ID] = in.Status
		if in.ParentID != "" {
			childrenByID[in.ParentID] = append(childrenByID[in.ParentID], in.ID)
		}
	}
	for i := len(db.Intents) - 1; i >= 0 && len(v.Frontier) < limit; i-- {
		in := db.Intents[i]
		switch in.Status {
		case model.StatusPending, model.StatusClaimed, model.StatusFailed:
		default:
			continue
		}
		// Ready mirrors the real dispatch gate: pending/claimed and not held.
		// A canceled direction becomes pending+Hold, and claiming it would be a
		// silent resurrection (D19), so advertising it as ready made the decider
		// wait for work it had itself stopped.
		fi := FrontierItem{
			ID: in.ID, Title: in.Title, Status: in.Status,
			Ready:          in.Status != model.StatusFailed && !in.Hold,
			Prerequisites:  len(in.FromFactIDs),
			NoPremise:      len(in.FromFactIDs) == 0,
			PremiseChanged: store.PremiseClaimsChanged(in, factsByID),
			Descendants:    openDescendants(in.ID, childrenByID, intentStatus),
		}
		for _, id := range in.FromFactIDs {
			f, ok := factsByID[id]
			switch {
			case !ok:
				fi.Blocked = append(fi.Blocked, id+"（不存在）")
			case f.Status == "rejected":
				fi.Blocked = append(fi.Blocked, id+"（已否定）")
			}
		}
		if len(fi.Blocked) > 0 {
			fi.Ready = false
		}
		if r, ok := runsByID[in.RunID]; ok {
			fi.LastStop = r.StopReason
			fi.Declared = r.DeclaredFacts
		}
		v.Frontier = append(v.Frontier, fi)
	}

	// Declared facts lead regardless of age: a heuristic row is a trace, a
	// declared row is evidence of work (docs/06, D18).
	type ranked struct {
		it   Item
		rank int
	}
	all := make([]ranked, 0, len(db.Facts))
	for _, f := range db.Facts {
		v.Facts.Total++
		v.Facts.ByKind[f.Kind]++
		v.Facts.ByOrigin[originOf(f.Origin)]++
		if len(f.EvidenceIDs) > 0 {
			v.Facts.WithEvidence++
		}
		if len(f.Refs) > 0 {
			v.Facts.WithRefs++
		}
		if f.Status == "verified" {
			v.Facts.Verified++
		} else if f.Status == "rejected" {
			v.Facts.Rejected++
		} else {
			v.Facts.Unverified++
		}
		all = append(all, ranked{
			rank: originRank(f.Origin),
			it: Item{
				ID:        f.ID,
				Title:     f.Title,
				Summary:   f.Summary,
				Status:    f.Status,
				Kind:      f.Kind,
				Origin:    originOf(f.Origin),
				SourceRef: f.SourceRef,
				Evidence:  len(f.EvidenceIDs),
				Refs:      len(f.Refs),
				IntentID:  f.IntentID,
				CreatedAt: f.CreatedAt,
			},
		})
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].rank != all[j].rank {
			return all[i].rank < all[j].rank
		}
		return all[i].it.CreatedAt.After(all[j].it.CreatedAt)
	})
	for i := 0; i < len(all) && len(v.RecentFacts) < limit; i++ {
		v.RecentFacts = append(v.RecentFacts, all[i].it)
	}

	v.Evidence = len(db.Evidence)

	for _, g := range db.Goals {
		v.Goals.Total++
		if g.Met {
			v.Goals.Met++
			if g.ReceiptStale && len(v.Goals.NeedsReview) < limit {
				v.Goals.NeedsReview = append(v.Goals.NeedsReview, GoalItem{ID: g.ID, Statement: g.Statement, Quantified: g.QuantifiedTarget, ChangedFacts: append([]string(nil), g.ReceiptChanged...)})
			}
			continue
		}
		if len(v.Goals.Open) < limit {
			v.Goals.Open = append(v.Goals.Open, GoalItem{
				ID:         g.ID,
				Statement:  g.Statement,
				Quantified: g.QuantifiedTarget,
			})
		}
	}
	for _, p := range db.Playbooks {
		v.Playbooks.Total++
		if p.Archived {
			v.Playbooks.Archived++
			continue
		}
		switch p.Status {
		case model.PlaybookConfirmed:
			v.Playbooks.Confirmed++
		case model.PlaybookCandidate:
			v.Playbooks.Candidates++
		case model.PlaybookRetired:
			v.Playbooks.Retired++
		}
	}
	for _, r := range db.Runs {
		v.Runs.Total++
		switch r.Status {
		case model.RunRunning:
			v.Runs.Running++
		case model.RunDone:
			v.Runs.Done++
		case model.RunCanceled:
			v.Runs.Canceled++
		default:
			v.Runs.Failed++
		}
	}

	// Port aggregate: the service facts the ledger holds, grouped by port. This
	// is an observation the decider can start from, not a conclusion it can
	// reuse: same port ≠ same product, and one product may span ports (D54).
	// Any judgment about what belongs together is made from facts, with the
	// decider's read tools, never here.
	type portGroup struct {
		hosts   map[string]bool
		members []string
	}
	byPort := make(map[string]*portGroup, 8)
	for _, f := range db.Facts {
		if f.Kind != "service" {
			continue
		}
		host, port, ok := serviceHostPort(f.Title)
		if !ok {
			continue
		}
		g := byPort[port]
		if g == nil {
			g = &portGroup{hosts: map[string]bool{}}
			byPort[port] = g
		}
		g.hosts[host] = true
		if len(g.members) < 12 {
			g.members = append(g.members, f.Title)
		}
	}
	for port, g := range byPort {
		hosts := make([]string, 0, len(g.hosts))
		for h := range g.hosts {
			hosts = append(hosts, h)
		}
		sort.Strings(hosts)
		sort.Strings(g.members)
		v.Ports = append(v.Ports, PortGroup{Port: port, Hosts: hosts, Members: g.members})
	}
	sort.Slice(v.Ports, func(i, j int) bool { return v.Ports[i].Port < v.Ports[j].Port })
	if len(v.Ports) > 12 {
		v.Ports = v.Ports[:12]
	}

	// Known-host coverage. It is derived from observations (host/service facts)
	// and from intent text, not from a scope parser: the program does not decide
	// what is in scope (D59), it only highlights hosts nobody is working on.
	type covStat struct {
		services   int
		last       time.Time
		lastIntent string
		open       bool
		running    bool
	}
	byHost := make(map[string]*covStat, 16)
	addHost := func(host string, at time.Time, intent string) {
		host = strings.TrimSpace(host)
		if host == "" {
			return
		}
		c := byHost[host]
		if c == nil {
			c = &covStat{}
			byHost[host] = c
		}
		if at.After(c.last) {
			c.last = at
			c.lastIntent = intent
		}
	}
	for _, f := range db.Facts {
		switch f.Kind {
		case "service":
			if host, _, ok := serviceHostPort(f.Title); ok {
				c := byHost[host]
				if c == nil {
					c = &covStat{}
					byHost[host] = c
				}
				c.services++
				addHost(host, f.CreatedAt, f.IntentID)
			}
		case "host":
			addHost(f.Title, f.CreatedAt, f.IntentID)
		}
	}
	for _, in := range db.Intents {
		running := in.Status == model.StatusRunning
		open := in.Status == model.StatusPending || in.Status == model.StatusClaimed
		if !running && !open {
			continue
		}
		text := in.Title + "\n" + in.Description + "\n" + in.Doc
		for host := range byHost {
			if !hostMention(text, host) {
				continue
			}
			c := byHost[host]
			if running {
				c.running = true
			} else {
				c.open = true
			}
		}
	}
	for host, c := range byHost {
		v.Coverage = append(v.Coverage, CoverageItem{
			Host:       host,
			Services:   c.services,
			LastSeen:   c.last,
			LastIntent: c.lastIntent,
			Open:       c.open,
			Running:    c.running,
		})
	}
	sort.SliceStable(v.Coverage, func(i, j int) bool {
		if v.Coverage[i].Running != v.Coverage[j].Running {
			return !v.Coverage[i].Running
		}
		if v.Coverage[i].Open != v.Coverage[j].Open {
			return !v.Coverage[i].Open
		}
		return v.Coverage[i].LastSeen.Before(v.Coverage[j].LastSeen)
	})
	if len(v.Coverage) > 30 {
		v.Coverage = v.Coverage[:30]
	}
	return v
}

// hostMention matches an address on a label boundary so 10.0.0.5 does not
// match 10.0.0.50. It is a text heuristic, not a target assignment.
func hostMention(text, host string) bool {
	text, host = strings.ToLower(text), strings.ToLower(host)
	if host == "" {
		return false
	}
	for from := 0; from+len(host) <= len(text); {
		i := strings.Index(text[from:], host)
		if i < 0 {
			return false
		}
		i += from
		end := i + len(host)
		beforeOK := i == 0 || !hostByte(text[i-1])
		afterOK := end == len(text) || !hostByte(text[end])
		if beforeOK && afterOK {
			return true
		}
		from = i + 1
	}
	return false
}

func hostByte(b byte) bool {
	return b == '.' || b == '-' || b == '_' ||
		(b >= '0' && b <= '9') || (b >= 'a' && b <= 'z')
}

// serviceHostPort splits "10.0.0.5:8080" (or "[::1]:22", or a URL someone put
// in a service title) into host and port.
func serviceHostPort(title string) (string, string, bool) {
	t := strings.TrimSpace(title)
	if i := strings.Index(t, "://"); i >= 0 {
		t = t[i+3:]
	}
	if i := strings.IndexAny(t, "/?#"); i >= 0 {
		t = t[:i]
	}
	if h, p, err := net.SplitHostPort(t); err == nil && p != "" {
		return h, p, true
	}
	if i := strings.LastIndex(t, ":"); i > 0 && i < len(t)-1 {
		return t[:i], t[i+1:], true
	}
	return "", "", false
}

func originOf(o string) string {
	switch o {
	case model.OriginDeclared, model.OriginDerived, model.OriginHeuristic:
		return o
	default:
		return model.OriginHeuristic
	}
}

func originRank(o string) int {
	switch originOf(o) {
	case model.OriginDeclared:
		return 0
	case model.OriginDerived:
		return 1
	default:
		return 2
	}
}

// openDescendants counts descendant directions that are not a finished outcome
// (done/parked). Failed descendants stay counted: a retryable failure is still
// open work for the parent's chain.
func openDescendants(id string, children map[string][]string, status map[string]string) int {
	count := 0
	var walk func(string)
	walk = func(parent string) {
		for _, child := range children[parent] {
			switch status[child] {
			case model.StatusDone, model.StatusParked:
				continue
			}
			count++
			walk(child)
		}
	}
	walk(id)
	return count
}
