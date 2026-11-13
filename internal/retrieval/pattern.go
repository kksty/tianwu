package retrieval

import (
	"sort"
	"strings"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

type PatternResult struct {
	State    string            `json:"state"`
	Bindings map[string]string `json:"bindings"`
	Reason   string            `json:"reason,omitempty"`
}

type patternContext struct {
	facts   []model.Fact
	intents []model.Intent
	links   map[string]bool
}

func newPatternContext(db store.DB, contextIDs []string) *patternContext {
	context := &patternContext{intents: db.Intents, links: map[string]bool{}}
	allowed := map[string]bool{}
	for _, id := range contextIDs {
		allowed[id] = true
	}
	link := func(a, b, k string) { context.links[a+"\x00"+b+"\x00"+k] = true }
	for _, in := range db.Intents {
		for _, id := range in.FromFactIDs {
			link("fact:"+id, "intent:"+in.ID, "premise")
		}
		if in.ParentID != "" {
			link("intent:"+in.ParentID, "intent:"+in.ID, "delegates")
		}
	}
	for _, f := range db.Facts {
		if allowed[f.ID] {
			context.facts = append(context.facts, f)
		}
		if f.IntentID != "" {
			link("intent:"+f.IntentID, "fact:"+f.ID, "produces")
		}
	}
	return context
}

// MatchPattern checks only recorded directed relationships. An incomplete
// entity binding or exhausted search is unknown, never a positive match.
func MatchPattern(db store.DB, p *model.ExperiencePattern, contextIDs []string) PatternResult {
	if p == nil {
		return PatternResult{State: "unspecified", Bindings: map[string]string{}}
	}
	return matchPattern(p, newPatternContext(db, contextIDs))
}
func matchPattern(p *model.ExperiencePattern, context *patternContext) PatternResult {
	out := PatternResult{State: "unspecified", Bindings: map[string]string{}}
	if p == nil {
		return out
	}
	out.State = "missing"
	if err := store.ValidatePattern(p); err != nil {
		out.State = "unknown"
		out.Reason = err.Error()
		return out
	}
	type candidate struct{ id, entity string }
	choices := map[string][]candidate{}
	uncertain := false
	links := context.links
	for _, n := range p.Nodes {
		if n.Type == "intent" {
			for _, in := range context.intents {
				choices[n.Name] = append(choices[n.Name], candidate{id: "intent:" + in.ID})
			}
			continue
		}
		for _, f := range context.facts {
			if f.Status == "rejected" || n.Kind != "" && n.Kind != f.Kind {
				continue
			}
			ok := true
			for _, tag := range n.Tags {
				found := false
				for _, t := range f.Tags {
					if strings.EqualFold(t, tag) {
						found = true
					}
				}
				if !found {
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
			if n.EntityVar != "" && f.EntityRef == "" {
				uncertain = true
				continue
			}
			choices[n.Name] = append(choices[n.Name], candidate{"fact:" + f.ID, f.EntityRef})
		}
	}
	order := append([]model.PatternNode(nil), p.Nodes...)
	sort.SliceStable(order, func(i, j int) bool { return len(choices[order[i].Name]) < len(choices[order[j].Name]) })
	for name := range choices {
		sort.Slice(choices[name], func(i, j int) bool { return choices[name][i].id < choices[name][j].id })
	}
	bound := map[string]string{}
	used := map[string]bool{}
	entities := map[string]string{}
	work := 0
	exhausted := false
	var search func(int) bool
	search = func(index int) bool {
		if index == len(order) {
			return true
		}
		n := order[index]
		for _, v := range choices[n.Name] {
			work++
			if work > 10000 {
				exhausted = true
				return false
			}
			if used[v.id] {
				continue
			}
			old, hasEntity := entities[n.EntityVar]
			if n.EntityVar != "" && hasEntity && old != v.entity {
				continue
			}
			bound[n.Name] = v.id
			valid := true
			for _, e := range p.Edges {
				a, aok := bound[e.Source]
				b, bok := bound[e.Target]
				if aok && bok && !links[a+"\x00"+b+"\x00"+e.Kind] {
					valid = false
					break
				}
			}
			if valid {
				used[v.id] = true
				if n.EntityVar != "" {
					entities[n.EntityVar] = v.entity
				}
				if search(index + 1) {
					return true
				}
				delete(used, v.id)
				if n.EntityVar != "" && !hasEntity {
					delete(entities, n.EntityVar)
				}
			}
			delete(bound, n.Name)
		}
		return false
	}
	if search(0) {
		out.State = "matched"
		out.Bindings = bound
		return out
	}
	if uncertain {
		out.State = "unknown"
		out.Reason = "entity identity is not recorded"
	}
	if exhausted {
		out.State = "unknown"
		out.Reason = "pattern search budget exhausted"
	}
	return out
}
