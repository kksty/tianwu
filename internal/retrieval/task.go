package retrieval

import "github.com/kksty/tianwu/internal/store"

// TaskSources follows recorded provenance, including joint prerequisites. It
// does not infer an entity or choose a tactical direction from prose.
func TaskSources(db store.DB, intentID string) []string {
	from := map[string][]string{}
	producer := map[string]string{}
	for _, in := range db.Intents {
		from[in.ID] = in.FromFactIDs
	}
	for _, f := range db.Facts {
		producer[f.ID] = f.IntentID
	}
	queue := append([]string(nil), from[intentID]...)
	seen := map[string]bool{}
	out := []string{}
	for i := 0; i < len(queue); i++ {
		id := queue[i]
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
		if parent := producer[id]; parent != "" {
			queue = append(queue, from[parent]...)
		}
	}
	return out
}
