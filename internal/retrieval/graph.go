package retrieval

import (
	"fmt"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
	"sort"
	"strings"
	"time"
)

type GraphQuery struct {
	EdgeLimit     int      `json:"-"`
	BoundaryLimit int      `json:"-"`
	MaxBytes      int      `json:"-"`
	Roots         []string `json:"roots,omitempty"`
	Direction     string   `json:"direction,omitempty"`
	Depth         int      `json:"depth"`
	// Order and Projection are optional additions to the shared read contract
	// (docs/33 §3). Empty keeps the historical id-lexicographic, full-body
	// behaviour every existing caller (the agent included) already relies on.
	Order      string `json:"order,omitempty"`
	Projection string `json:"projection,omitempty"`
	Cursor     string `json:"-"`
	Limit      int    `json:"-"`
}
type Edge = store.Relation
type Boundary struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

// FactMeta is the count/verdict projection of one fact returned by a graph
// page. It always accompanies the fact so a client that dropped the original
// lists (projection=graph) can still show "N evidence / M refs" and "复核的是
// 旧断言"; the详情 panel re-fetches the bytes by id instead of the graph
// inventing them.
type FactMeta struct {
	ID                   string `json:"id"`
	NeedsReview          bool   `json:"needs_review"`
	ReviewedClaimVersion uint64 `json:"reviewed_claim_version"`
	EvidenceCount        int    `json:"evidence_count"`
	RefCount             int    `json:"ref_count"`
}

type GraphPage struct {
	EdgesTotal    int            `json:"edges_total"`
	BoundaryTotal int            `json:"boundary_total"`
	IntentsTotal  int            `json:"intents_total"`
	FactsTotal    int            `json:"facts_total"`
	GoalsTotal    int            `json:"goals_total"`
	Details       []NodeDetail   `json:"details,omitempty"`
	FactMeta      []FactMeta     `json:"fact_meta"`
	Facts         []model.Fact   `json:"facts"`
	Intents       []model.Intent `json:"intents"`
	Goals         []model.Goal   `json:"goals"`
	Edges         []Edge         `json:"edges"`
	Boundary      []Boundary     `json:"boundary"`
	PageInfo
}

// GraphOrderStructural / GraphOrderRecent are the accepted non-default orders.
// GraphProjectionGraph is the fact projection that removes body fields before
// the byte budget is measured (docs/33 §3.1).
const (
	GraphOrderStructural = "structural"
	GraphOrderRecent     = "recent"
	GraphProjectionGraph = "graph"
)

// Graph keeps all explicit premise edges. Pagination exposes boundary refs
// rather than turning a not-yet-loaded predecessor into a project-root edge.
func Graph(s *store.Store, q GraphQuery) (GraphPage, error) {
	if q.Direction != "" && q.Direction != "both" && q.Direction != "ancestors" && q.Direction != "descendants" {
		return GraphPage{}, fmt.Errorf("invalid graph direction")
	}
	if q.Depth < 0 {
		return GraphPage{}, fmt.Errorf("invalid graph depth")
	}
	switch q.Order {
	case "", GraphOrderStructural, GraphOrderRecent:
	default:
		return GraphPage{}, fmt.Errorf("invalid graph order")
	}
	switch q.Projection {
	case "", "full", GraphProjectionGraph:
	default:
		return GraphPage{}, fmt.Errorf("invalid graph projection")
	}
	key := queryKey(struct {
		Type  string
		Query GraphQuery
	}{"graph", q})
	c := cursor{Version: s.Watermark(), Query: key}
	if q.Cursor != "" {
		var err error
		c, err = decode(q.Cursor, key)
		if err != nil {
			return GraphPage{}, err
		}
	}
	roots := []string{}
	for _, id := range q.Roots {
		if !strings.Contains(id, ":") {
			if strings.HasPrefix(id, "in_") {
				id = "intent:" + id
			} else {
				id = "fact:" + id
			}
		}
		roots = append(roots, id)
	}
	db, used, e := s.ReadNeighborhood(c.Version, roots, q.Direction, q.Depth)
	if e != nil {
		return GraphPage{}, e
	}
	if !used {
		db, e = s.ReadAt(c.Version)
		if e != nil {
			return GraphPage{}, e
		}
	}
	nodes := map[string]any{}
	edges := []Edge{}
	for _, f := range db.Facts {
		nodes["fact:"+f.ID] = f
		if f.IntentID != "" {
			edges = append(edges, Edge{Source: "intent:" + f.IntentID, Target: "fact:" + f.ID, Kind: "produces"})
		}
	}
	for _, in := range db.Intents {
		nodes["intent:"+in.ID] = in
		for _, id := range in.FromFactIDs {
			edges = append(edges, Edge{Source: "fact:" + id, Target: "intent:" + in.ID, Kind: "premise"})
		}
		for _, id := range in.ContextFactIDs {
			edges = append(edges, Edge{Source: "fact:" + id, Target: "intent:" + in.ID, Kind: "reference"})
		}
		// A pin is recorded focus, not a premise and not a reference: it is its
		// own ledger field, so it gets its own edge. The two lists never consume
		// each other (docs/33 §3.2).
		for _, id := range in.PinnedFactIDs {
			edges = append(edges, Edge{Source: "fact:" + id, Target: "intent:" + in.ID, Kind: "pin"})
		}
		if in.ParentID != "" {
			edges = append(edges, Edge{Source: "intent:" + in.ParentID, Target: "intent:" + in.ID, Kind: "delegates"})
		}
	}
	for _, g := range db.Goals {
		nodes["goal:"+g.ID] = g
		for _, id := range g.From {
			edges = append(edges, Edge{Source: "fact:" + id, Target: "goal:" + g.ID, Kind: "supports"})
		}
	}
	if len(db.ReadEdges) > 0 {
		known := map[string]bool{}
		for _, edge := range edges {
			known[edge.Source+"\x00"+edge.Target+"\x00"+edge.Kind] = true
		}
		for _, edge := range db.ReadEdges {
			key := edge.Source + "\x00" + edge.Target + "\x00" + edge.Kind
			if !known[key] {
				edges = append(edges, edge)
				known[key] = true
			}
		}
	}
	adj := map[string][]string{}
	for _, edge := range edges {
		if q.Direction != "ancestors" {
			adj[edge.Source] = append(adj[edge.Source], edge.Target)
		}
		if q.Direction != "descendants" {
			adj[edge.Target] = append(adj[edge.Target], edge.Source)
		}
	}
	selected := map[string]bool{}
	if len(q.Roots) == 0 {
		for id := range nodes {
			selected[id] = true
		}
	} else {
		type step struct {
			id    string
			depth int
		}
		queue := []step{}
		for _, id := range q.Roots {
			if !strings.Contains(id, ":") {
				if strings.HasPrefix(id, "in_") {
					id = "intent:" + id
				} else {
					id = "fact:" + id
				}
			}
			queue = append(queue, step{id, 0})
			selected[id] = true
		}
		for head := 0; head < len(queue); head++ {
			v := queue[head]
			if q.Depth > 0 && v.depth >= q.Depth {
				continue
			}
			for _, id := range adj[v.id] {
				if !selected[id] {
					selected[id] = true
					queue = append(queue, step{id, v.depth + 1})
				}
			}
		}
	}
	ids := []string{}
	for id := range selected {
		if _, ok := nodes[id]; ok {
			ids = append(ids, id)
		}
	}
	// Depth/fact_meta use the same frozen snapshot the page is cut from. The
	// neighbourhood read (root-scoped queries) does not carry the review table,
	// so fall back to the live review list there; a full ReadAt snapshot already
	// has the snapshot-era reviews.
	latest := latestReviewByFact(db.Reviews)
	if db.ReadExistence != nil {
		latest = latestReviewByFact(s.Reviews(""))
	}
	factMeta := map[string]FactMeta{}
	for _, id := range ids {
		if f, ok := nodes[id].(model.Fact); ok {
			factMeta[id] = factMetaOf(f, latest)
		}
	}
	ids = orderGraphIDs(q, db, ids)
	// Projection must happen before paginateGraph measures bytes, otherwise the
	// budget would be spent on body fields the caller explicitly dropped.
	if q.Projection == GraphProjectionGraph {
		for id, node := range nodes {
			if f, ok := node.(model.Fact); ok {
				nodes[id] = graphProjectedFact(f)
			}
		}
	}
	return paginateGraph(db, nodes, edges, selected, ids, c, q, factMeta)
}

// graphProjectedFact removes the body fields the graph page never draws. The
// omitted content is re-fetched by id from /api/facts/<id>; it must not become
// a `details` entry, because "not in this projection" is a contract, not a
// truncation (docs/33 §3.1).
func graphProjectedFact(f model.Fact) model.Fact {
	f.Doc = ""
	f.EvidenceIDs = nil
	f.Refs = nil
	f.Tags = nil
	return f
}

// orderGraphIDs orders the selected ids for the requested page order. Default
// (empty) is the historical lexicographic order: every existing caller keeps
// exactly the bytes and page boundaries it saw before.
//
// structural pages by chain depth, not by node type: depth asc, then same-depth
// rank (fact=0, intent=1, goal=2), then created_at desc (undated last), then id.
// A premise fact sits one depth above its intent and every produced fact one
// depth below it, so any page prefix is a legal subgraph: an intent never
// appears without the premises it cites, and buildGraph does not have to draw
// them as boundary "expand" nodes on the first page.
func orderGraphIDs(q GraphQuery, db store.DB, ids []string) []string {
	switch q.Order {
	case GraphOrderStructural:
		depths := deriveGraphDepths(db)
		created, rank := graphNodeTimes(db)
		sort.Slice(ids, func(i, j int) bool {
			a, b := ids[i], ids[j]
			if da, dbDepth := depths.of(a), depths.of(b); da != dbDepth {
				return da < dbDepth
			}
			if rank[a] != rank[b] {
				return rank[a] < rank[b]
			}
			ca, cb := created[a], created[b]
			az, bz := ca.IsZero(), cb.IsZero()
			if az != bz {
				return bz // undated rows last
			}
			if !ca.Equal(cb) {
				return ca.After(cb)
			}
			return a < b
		})
	case GraphOrderRecent:
		created, _ := graphNodeTimes(db)
		sort.Slice(ids, func(i, j int) bool {
			a, b := ids[i], ids[j]
			ca, cb := created[a], created[b]
			az, bz := ca.IsZero(), cb.IsZero()
			if az != bz {
				return bz // undated rows last
			}
			if !ca.Equal(cb) {
				return ca.After(cb)
			}
			return a < b
		})
	default:
		sort.Strings(ids)
	}
	return ids
}

// graphNodeTimes maps prefixed graph ids to their created_at and same-depth
// rank used by the structural page order (fact=0, intent=1, goal=2).
func graphNodeTimes(db store.DB) (map[string]time.Time, map[string]int) {
	created := make(map[string]time.Time, len(db.Facts)+len(db.Intents)+len(db.Goals))
	rank := make(map[string]int, len(created))
	for _, f := range db.Facts {
		id := "fact:" + f.ID
		created[id], rank[id] = f.CreatedAt, 0
	}
	for _, in := range db.Intents {
		id := "intent:" + in.ID
		created[id], rank[id] = in.CreatedAt, 1
	}
	for _, g := range db.Goals {
		id := "goal:" + g.ID
		created[id], rank[id] = g.CreatedAt, 2
	}
	return created, rank
}

func graphNodeID(id string) string {
	if i := strings.IndexByte(id, ':'); i >= 0 {
		return id[i+1:]
	}
	return id
}

// graphDepths is the ledger-derived chain depth of every intent and fact,
// recomputed from the same snapshot the page is cut from.
type graphDepths struct {
	step map[string]int
	fact map[string]int
	// goal is model.ts's goalDepth: one below the deepest produced fact (1 when
	// the ledger has no facts).
	goal int
}

// of returns the structural paging depth of one prefixed graph id: stepDepth
// for intents, factDepth for facts (1 when the producer is missing), and the
// goal depth for goals.
func (d graphDepths) of(id string) int {
	switch {
	case strings.HasPrefix(id, "intent:"):
		if depth := d.step[graphNodeID(id)]; depth > 0 {
			return depth
		}
		return 1
	case strings.HasPrefix(id, "goal:"):
		if d.goal > 0 {
			return d.goal
		}
		return 1
	default:
		if depth := d.fact[graphNodeID(id)]; depth > 0 {
			return depth
		}
		return 1
	}
}

// deriveGraphDepths mirrors web/src/graph/model.ts: intents are visited in
// created_at/id order, stepDepth = max(premise fact depth, parent stepDepth)+1
// (floored at 1), and a fact produced by a step sits at stepDepth+1. A fact with
// no producer has depth 1. Goals sit at goalDepth = maxFactDepth+1 (or 1 when
// the ledger has no facts).
func deriveGraphDepths(db store.DB) graphDepths {
	factsOut := map[string][]string{}
	for _, f := range db.Facts {
		if f.IntentID != "" {
			factsOut[f.IntentID] = append(factsOut[f.IntentID], f.ID)
		}
	}
	ordered := append([]model.Intent(nil), db.Intents...)
	sort.Slice(ordered, func(i, j int) bool {
		if !ordered[i].CreatedAt.Equal(ordered[j].CreatedAt) {
			return ordered[i].CreatedAt.Before(ordered[j].CreatedAt)
		}
		return ordered[i].ID < ordered[j].ID
	})
	out := graphDepths{step: map[string]int{}, fact: map[string]int{}}
	for _, in := range ordered {
		depth := 1
		for _, id := range in.FromFactIDs {
			src := out.fact[id]
			if src == 0 {
				src = 1
			}
			if src+1 > depth {
				depth = src + 1
			}
		}
		if in.ParentID != "" {
			if parent, ok := out.step[in.ParentID]; ok && parent+1 > depth {
				depth = parent + 1
			}
		}
		out.step[in.ID] = depth
		for _, id := range factsOut[in.ID] {
			if _, ok := out.fact[id]; !ok {
				out.fact[id] = depth + 1
			}
		}
	}
	out.goal = 1
	if len(db.Facts) > 0 {
		maxFactDepth := 0
		for _, depth := range out.fact {
			if depth > maxFactDepth {
				maxFactDepth = depth
			}
		}
		out.goal = maxFactDepth + 1
	}
	return out
}

type Impact struct {
	IntentsTotal int      `json:"intents_total"`
	GoalsTotal   int      `json:"goals_total"`
	Query        string   `json:"query,omitempty"`
	Fact         string   `json:"fact"`
	Intents      []string `json:"intents"`
	Goals        []string `json:"goals"`
}

func Impacts(db store.DB, ids []string) []Impact {
	dependents := map[string][]string{}
	produced := map[string][]string{}
	goals := map[string][]string{}
	for _, in := range db.Intents {
		for _, id := range in.FromFactIDs {
			dependents[id] = append(dependents[id], in.ID)
		}
	}
	for _, f := range db.Facts {
		produced[f.IntentID] = append(produced[f.IntentID], f.ID)
	}
	for _, g := range db.Goals {
		for _, id := range g.From {
			goals[id] = append(goals[id], g.ID)
		}
	}
	out := []Impact{}
	for _, id := range ids {
		v := Impact{Fact: id, Intents: []string{}, Goals: []string{}}
		queue := []string{id}
		seenFacts := map[string]bool{id: true}
		seenIntents := map[string]bool{}
		seenGoals := map[string]bool{}
		for head := 0; head < len(queue); head++ {
			for _, g := range goals[queue[head]] {
				if !seenGoals[g] {
					v.Goals = append(v.Goals, g)
					seenGoals[g] = true
				}
			}
			for _, in := range dependents[queue[head]] {
				if seenIntents[in] {
					continue
				}
				seenIntents[in] = true
				v.Intents = append(v.Intents, in)
				for _, f := range produced[in] {
					if !seenFacts[f] {
						seenFacts[f] = true
						queue = append(queue, f)
					}
				}
			}
		}
		out = append(out, v)
	}
	return out
}
