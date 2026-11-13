package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"

	"github.com/kksty/tianwu/internal/retrieval"
	"github.com/kksty/tianwu/internal/store"
)

// The focus index written into the run's case. It is a bounded, frozen index of
// the hot/warm rows next to the complete premise directory — not a second
// ledger and not the full text of anything.
const (
	focusFile = ".tw/focus.json"
	focusDir  = ".tw/focus"

	// focusMaxPages bounds the on-disk index. When a projection has more
	// hot/warm rows than fit, the manifest says how many were omitted and points
	// at the API continuation; cold rows are never written as pages at all.
	focusMaxPages = 16
)

// focusManifest is the stable entry point the prompt names. It is written last:
// a manifest that exists always describes the pages that exist.
type focusManifest struct {
	IntentID  string             `json:"intent_id"`
	Snapshot  *store.ReadVersion `json:"snapshot,omitempty"`
	Hot       int                `json:"hot"`
	Warm      int                `json:"warm"`
	Cold      int                `json:"cold"`
	Indexed   int                `json:"indexed"`
	Pages     []string           `json:"pages"`
	Truncated bool               `json:"truncated"`
	Omitted   int                `json:"omitted"`
	// The queries are read paths, not instructions: cold facts, by-id reads and
	// the complete premise directory all remain reachable without this index.
	IndexQuery string `json:"index_query"`
	HotQuery   string `json:"hot_query"`
	WarmQuery  string `json:"warm_query"`
	ColdQuery  string `json:"cold_query"`
	FactQuery  string `json:"fact_query"`
	// Premises is the case-relative complete premise catalogue. PremisesQuery is
	// the same list from the ledger, so a reader without the case directory can
	// still enumerate every required premise (no second memory system: both are
	// projections of the one ledger).
	Premises      string `json:"premises"`
	PremisesQuery string `json:"premises_query"`
	Experience    string `json:"experience_query"`
	// ReasonCounts and Contract are the same values the API page returns: the
	// model reads why a row is here and what heat means from the same contract
	// the operator reads. A cut index is a bounded digest, never "all memory".
	ReasonCounts map[string]int          `json:"reason_counts"`
	Contract     retrieval.FocusContract `json:"contract"`
}

// focusPageFile is one bounded page: the summary layer only. Doc bodies are
// deliberately absent; the row's id/query is how the full record is read.
type focusPageFile struct {
	IntentID string                `json:"intent_id"`
	Page     int                   `json:"page"`
	Snapshot *store.ReadVersion    `json:"snapshot,omitempty"`
	Facts    []retrieval.FocusFact `json:"facts"`
}

// writeFocus writes the frozen focus index for one dispatch and reports whether
// the prompt may point at it. The caller logs a failure; it must not fail the
// dispatch, because the brief and the premise directory are still complete
// without it (the same contract as writeGraph).
func writeFocus(workDir string, db store.DB, intentID string) bool {
	return writeFocusFiles(workDir, db, intentID) == nil
}

func writeFocusFiles(workDir string, db store.DB, intentID string) error {
	items, err := retrieval.FocusItems(db, intentID, focusVersions(db))
	if err != nil {
		return err
	}
	snapshot := focusSnapshot(db)
	dir := filepath.Join(workDir, filepath.FromSlash(focusDir))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// Drop the old manifest before touching pages: if a later write fails, the
	// case must not keep a manifest pointing at pages this pass removed. The
	// prompt only names the file when writeFocus returns true.
	if err := os.Remove(filepath.Join(workDir, filepath.FromSlash(focusFile))); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := pruneStalePages(dir, "page-*.json*", nil); err != nil {
		return err
	}

	manifest := focusManifest{
		IntentID:      intentID,
		Snapshot:      snapshot,
		Pages:         []string{},
		IndexQuery:    retrieval.FocusLayerQuery(intentID, "focus"),
		HotQuery:      retrieval.FocusLayerQuery(intentID, retrieval.FocusHot),
		WarmQuery:     retrieval.FocusLayerQuery(intentID, retrieval.FocusWarm),
		ColdQuery:     retrieval.FocusLayerQuery(intentID, retrieval.FocusCold),
		FactQuery:     "/api/facts?intent=" + intentID,
		Premises:      ".tw/context.json",
		PremisesQuery: retrieval.FocusPremisesQuery(intentID),
		Experience:    "/api/experience?intent=" + intentID,
		ReasonCounts:  retrieval.FocusReasonCounts(items),
		Contract:      retrieval.FocusContractValue(),
	}
	// Count the whole projection first; the counts describe the snapshot even
	// when the on-disk index is cut short. Only hot/warm rows are paged: the
	// cold layer stays queryable through the API and is not copied to disk.
	paged := make([]retrieval.FocusFact, 0, len(items))
	for _, item := range items {
		switch item.Temperature {
		case retrieval.FocusHot:
			manifest.Hot++
		case retrieval.FocusWarm:
			manifest.Warm++
		default:
			manifest.Cold++
			continue
		}
		paged = append(paged, item)
	}
	batch := make([]retrieval.FocusFact, 0, 64)
	used := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		page := focusPageFile{
			IntentID: intentID,
			Page:     len(manifest.Pages) + 1,
			Snapshot: snapshot,
			Facts:    batch,
		}
		name := "page-" + strconv.Itoa(page.Page) + ".json"
		if err := writeJSONFileAtomic(filepath.Join(dir, name), page); err != nil {
			return err
		}
		manifest.Pages = append(manifest.Pages, focusDir+"/"+name)
		manifest.Indexed += len(batch)
		batch = make([]retrieval.FocusFact, 0, 64)
		used = 0
		return nil
	}
	for _, item := range paged {
		raw, err := json.Marshal(item)
		if err != nil {
			return err
		}
		if len(batch) > 0 && used+len(raw) > retrieval.FocusPageBytes {
			if len(manifest.Pages) >= focusMaxPages {
				manifest.Truncated = true
				manifest.Omitted = len(paged) - manifest.Indexed
				batch = nil
				break
			}
			if err := flush(); err != nil {
				return err
			}
		}
		batch = append(batch, item)
		used += len(raw)
	}
	if len(batch) > 0 {
		if len(manifest.Pages) >= focusMaxPages {
			manifest.Truncated = true
			manifest.Omitted = len(paged) - manifest.Indexed
		} else if err := flush(); err != nil {
			return err
		}
	}
	return writeJSONFileAtomic(filepath.Join(workDir, filepath.FromSlash(focusFile)), manifest)
}

// focusSnapshot returns the frozen ledger watermark of the dispatch DB, when it
// carries one (PrepareRunInput does; a read-path DB deliberately does not).
func focusSnapshot(db store.DB) *store.ReadVersion {
	if db.Memory == nil {
		return nil
	}
	return &store.ReadVersion{Epoch: db.Memory.Epoch, Seq: db.Memory.Seq}
}

// focusVersions maps fact id to record version at this frozen DB. It is the
// same rule as store.FactVersionsAt, applied to the DB the dispatch already
// holds: last recorded change wins, moves never count and deletes drop the id.
func focusVersions(db store.DB) map[string]uint64 {
	out := map[string]uint64{}
	if db.Memory == nil {
		return out
	}
	for _, c := range db.Memory.History {
		if c.Kind != "fact" || c.Operation == "move" {
			continue
		}
		if c.Operation == "delete" {
			delete(out, c.ID)
			continue
		}
		out[c.ID] = c.Version
	}
	return out
}

// pruneStalePages removes files matching pattern in dir that are not in keep
// (nil keep removes every match). It runs before a new page set is written, so
// a case that shrank never keeps a page the manifest does not name.
func pruneStalePages(dir, pattern string, keep map[string]bool) error {
	matches, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		return err
	}
	for _, path := range matches {
		if keep[filepath.Base(path)] {
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// writeJSONFileAtomic writes JSON next to its target and renames it into place,
// so a reader never sees a half-written page or manifest.
func writeJSONFileAtomic(target string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
