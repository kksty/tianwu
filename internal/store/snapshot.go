package store

import "github.com/kksty/tianwu/internal/model"

func detachGoal(g model.Goal) model.Goal {
	g.From = append([]string(nil), g.From...)
	g.FromClaimVersions = copyVersions(g.FromClaimVersions)
	g.ReceiptChanged = append([]string(nil), g.ReceiptChanged...)
	if g.CompletedAt != nil {
		at := *g.CompletedAt
		g.CompletedAt = &at
	}
	return g
}

// markGoalReceipt computes whether a completed goal's receipt still rests on the
// claims it cited. It is a read-side observation: the stored receipt is left
// exactly as it was stamped, so "what did we prove at the time" stays answerable.
func markGoalReceipt(g model.Goal, facts []model.Fact) model.Goal {
	g.ReceiptStale, g.ReceiptChanged = false, nil
	if !g.Met || len(g.FromClaimVersions) == 0 {
		return g
	}
	byID := make(map[string]uint64, len(facts))
	rejected := make(map[string]bool)
	for i := range facts {
		byID[facts[i].ID] = facts[i].ClaimVersion
		rejected[facts[i].ID] = facts[i].Status == "rejected"
	}
	for _, id := range g.From {
		stamped, ok := g.FromClaimVersions[id]
		if !ok {
			continue
		}
		current, exists := byID[id]
		if !exists || current != stamped || rejected[id] {
			g.ReceiptChanged = append(g.ReceiptChanged, id)
		}
	}
	if len(g.ReceiptChanged) > 0 {
		g.ReceiptStale = true
	}
	return g
}

func detachPlaybook(p model.Playbook) model.Playbook {
	p.Tags = append([]string(nil), p.Tags...)
	if p.RequiredTags != nil {
		p.RequiredTags = append([]string{}, p.RequiredTags...)
	}
	p.FromFactIDs = append([]string(nil), p.FromFactIDs...)
	if p.FromFactVersions != nil {
		v := map[string]uint64{}
		for k, n := range p.FromFactVersions {
			v[k] = n
		}
		p.FromFactVersions = v
	}
	if p.Pattern != nil {
		pattern := *p.Pattern
		pattern.Nodes = append([]model.PatternNode(nil), pattern.Nodes...)
		pattern.Edges = append([]model.PatternEdge(nil), pattern.Edges...)
		for i := range pattern.Nodes {
			pattern.Nodes[i].Tags = append([]string(nil), pattern.Nodes[i].Tags...)
		}
		p.Pattern = &pattern
	}
	return p
}

func detachFact(f model.Fact) model.Fact {
	if f.ObservedAt != nil {
		at := *f.ObservedAt
		f.ObservedAt = &at
	}
	f.EvidenceIDs = append([]string(nil), f.EvidenceIDs...)
	f.Tags = append([]string(nil), f.Tags...)
	f.Refs = append([]model.Ref(nil), f.Refs...)
	return f
}
func detachRun(r model.Run) model.Run {
	r.ContextFactVersions = copyVersions(r.ContextFactVersions)
	r.PremiseClaimVersions = copyVersions(r.PremiseClaimVersions)
	r.ResourceRefs = append([]string(nil), r.ResourceRefs...)
	r.InstructionVersions = copyVersions(r.InstructionVersions)
	r.InstructionNotices = copyVersions(r.InstructionNotices)
	r.InstructionAcks = copyVersions(r.InstructionAcks)
	if r.PremiseVersions != nil {
		versions := map[string]uint64{}
		for id, v := range r.PremiseVersions {
			versions[id] = v
		}
		r.PremiseVersions = versions
	}
	r.Warnings = append([]string(nil), r.Warnings...)
	if r.DeclarationStart != nil {
		v := *r.DeclarationStart
		r.DeclarationStart = &v
	}
	if r.EndedAt != nil {
		v := *r.EndedAt
		r.EndedAt = &v
	}
	if r.LastEventAt != nil {
		v := *r.LastEventAt
		r.LastEventAt = &v
	}
	if r.LastTurnStartedAt != nil {
		v := *r.LastTurnStartedAt
		r.LastTurnStartedAt = &v
	}
	if r.Pricing != nil {
		v := *r.Pricing
		r.Pricing = &v
	}
	if r.Cost != nil {
		v := *r.Cost
		r.Cost = &v
	}
	return r
}

func copyVersions(v map[string]uint64) map[string]uint64 {
	if v == nil {
		return nil
	}
	out := make(map[string]uint64, len(v))
	for id, version := range v {
		out[id] = version
	}
	return out
}

// Store inputs and snapshots must own every mutable field. Readers keep these
// values after the lock is released, while later transactions edit live rows.
func detachIntent(in model.Intent) model.Intent {
	in.ContextFactIDs = append([]string(nil), in.ContextFactIDs...)
	in.PinnedFactIDs = append([]string(nil), in.PinnedFactIDs...)
	in.InterestEntityRefs = append([]string(nil), in.InterestEntityRefs...)
	in.FromFactIDs = append([]string(nil), in.FromFactIDs...)
	if in.FromFactClaims != nil {
		claims := make(map[string]uint64, len(in.FromFactClaims))
		for id, version := range in.FromFactClaims {
			claims[id] = version
		}
		in.FromFactClaims = claims
	}
	in.Skills = append([]string(nil), in.Skills...)
	in.Interjections = append([]model.Interjection(nil), in.Interjections...)
	in.Notes = append([]model.IntentNote(nil), in.Notes...)
	in.Wait = detachWait(in.Wait)
	if in.Handoff != nil {
		h := *in.Handoff
		h.Wait = detachWait(h.Wait)
		h.Facts = append([]string(nil), h.Facts...)
		h.Evidence = append([]string(nil), h.Evidence...)
		h.Attempts = append([]string(nil), h.Attempts...)
		h.Children = append([]string(nil), h.Children...)
		in.Handoff = &h
	}
	return in
}

func detachWait(wait *model.Wait) *model.Wait {
	if wait == nil {
		return nil
	}
	w := *wait
	w.Actions = append([]string(nil), wait.Actions...)
	if w.Deadline != nil {
		deadline := *w.Deadline
		w.Deadline = &deadline
	}
	w.Children = append([]string(nil), w.Children...)
	w.Results = append([]model.WaitResult(nil), w.Results...)
	for i := range w.Results {
		w.Results[i].FactIDs = append([]string(nil), w.Results[i].FactIDs...)
		w.Results[i].EvidenceIDs = append([]string(nil), w.Results[i].EvidenceIDs...)
	}
	if w.Operator != nil {
		o := *w.Operator
		o.AccessIDs = append([]string(nil), o.AccessIDs...)
		if o.CompletedAt != nil {
			completed := *o.CompletedAt
			o.CompletedAt = &completed
		}
		w.Operator = &o
	}
	return &w
}
