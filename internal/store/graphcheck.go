package store

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/kksty/tianwu/internal/model"
)

// ErrIntentGraphCycle means a structural write would close a cycle in the
// recorded facts-intents graph. The graph is a ledger projection, not a second
// store, so this invariant is checked on the write paths that can change the
// graph shape (intent creation snapshots claims; UpdateIntent may move a parent
// or premise later).
var ErrIntentGraphCycle = errors.New("意图图会出现环：事实—意图边只能沿已记录方向前进")

// snapshotPremiseClaimsLocked records the claim version of every premise that
// exists now. The board compares it later and reports "premise_changed" without
// blocking the direction: a changed assertion is a judgement input, not an
// automatic permission. Caller holds the write lock.
func (s *Store) snapshotPremiseClaimsLocked(in *model.Intent) {
	if len(in.FromFactIDs) == 0 {
		in.FromFactClaims = nil
		return
	}
	claims := make(map[string]uint64, len(in.FromFactIDs))
	for _, id := range in.FromFactIDs {
		r, ok := s.records[recordKey("fact", id)]
		if !ok {
			continue
		}
		claims[id] = r.value.(model.Fact).ClaimVersion
	}
	if len(claims) == 0 {
		claims = nil
	}
	in.FromFactClaims = claims
}

// PremiseClaimsChanged compares a direction's creation snapshot with the
// current claim versions. Missing records are the dispatch gate's business
// (blocked premises), not this marker.
func PremiseClaimsChanged(in model.Intent, facts map[string]model.Fact) []string {
	if len(in.FromFactClaims) == 0 || len(in.FromFactIDs) == 0 {
		return nil
	}
	var changed []string
	for _, id := range in.FromFactIDs {
		snapshot, ok := in.FromFactClaims[id]
		if !ok {
			continue
		}
		f, exists := facts[id]
		if exists && f.ClaimVersion != snapshot {
			changed = append(changed, id)
		}
	}
	return changed
}

// intentGraphCycleLocked reports the first dependency cycle in the recorded
// graph, if any. Nodes are "fact:<id>" and "intent:<id>"; edges are
// fact->intent (premise), intent->fact (produces) and parent->child (delegates).
// The search is a standard three-colour DFS; it runs only when a structural
// field changes, so normal status/attempt writes do not pay for it.
func (s *Store) intentGraphCycleLocked() (string, bool) {
	adj := map[string][]string{}
	link := func(from, to string) {
		for _, existing := range adj[from] {
			if existing == to {
				return
			}
		}
		adj[from] = append(adj[from], to)
	}
	for _, in := range s.db.Intents {
		owner := "intent:" + in.ID
		for _, id := range in.FromFactIDs {
			link("fact:"+id, owner)
		}
		if in.ParentID != "" {
			link("intent:"+in.ParentID, owner)
		}
	}
	for _, f := range s.db.Facts {
		if f.IntentID != "" {
			link("intent:"+f.IntentID, "fact:"+f.ID)
		}
	}
	const (
		unvisited = 0
		active    = 1
		done      = 2
	)
	color := make(map[string]int, len(adj))
	var stack []string
	var visit func(string) (string, bool)
	visit = func(node string) (string, bool) {
		color[node] = active
		stack = append(stack, node)
		for _, next := range adj[node] {
			switch color[next] {
			case active:
				// Report the cycle path from the repeated node.
				from := 0
				for i, id := range stack {
					if id == next {
						from = i
						break
					}
				}
				path := append(append([]string(nil), stack[from:]...), next)
				return strings.Join(path, " → "), true
			case unvisited:
				if path, cyclic := visit(next); cyclic {
					return path, true
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[node] = done
		return "", false
	}
	for node := range adj {
		if color[node] == unvisited {
			if path, cyclic := visit(node); cyclic {
				return path, true
			}
		}
	}
	return "", false
}

// validateIntentStructureLocked snapshots premise claims after a structural
// edit and refuses it if the resulting graph has a cycle, restoring the row it
// replaced. Caller holds the write lock. It returns the (possibly updated) row
// to persist.
func (s *Store) validateIntentStructureLocked(row *model.Intent, previous model.Intent) error {
	structural := !slices.Equal(row.FromFactIDs, previous.FromFactIDs) || row.ParentID != previous.ParentID
	if !structural {
		return nil
	}
	s.snapshotPremiseClaimsLocked(row)
	if path, cyclic := s.intentGraphCycleLocked(); cyclic {
		*row = previous
		return fmt.Errorf("%w（%s）", ErrIntentGraphCycle, path)
	}
	return nil
}
