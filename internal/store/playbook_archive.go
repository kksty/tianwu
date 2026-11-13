package store

import (
	"fmt"
	"os"
	"time"

	"github.com/kksty/tianwu/internal/model"
)

func ValidatePattern(p *model.ExperiencePattern) error {
	if p == nil {
		return nil
	}
	names := map[string]string{}
	facts := 0
	for _, n := range p.Nodes {
		if n.Name == "" || names[n.Name] != "" || (n.Type != "fact" && n.Type != "intent") {
			return fmt.Errorf("invalid experience pattern node")
		}
		if n.Type != "fact" && (n.EntityVar != "" || n.Kind != "" || len(n.Tags) > 0) {
			return fmt.Errorf("fact attributes require a fact pattern node")
		}
		names[n.Name] = n.Type
		if n.Type == "fact" {
			facts++
		}
	}
	if len(names) == 0 || facts == 0 {
		return fmt.Errorf("empty experience pattern")
	}
	adj := map[string][]string{}
	for _, e := range p.Edges {
		source, target := names[e.Source], names[e.Target]
		valid := e.Kind == "premise" && source == "fact" && target == "intent" ||
			e.Kind == "produces" && source == "intent" && target == "fact" ||
			e.Kind == "delegates" && source == "intent" && target == "intent" && e.Source != e.Target
		if !valid {
			return fmt.Errorf("invalid experience pattern edge")
		}
		adj[e.Source] = append(adj[e.Source], e.Target)
		adj[e.Target] = append(adj[e.Target], e.Source)
	}
	seen := map[string]bool{p.Nodes[0].Name: true}
	queue := []string{p.Nodes[0].Name}
	for i := 0; i < len(queue); i++ {
		for _, id := range adj[queue[i]] {
			if !seen[id] {
				seen[id] = true
				queue = append(queue, id)
			}
		}
	}
	if len(seen) != len(names) {
		return fmt.Errorf("experience pattern has disconnected nodes")
	}
	return nil
}

// Archival is explicit curation. Neither a full list nor an executor update
// silently removes or restores an operator's archived experience.
func (s *Store) ArchivePlaybook(id string, expected time.Time, archived bool) (model.Playbook, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, p := range s.db.Playbooks {
		if p.ID != id {
			continue
		}
		if !p.UpdatedAt.Equal(expected) {
			return model.Playbook{}, ErrPlaybookChanged
		}
		s.db.Playbooks[i].Archived = archived
		s.db.Playbooks[i].UpdatedAt = time.Now()
		out := detachPlaybook(s.db.Playbooks[i])
		return out, s.flushNoRev("playbook")
	}
	return model.Playbook{}, os.ErrNotExist
}
