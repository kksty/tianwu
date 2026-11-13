package retrieval

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

type NodeDetail struct {
	ID      string   `json:"id"`
	Query   string   `json:"query"`
	Omitted []string `json:"omitted"`
}

func paginateGraph(db store.DB, nodes map[string]any, edges []Edge, selected map[string]bool, ids []string, c cursor, q GraphQuery, factMeta map[string]FactMeta) (GraphPage, error) {
	// All three streams have stable independent offsets in the same snapshot.
	// Historical reconstruction contains the full project. Keep only edges
	// incident to this query's selected nodes, including boundary references.
	relevant := make([]Edge, 0, len(edges))
	for _, edge := range edges {
		if selected[edge.Source] || selected[edge.Target] {
			relevant = append(relevant, edge)
		}
	}
	edges = relevant
	sort.Slice(edges, func(i, j int) bool {
		a, b := edges[i], edges[j]
		return a.Source+"\x00"+a.Target+"\x00"+a.Kind < b.Source+"\x00"+b.Target+"\x00"+b.Kind
	})
	refs := map[string]bool{}
	for _, e := range edges {
		refs[e.Source] = true
		refs[e.Target] = true
	}
	for id := range selected {
		refs[id] = true
	}
	boundaries := []Boundary{}
	for id := range refs {
		state := "not_loaded"
		if _, ok := nodes[id]; !ok {
			state = "missing"
		}
		if known, ok := db.ReadExistence[id]; ok {
			state = known
		}
		if f, ok := nodes[id].(model.Fact); ok && f.Status == "rejected" {
			state = "rejected"
		}
		boundaries = append(boundaries, Boundary{id, state})
	}
	sort.Slice(boundaries, func(i, j int) bool { return boundaries[i].ID < boundaries[j].ID })
	intentsTotal, factsTotal, goalsTotal := 0, 0, 0
	for _, id := range ids {
		switch {
		case strings.HasPrefix(id, "intent:"):
			intentsTotal++
		case strings.HasPrefix(id, "fact:"):
			factsTotal++
		case strings.HasPrefix(id, "goal:"):
			goalsTotal++
		}
	}
	out := GraphPage{
		Facts: []model.Fact{}, Intents: []model.Intent{}, Goals: []model.Goal{}, Edges: []Edge{}, Boundary: []Boundary{},
		FactMeta: []FactMeta{}, IntentsTotal: intentsTotal, FactsTotal: factsTotal, GoalsTotal: goalsTotal,
		PageInfo: PageInfo{Total: len(ids), Version: c.Version}, EdgesTotal: len(edges), BoundaryTotal: len(boundaries),
	}
	if c.Offset > len(ids) || c.Edge < 0 || c.Edge > len(edges) || c.Boundary < 0 || c.Boundary > len(boundaries) {
		return out, fmt.Errorf("invalid_cursor")
	}
	budget := q.MaxBytes
	if budget <= 0 {
		budget = 128 << 10
	}
	if budget < 4096 {
		budget = 4096
	}
	if budget > 256<<10 {
		budget = 256 << 10
	}
	used := 1500 // cursor, counts and envelope, including the query fingerprint
	canAdd := func(v any) bool {
		raw, _ := json.Marshal(v)
		if used+len(raw)+2 > budget {
			return false
		}
		used += len(raw) + 2
		return true
	}
	projectedFact := q.Projection == GraphProjectionGraph
	for count := 0; c.Offset < len(ids) && count < pageLimit(q.Limit); count++ {
		id := ids[c.Offset]
		node, detail := graphNode(id, nodes[id], budget-1500, projectedFact)
		raw, _ := json.Marshal(node)
		detailRaw, _ := json.Marshal(detail)
		var metaRow FactMeta
		isFact := false
		var metaRaw []byte
		if f, ok := node.(model.Fact); ok {
			isFact = true
			metaRow = factMeta[id]
			if metaRow.ID == "" {
				metaRow = FactMeta{ID: f.ID, EvidenceCount: len(f.EvidenceIDs), RefCount: len(f.Refs)}
			}
			metaRaw, _ = json.Marshal(metaRow)
		}
		cost := len(raw) + len(detailRaw) + 4
		if isFact {
			// fact_meta counts against the same byte budget as the node itself:
			// a page must stay bounded including the fields it always ships.
			cost += len(metaRaw) + 2
		}
		if used+cost > budget {
			break
		}
		used += cost
		switch v := node.(type) {
		case model.Fact:
			out.Facts = append(out.Facts, v)
		case model.Intent:
			out.Intents = append(out.Intents, v)
		case model.Goal:
			out.Goals = append(out.Goals, v)
		}
		if isFact {
			out.FactMeta = append(out.FactMeta, metaRow)
		}
		if len(detail.Omitted) > 0 {
			out.Details = append(out.Details, detail)
		}
		c.Offset++
	}
	edgeLimit := q.EdgeLimit
	if edgeLimit <= 0 {
		edgeLimit = pageLimit(q.Limit)
	}
	for count := 0; c.Edge < len(edges) && count < pageLimit(edgeLimit); count++ {
		if !canAdd(edges[c.Edge]) {
			break
		}
		out.Edges = append(out.Edges, edges[c.Edge])
		c.Edge++
	}
	boundaryLimit := q.BoundaryLimit
	if boundaryLimit <= 0 {
		boundaryLimit = pageLimit(q.Limit)
	}
	for count := 0; c.Boundary < len(boundaries) && count < pageLimit(boundaryLimit); count++ {
		if !canAdd(boundaries[c.Boundary]) {
			break
		}
		out.Boundary = append(out.Boundary, boundaries[c.Boundary])
		c.Boundary++
	}
	out.HasMore = c.Offset < len(ids) || c.Edge < len(edges) || c.Boundary < len(boundaries)
	if out.HasMore {
		if len(out.Facts)+len(out.Intents)+len(out.Goals)+len(out.Edges)+len(out.Boundary) == 0 {
			return out, fmt.Errorf("graph object exceeds byte budget; read object detail")
		}
		out.NextCursor = encode(c)
	}
	return out, nil
}

func graphNode(id string, node any, budget int, projected bool) (any, NodeDetail) {
	d := NodeDetail{ID: id, Omitted: []string{}}
	tooLarge := func(v any) bool { raw, _ := json.Marshal(v); return len(raw) > budget/2 }
	if !tooLarge(node) {
		return node, d
	}
	parts := strings.SplitN(id, ":", 2)
	switch v := node.(type) {
	case model.Fact:
		d.Query = "/api/facts/" + parts[1]
		if projected {
			// projection=graph already dropped the body before the budget was
			// measured. Only a pathological title/summary is truncated here, and
			// that is a real size cut, not the graph projection: it must not
			// claim doc/evidence_ids/refs/tags were omitted.
			if tooLarge(v) {
				v = model.Fact{ID: v.ID, IntentID: v.IntentID, Kind: v.Kind, Status: v.Status, Title: clip(v.Title, 160), Summary: clip(v.Summary, 240)}
				d.Omitted = append(d.Omitted, "full_text", "remaining_fields")
			}
			node = v
			return node, d
		}
		v.Doc = ""
		d.Omitted = append(d.Omitted, "doc")
		if tooLarge(v) {
			v.EvidenceIDs = nil
			v.Refs = nil
			v.Tags = nil
			v.Title = clip(v.Title, 160)
			v.Summary = clip(v.Summary, 240)
			d.Omitted = append(d.Omitted, "evidence_ids", "refs", "tags", "full_text")
		}
		if tooLarge(v) {
			v = model.Fact{ID: v.ID, IntentID: v.IntentID, Kind: v.Kind, Status: v.Status, Title: clip(v.Title, 160), Summary: clip(v.Summary, 240)}
			d.Omitted = append(d.Omitted, "remaining_fields")
		}
		node = v
	case model.Intent:
		d.Query = "/api/intents/" + parts[1]
		v.Doc = ""
		v.Notes = nil
		v.Interjections = nil
		v.Handoff = nil
		d.Omitted = append(d.Omitted, "doc", "notes", "interjections", "handoff")
		if tooLarge(v) {
			v.FromFactIDs = nil
			v.Skills = nil
			v.Wait = nil
			v.Title = clip(v.Title, 160)
			v.Description = clip(v.Description, 240)
			v.Eval = ""
			d.Omitted = append(d.Omitted, "from_fact_ids", "skills", "wait", "full_text")
		}
		if tooLarge(v) {
			v = model.Intent{ID: v.ID, Status: v.Status, Title: clip(v.Title, 160), Description: clip(v.Description, 240)}
			d.Omitted = append(d.Omitted, "remaining_fields", "from_fact_ids")
		}
		node = v
	case model.Goal:
		d.Query = "/api/goals"
		v.From = nil
		v.Statement = clip(v.Statement, 240)
		v.QuantifiedTarget = clip(v.QuantifiedTarget, 240)
		v.Note = clip(v.Note, 240)
		d.Omitted = append(d.Omitted, "from", "full_text")
		node = v
	}
	return node, d
}

// latestReviewByFact keeps the newest review per fact. "Newest" is the
// (CreatedAt, id) total order, so a same-instant pair still resolves
// deterministically.
func latestReviewByFact(reviews []model.Review) map[string]model.Review {
	out := make(map[string]model.Review, len(reviews))
	for _, r := range reviews {
		prev, ok := out[r.FactID]
		if !ok || r.CreatedAt.After(prev.CreatedAt) || (r.CreatedAt.Equal(prev.CreatedAt) && r.ID > prev.ID) {
			out[r.FactID] = r
		}
	}
	return out
}

// factMetaOf derives the graph-page metadata from the original fact (before any
// projection/size truncation), so counts and the review verdict stay truthful.
func factMetaOf(f model.Fact, latest map[string]model.Review) FactMeta {
	m := FactMeta{ID: f.ID, EvidenceCount: len(f.EvidenceIDs), RefCount: len(f.Refs)}
	if r, ok := latest[f.ID]; ok {
		m.ReviewedClaimVersion = r.ClaimVersion
		m.NeedsReview = f.ClaimVersion > r.ClaimVersion
	}
	return m
}
