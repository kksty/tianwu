// Package vulnlib is the instance-level vulnerability library.
//
// The engagement is disposable, the finding is not. Every project mirrors its
// `vuln` facts here as they are declared, and deleting a project removes
// everything under projects/<id>/ but leaves these entries (D34). That is the
// one exception to "delete means delete", and it is deliberate: a confirmed
// vulnerability is the part of an engagement that stays useful after the client
// is gone.
//
// Evidence is copied in at capture time rather than referenced, because the
// project workspace it lives in is exactly what deletion destroys. The copy is
// capped: a vulnerability's proof is a minimal reproducer, and the library must
// not silently duplicate a firmware image.
package vulnlib

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kksty/tianwu/internal/evidence"
	"github.com/kksty/tianwu/internal/model"
)

const (
	// FileName holds the library index.
	FileName = "vulns.json"
	// EvidenceDir is where copied evidence lives, below the library root.
	EvidenceDir = "evidence"
	// MaxEvidenceCopy caps one copied file. Larger files are recorded in
	// evidence_dropped instead of being duplicated.
	MaxEvidenceCopy = 32 << 20
)

// Review is the latest verifier verdict copied from the project ledger. It is a
// copy, not a reference: deleting the project deletes the ledger it lives in,
// and the library has to remain able to say why the entry is in its state (D105).
type Review struct {
	Verdict     string    `json:"verdict"` // pass|needs_evidence|reject
	Notes       string    `json:"notes,omitempty"`
	VerifierRun string    `json:"verifier_run,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// Entry is one vulnerability, attributed to the project that found it.
type Entry struct {
	ID string `json:"id"`
	// ProjectID and Project are captured at declaration time. They outlive the
	// project: after a delete the entry still says where it came from.
	ProjectID string `json:"project_id"`
	Project   string `json:"project,omitempty"`
	// SourceID is the fact id in that project, kept for traceability.
	SourceID   string `json:"source_id"`
	IntentID   string `json:"intent_id,omitempty"`
	Intent     string `json:"intent,omitempty"`
	Title      string `json:"title"`
	Summary    string `json:"summary,omitempty"`
	Doc        string `json:"doc,omitempty"`
	Confidence string `json:"confidence,omitempty"`
	// Proof says how the conclusion is reproduced: dynamic|static (D35).
	Proof  string `json:"proof,omitempty"`
	Status string `json:"status"`
	// Review is the latest verdict copied from the project ledger, so the
	// library can still explain the status after the project is deleted (D105).
	Review *Review `json:"review,omitempty"`
	// Evidence paths are relative to the library root.
	Evidence []string `json:"evidence,omitempty"`
	Dropped  []string `json:"evidence_dropped,omitempty"`
	// CreatedAt is when the project declared it; UpdatedAt follows the fact's
	// status (unverified → verified / rejected).
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// Orphaned is set when the owning project is deleted.
	Orphaned bool `json:"orphaned,omitempty"`
}

type file struct {
	Version int     `json:"version"`
	Vulns   []Entry `json:"vulns"`
}

// Library is the on-disk index plus its evidence directory.
type Library struct {
	root string
	path string

	mu    sync.Mutex
	byKey map[string]*Entry
	order []string
}

// Open loads (or creates) the library below dir.
func Open(dir string) (*Library, error) {
	if err := os.MkdirAll(filepath.Join(dir, EvidenceDir), 0o755); err != nil {
		return nil, err
	}
	l := &Library{root: dir, path: filepath.Join(dir, FileName), byKey: map[string]*Entry{}}
	b, err := os.ReadFile(l.path)
	if err != nil {
		if os.IsNotExist(err) {
			return l, nil
		}
		return nil, err
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", l.path, err)
	}
	for i := range f.Vulns {
		e := f.Vulns[i]
		key := keyOf(e.ProjectID, e.SourceID)
		l.byKey[key] = &e
		l.order = append(l.order, key)
	}
	return l, nil
}

func keyOf(projectID, sourceID string) string { return projectID + "\x00" + sourceID }

// Upsert records one declaration. The same fact updated twice (unverified →
// verified) keeps its id and creation time, so a re-scan does not create a
// second row for the same finding.
func (l *Library) Upsert(e Entry) (Entry, error) {
	if e.ProjectID == "" || e.SourceID == "" || e.Title == "" {
		return Entry{}, fmt.Errorf("漏洞条目缺 project_id / source_id / title")
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	key := keyOf(e.ProjectID, e.SourceID)
	now := time.Now()
	if e.UpdatedAt.IsZero() {
		e.UpdatedAt = now
	}
	if old, ok := l.byKey[key]; ok {
		e.ID = old.ID
		e.CreatedAt = old.CreatedAt
		e.Orphaned = old.Orphaned
		if e.CreatedAt.IsZero() {
			e.CreatedAt = now
		}
		*old = e
	} else {
		e.ID = newID()
		if e.CreatedAt.IsZero() {
			e.CreatedAt = now
		}
		cp := e
		l.byKey[key] = &cp
		l.order = append(l.order, key)
	}
	if err := l.flush(); err != nil {
		return e, err
	}
	return e, nil
}

// KeepEvidence copies the named files (paths relative to caseRoot) into the
// library and returns their library-relative paths plus the ones it refused to
// copy. Files that do not exist are listed as dropped, not silently ignored.
//
// A preserved evidence URI (.evidence/objects/<sha256>) is copied through the
// evidence package's digest check: bytes that no longer hash to the address are
// refused and reported as dropped, so the library cannot outlive the project
// while carrying altered proof. Legacy working-path URIs recorded no digest and
// are copied as before — the documented old-row limitation.
func (l *Library) KeepEvidence(projectID, caseRoot string, uris []string) (kept, dropped []string) {
	for _, uri := range uris {
		if strings.TrimSpace(uri) == "" {
			continue
		}
		src := filepath.Join(caseRoot, filepath.FromSlash(uri))
		info, err := os.Stat(src)
		if err != nil || info.IsDir() {
			dropped = append(dropped, uri)
			continue
		}
		if info.Size() > MaxEvidenceCopy {
			dropped = append(dropped, uri)
			continue
		}
		rel := filepath.ToSlash(filepath.Join(EvidenceDir, projectID, filepath.FromSlash(uri)))
		dst := filepath.Join(l.root, filepath.FromSlash(rel))
		if digest, preserved := evidence.ObjectDigest(uri); preserved {
			// An earlier hook call may have copied this object already; re-hash
			// it so a tampered library copy is rebuilt or dropped, never kept.
			if got, err := fileDigest(dst); err != nil || got != digest {
				if err := evidence.CopyTo(caseRoot, model.Evidence{URI: uri, Digest: digest}, dst); err != nil {
					_ = os.Remove(dst)
					dropped = append(dropped, uri)
					continue
				}
			}
			kept = append(kept, rel)
			continue
		}
		if _, err := os.Stat(dst); os.IsNotExist(err) {
			if err := copyFile(src, dst); err != nil {
				dropped = append(dropped, uri)
				continue
			}
		}
		kept = append(kept, rel)
	}
	return kept, dropped
}

// fileDigest hashes an already-copied library file. It is a local helper: the
// library copy is evidence of a project fact, so it must be equivalent to the
// preserved object before the row says "kept".
func fileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// MarkOrphaned flags every entry of a deleted project. The rows stay.
func (l *Library) MarkOrphaned(projectID string) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, key := range l.order {
		e := l.byKey[key]
		if e.ProjectID != projectID || e.Orphaned {
			continue
		}
		e.Orphaned = true
		e.UpdatedAt = time.Now()
		n++
	}
	if n == 0 {
		return 0, nil
	}
	return n, l.flush()
}

// List returns entries newest first, optionally filtered by project and by a
// case-insensitive substring of the title, summary and doc.
func (l *Library) List(projectID, q string, limit int) []Entry {
	l.mu.Lock()
	out := make([]Entry, 0, len(l.order))
	for _, key := range l.order {
		e := *l.byKey[key]
		if !keep(e, projectID, q) {
			continue
		}
		out = append(out, e)
	}
	l.mu.Unlock()

	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Count returns how many entries List would keep for the same filters, ignoring
// the limit. The console reports "matched" next to a paged list, so deriving
// the number from a limited page would undercount on every page but a full one.
func (l *Library) Count(projectID, q string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, key := range l.order {
		if keep(*l.byKey[key], projectID, q) {
			n++
		}
	}
	return n
}

// keep is the single filter predicate shared by List and Count. If the two had
// their own copies, a filter change to one would silently make the displayed
// count disagree with the rows.
func keep(e Entry, projectID, q string) bool {
	if projectID != "" && e.ProjectID != projectID {
		return false
	}
	if q != "" && !match(e, q) {
		return false
	}
	return true
}

func match(e Entry, q string) bool {
	q = strings.ToLower(q)
	return strings.Contains(strings.ToLower(e.Title), q) ||
		strings.Contains(strings.ToLower(e.Summary), q) ||
		strings.Contains(strings.ToLower(e.Doc), q)
}

func (l *Library) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.order)
}

// EvidencePath resolves a library-relative evidence path (as stored in Entry,
// e.g. evidence/<project>/<file>) and refuses anything outside the evidence
// directory.
func (l *Library) EvidencePath(rel string) (string, bool) {
	base := filepath.Join(l.root, EvidenceDir)
	abs := filepath.Join(l.root, filepath.FromSlash(rel))
	if !strings.HasPrefix(abs, base+string(os.PathSeparator)) {
		return "", false
	}
	if _, err := os.Stat(abs); err != nil {
		return "", false
	}
	return abs, true
}

func (l *Library) flush() error {
	entries := make([]Entry, 0, len(l.order))
	for _, key := range l.order {
		entries = append(entries, *l.byKey[key])
	}
	b, err := json.MarshalIndent(file{Version: 1, Vulns: entries}, "", "  ")
	if err != nil {
		return err
	}
	tmp := l.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, l.path)
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func newID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return "gv_" + hex.EncodeToString(b[:])
}
