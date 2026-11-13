package retrieval

import (
	"fmt"
	"sort"
	"strings"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

type ExperienceQuery struct {
	Q      string `json:"q,omitempty"`
	Intent string `json:"intent,omitempty"`
	Cursor string `json:"-"`
	Limit  int    `json:"-"`
}
type Experience struct {
	Method          string         `json:"match_method"`
	Structure       PatternResult  `json:"structure"`
	Playbook        model.Playbook `json:"playbook"`
	SupportingFacts []string       `json:"supporting_facts"`
	MissingTags     []string       `json:"missing_tags"`
	SourceFacts     []Boundary     `json:"source_facts"`
	Match           string         `json:"precondition_match"`
	Score           int            `json:"score"`
}
type ExperiencePage struct {
	Candidates []Experience `json:"candidates"`
	PageInfo
}

func ExperienceCandidates(db store.DB, q ExperienceQuery) []Experience {
	facts := map[string]model.Fact{}
	for _, f := range db.Facts {
		facts[f.ID] = f
	}
	contextIDs := TaskSources(db, q.Intent)
	if q.Intent == "" {
		for _, f := range db.Facts {
			contextIDs = append(contextIDs, f.ID)
		}
	}
	query := q.Q
	for _, in := range db.Intents {
		if in.ID == q.Intent {
			query += " " + in.Title + " " + in.Description
		}
	}
	terms := strings.Fields(strings.ToLower(query))
	out := []Experience{}
	var context *patternContext
	for _, p := range db.Playbooks {
		if p.Archived || p.Status == model.PlaybookRetired || p.Outcome == model.PlaybookDeadEnd {
			continue
		}
		v := Experience{Playbook: p, SupportingFacts: []string{}, MissingTags: []string{}, SourceFacts: []Boundary{}, Match: "unspecified"}
		v.Method = "tags"
		text := strings.ToLower(p.Title + " " + p.When + " " + p.Doc + " " + strings.Join(p.Tags, " "))
		for _, term := range terms {
			if strings.Contains(text, term) {
				v.Score++
			}
		}
		matched := map[string]bool{}
		for _, required := range p.RequiredTags {
			found := false
			for _, id := range contextIDs {
				f := facts[id]
				if f.Status == "rejected" {
					continue
				}
				for _, tag := range f.Tags {
					if strings.EqualFold(tag, required) {
						found = true
						matched[id] = true
					}
				}
			}
			if !found {
				v.MissingTags = append(v.MissingTags, required)
			}
		}
		for _, id := range contextIDs {
			if matched[id] {
				v.SupportingFacts = append(v.SupportingFacts, id)
			}
		}
		if len(p.RequiredTags) > 0 {
			v.Match = "matched"
			if len(v.MissingTags) > 0 {
				v.Match = "missing"
			}
			v.Score += len(p.RequiredTags) - len(v.MissingTags)
		}
		v.Structure = PatternResult{State: "unspecified", Bindings: map[string]string{}}
		if p.Pattern != nil {
			if context == nil {
				context = newPatternContext(db, contextIDs)
			}
			v.Structure = matchPattern(p.Pattern, context)
		}
		if p.Pattern != nil {
			v.Method = "recorded_graph"
			v.Match = v.Structure.State
			if v.Match == "matched" {
				if len(v.MissingTags) > 0 {
					v.Match = "missing"
				} else {
					v.Score += len(p.Pattern.Nodes) + len(p.Pattern.Edges)
				}
				for _, ref := range v.Structure.Bindings {
					if strings.HasPrefix(ref, "fact:") {
						id := strings.TrimPrefix(ref, "fact:")
						if !matched[id] {
							v.SupportingFacts = append(v.SupportingFacts, id)
							matched[id] = true
						}
					}
				}
			}
		}
		sort.Strings(v.SupportingFacts)
		for _, id := range p.FromFactIDs {
			state := "missing"
			if f, ok := facts[id]; ok {
				state = f.Status
			}
			v.SourceFacts = append(v.SourceFacts, Boundary{id, state})
		}
		if strings.TrimSpace(query) != "" && v.Score == 0 {
			continue
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Playbook.ID < out[j].Playbook.ID
	})
	return out
}
func Experiences(s *store.Store, q ExperienceQuery) (ExperiencePage, error) {
	key := queryKey(struct {
		Type  string
		Query ExperienceQuery
	}{"experience", q})
	db, c, e := read(s, q.Cursor, key)
	if e != nil {
		return ExperiencePage{}, e
	}
	rows := ExperienceCandidates(db, q)
	out := ExperiencePage{Candidates: []Experience{}, PageInfo: PageInfo{Total: len(rows), Version: c.Version}}
	if c.Offset > len(rows) {
		return out, fmt.Errorf("invalid_cursor")
	}
	end := c.Offset + pageLimit(q.Limit)
	if end > len(rows) {
		end = len(rows)
	}
	out.Candidates = rows[c.Offset:end]
	out.HasMore = end < len(rows)
	if out.HasMore {
		c.Offset = end
		out.NextCursor = encode(c)
	}
	return out, nil
}
