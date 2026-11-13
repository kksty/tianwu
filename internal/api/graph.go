package api

import (
	"encoding/json"
	"fmt"
	"github.com/kksty/tianwu/internal/model"
	"os"
	"path/filepath"

	"github.com/kksty/tianwu/internal/board"
	"github.com/kksty/tianwu/internal/store"
)

// graphFile is the executor's read-only snapshot of the ledger. It lives inside
// .tw, which never travels in a bundle: it is a working view, not a deliverable.
const graphFile = ".tw/graph.json"

// writeGraph writes the snapshot and reports whether the prompt may point at
// it. A failed write is not a failed dispatch — the run still has the brief,
// and the brief must not promise a file that is not there.
func writeGraph(workDir string, db store.DB, scope string, focus ...string) bool {
	g := board.BuildGraph(db, scope)
	if len(focus) > 0 {
		g = board.BuildTaskGraph(db, scope, focus[0])
		if err := writeTaskManifest(workDir, db, focus[0]); err != nil {
			return false
		}
	}
	b, err := json.Marshal(g)
	if err != nil {
		return false
	}
	b = append(b, '\n')
	target := filepath.Join(workDir, filepath.FromSlash(graphFile))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return false
	}
	// Write then rename so a continuation never reads a half-written snapshot.
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return false
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return false
	}
	return true
}

func writeTaskManifest(workDir string, db store.DB, intentID string) error {
	type ref struct {
		ID      string `json:"id"`
		Version uint64 `json:"version"`
		State   string `json:"state"`
	}
	byID := map[string]model.Fact{}
	versions := map[string]uint64{}
	for _, f := range db.Facts {
		byID[f.ID] = f
	}
	if db.Memory != nil {
		for _, c := range db.Memory.History {
			if c.Kind == "fact" {
				versions[c.ID] = c.Version
			}
		}
	}
	refs := []ref{}
	for _, in := range db.Intents {
		if in.ID == intentID {
			for _, id := range in.FromFactIDs {
				state := "recorded"
				f, ok := byID[id]
				if !ok {
					state = "missing"
				} else if f.Status == "rejected" {
					state = "rejected"
				}
				refs = append(refs, ref{id, versions[id], state})
			}
		}
	}
	dir := filepath.Join(workDir, ".tw", "context")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	pages := []string{}
	for start := 0; start < len(refs); start += 128 {
		end := start + 128
		if end > len(refs) {
			end = len(refs)
		}
		name := fmt.Sprintf("premises-%04d.json", len(pages)+1)
		raw, err := json.Marshal(refs[start:end])
		if err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
			return err
		}
		pages = append(pages, ".tw/context/"+name)
	}
	// A later dispatch can need fewer pages than an earlier one; without this
	// sweep the directory keeps higher-numbered pages that the new manifest
	// does not name, and a reader following the old names finds stale premises.
	keep := map[string]bool{}
	for _, rel := range pages {
		keep[filepath.Base(rel)] = true
	}
	if err := pruneStalePages(dir, "premises-*.json", keep); err != nil {
		return err
	}
	manifest := map[string]any{"intent_id": intentID, "prerequisites_total": len(refs), "pages": pages, "graph_query": "/api/graph?root=intent:" + intentID + "&direction=ancestors&depth=0", "fact_query": "/api/facts?intent=" + intentID, "snapshot": true}
	if db.Memory != nil {
		manifest["epoch"] = db.Memory.Epoch
		manifest["seq"] = db.Memory.Seq
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(workDir, ".tw", "context.json"), raw, 0600)
}
