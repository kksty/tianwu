package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/kksty/tianwu/internal/model"
	_ "modernc.org/sqlite"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type sqliteBackend struct{ db *sql.DB }
type sqliteMeta struct {
	Version    int     `json:"version"`
	MessageSeq uint64  `json:"message_seq"`
	Memory     *Memory `json:"memory"`
}

// OpenSQLite opens the canonical ledger, not a second writable index beside a
// JSON ledger. Only the current SQLite format is supported.
func OpenSQLite(path string) (*Store, error) {
	info, statErr := os.Stat(path)
	fresh := os.IsNotExist(statErr)
	if statErr != nil && !fresh {
		return nil, statErr
	}
	if !fresh && info.Size() == 0 {
		return nil, fmt.Errorf("empty SQLite ledger: %s", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	p := filepath.ToSlash(abs)
	if filepath.VolumeName(abs) != "" {
		p = "/" + p
	}
	u := url.URL{Scheme: "file", Path: p}
	params := url.Values{}
	params.Add("_pragma", "busy_timeout(5000)")
	params.Add("_pragma", "journal_mode(WAL)")
	params.Add("_pragma", "synchronous(FULL)")
	u.RawQuery = params.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	// No idle OS handle: offline project removal and Windows directory cleanup
	// must not depend on the garbage collector. Transactions keep one connection.
	db.SetMaxIdleConns(0)
	fail := func(e error) (*Store, error) { _ = db.Close(); return nil, e }
	if fresh {
		for _, stmt := range []string{
			`CREATE TABLE meta (id INTEGER PRIMARY KEY CHECK(id=1), payload BLOB NOT NULL)`,
			`CREATE TABLE objects (kind TEXT NOT NULL,id TEXT NOT NULL,position INTEGER NOT NULL,version INTEGER NOT NULL,seq INTEGER NOT NULL,payload BLOB NOT NULL,PRIMARY KEY(kind,id))`,
			`CREATE INDEX objects_order ON objects(kind,position,id)`,
			`CREATE TABLE versions (seq INTEGER PRIMARY KEY,kind TEXT NOT NULL,id TEXT NOT NULL,version INTEGER NOT NULL,payload BLOB NOT NULL)`,
			`CREATE INDEX versions_object ON versions(kind,id,seq)`,
			`CREATE VIRTUAL TABLE fact_search USING fts5(id UNINDEXED,title,summary,doc,tokenize='trigram')`,
			`CREATE TABLE vectors (seq INTEGER PRIMARY KEY AUTOINCREMENT,model TEXT NOT NULL,hash TEXT NOT NULL,vector BLOB NOT NULL,UNIQUE(model,hash))`,
			`CREATE TABLE relations (owner TEXT NOT NULL,source TEXT NOT NULL,target TEXT NOT NULL,relation TEXT NOT NULL,PRIMARY KEY(owner,source,target,relation))`,
			`CREATE INDEX relations_source ON relations(source)`, `CREATE INDEX relations_target ON relations(target)`,
			`PRAGMA user_version=1`,
		} {
			if _, err = db.Exec(stmt); err != nil {
				return fail(err)
			}
		}
		mem := &Memory{Epoch: NewID("ledger"), Checkpoints: map[string]Checkpoint{}}
		mem.ContentHash = contentHash(nil)
		raw, _ := json.Marshal(sqliteMeta{schemaVersion, 0, mem})
		if _, err = db.Exec("INSERT INTO meta VALUES(1,?)", raw); err != nil {
			return fail(err)
		}
	}
	var format int
	if err = db.QueryRow("PRAGMA user_version").Scan(&format); err != nil || format != 1 {
		return fail(fmt.Errorf("unsupported SQLite ledger format %d: %v", format, err))
	}
	s := &Store{path: path, sqlite: &sqliteBackend{db: db}}
	if err = s.loadSQLite(); err != nil {
		return fail(err)
	}
	return s, nil
}

func (s *Store) loadSQLite() error {
	var raw []byte
	if err := s.sqlite.db.QueryRow("SELECT payload FROM meta WHERE id=1").Scan(&raw); err != nil {
		return err
	}
	var meta sqliteMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return err
	}
	if meta.Version != schemaVersion || meta.Memory == nil {
		return fmt.Errorf("unsupported ledger schema %d (expected %d; use a new data directory; migration is not supported)", meta.Version, schemaVersion)
	}
	db := DB{Version: meta.Version, MessageSeq: meta.MessageSeq, Memory: meta.Memory}
	rows, err := s.sqlite.db.Query("SELECT kind,payload FROM objects ORDER BY kind,position,id")
	if err != nil {
		return err
	}
	for rows.Next() {
		var kind string
		var value []byte
		if err = rows.Scan(&kind, &value); err != nil {
			break
		}
		if err = appendRow(&db, kind, value); err != nil {
			break
		}
	}
	rowErr := rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	if rowErr != nil {
		return rowErr
	}
	rows, err = s.sqlite.db.Query("SELECT payload FROM versions ORDER BY seq")
	if err != nil {
		return err
	}
	for rows.Next() {
		var value []byte
		if err = rows.Scan(&value); err != nil {
			break
		}
		var c Change
		if err = json.Unmarshal(value, &c); err != nil {
			break
		}
		db.Memory.History = append(db.Memory.History, c)
	}
	rowErr = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	if rowErr != nil {
		return rowErr
	}
	s.db = db
	s.readEpoch = db.Memory.Epoch
	s.readCacheMu.Lock()
	s.readCache, s.readCacheOrder, s.readCacheBytes = nil, nil, 0
	s.readCacheMu.Unlock()
	s.initRecords()
	return s.validateMemory()
}

func (s *Store) flushSQLite(kinds ...string) (err error) {
	if s.records == nil {
		s.initRecords()
	}
	previousRev := s.committedRev
	defer func() {
		if err != nil {
			s.rev = previousRev
			if reloadErr := s.loadSQLite(); reloadErr != nil {
				err = fmt.Errorf("%w; reload committed ledger: %v", err, reloadErr)
			}
		}
	}()
	next, changes := s.prepareMemory(kinds...)
	tx, err := s.sqlite.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, c := range changes {
		if err = writeSQLiteRow(tx, c); err != nil {
			return err
		}
	}
	mem := *s.db.Memory
	mem.History = nil
	raw, err := json.Marshal(sqliteMeta{s.db.Version, s.db.MessageSeq, &mem})
	if err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE meta SET payload=? WHERE id=1", raw); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	for _, r := range next {
		s.putRecord(r)
	}
	for _, c := range changes {
		if c.Operation == "delete" {
			key := recordKey(c.Kind, c.ID)
			delete(s.records, key)
			delete(s.recordKinds[c.Kind], key)
		}
	}
	s.committedRev = s.rev
	return nil
}

func writeSQLiteRow(tx *sql.Tx, c Change) error {
	var factRowID int64
	if c.Kind == "fact" && c.Operation == "delete" {
		if err := tx.QueryRow("SELECT rowid FROM objects WHERE kind='fact' AND id=?", c.ID).Scan(&factRowID); err != nil && err != sql.ErrNoRows {
			return err
		}
	}
	if c.Operation == "delete" {
		if _, err := tx.Exec("DELETE FROM objects WHERE kind=? AND id=?", c.Kind, c.ID); err != nil {
			return err
		}
	} else {
		if _, err := tx.Exec(`INSERT INTO objects VALUES(?,?,?,?,?,?) ON CONFLICT(kind,id) DO UPDATE SET position=excluded.position,version=excluded.version,seq=excluded.seq,payload=excluded.payload`, c.Kind, c.ID, c.Position, c.Version, c.Seq, []byte(c.Value)); err != nil {
			return err
		}
	}
	if newsKind(c.Kind) && c.Operation != "move" {
		raw, _ := json.Marshal(c)
		if _, err := tx.Exec("INSERT INTO versions VALUES(?,?,?,?,?)", c.Seq, c.Kind, c.ID, c.Version, raw); err != nil {
			return err
		}
	}
	owner := recordKey(c.Kind, c.ID)
	if _, err := tx.Exec("DELETE FROM relations WHERE owner=?", owner); err != nil {
		return err
	}
	link := func(source, target, kind string) error {
		_, err := tx.Exec("INSERT OR IGNORE INTO relations VALUES(?,?,?,?)", owner, source, target, kind)
		return err
	}
	if c.Kind == "fact" {
		if c.Operation != "delete" {
			if err := tx.QueryRow("SELECT rowid FROM objects WHERE kind='fact' AND id=?", c.ID).Scan(&factRowID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec("DELETE FROM fact_search WHERE rowid=?", factRowID); err != nil {
			return err
		}
		if c.Operation != "delete" {
			var f model.Fact
			if err := json.Unmarshal(c.Value, &f); err != nil {
				return err
			}
			if _, err := tx.Exec("INSERT INTO fact_search(rowid,id,title,summary,doc) VALUES(?,?,?,?,?)", factRowID, f.ID, f.Title, f.Summary, f.Doc); err != nil {
				return err
			}
			if f.IntentID != "" {
				if err := link("intent:"+f.IntentID, owner, "produces"); err != nil {
					return err
				}
			}
			for _, id := range f.EvidenceIDs {
				if err := link(owner, "evidence:"+id, "evidence"); err != nil {
					return err
				}
			}
		}
	}
	if c.Kind == "intent" && c.Operation != "delete" {
		var in model.Intent
		if err := json.Unmarshal(c.Value, &in); err != nil {
			return err
		}
		for _, id := range in.FromFactIDs {
			if err := link("fact:"+id, owner, "premise"); err != nil {
				return err
			}
		}
		for _, id := range in.ContextFactIDs {
			if err := link("fact:"+id, owner, "reference"); err != nil {
				return err
			}
		}
		// Pins are recorded focus, not premises or references, so they get their
		// own relation kind. The same fact may appear in both ContextFactIDs and
		// PinnedFactIDs; the (owner,source,target,relation) primary key keeps the
		// reference and pin rows side by side instead of one replacing the other.
		//
		// relations is a derived index rebuilt per written owner: a ledger
		// indexed before pin support picks these rows up when the intent is next
		// written (or through the explicit `ledger -reindex` maintenance
		// command). The startup path deliberately does not scan history, so do
		// not turn that into a migration here.
		for _, id := range in.PinnedFactIDs {
			if err := link("fact:"+id, owner, "pin"); err != nil {
				return err
			}
		}
		if in.ParentID != "" {
			if err := link("intent:"+in.ParentID, owner, "delegates"); err != nil {
				return err
			}
		}
	}
	if c.Kind == "goal" && c.Operation != "delete" {
		var g model.Goal
		if err := json.Unmarshal(c.Value, &g); err != nil {
			return err
		}
		for _, id := range g.From {
			if err := link("fact:"+id, owner, "supports"); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) Close() error {
	if s.sqlite != nil {
		return s.sqlite.db.Close()
	}
	return nil
}

// FullTextCandidates uses the same trigram index for mixed Chinese/English
// text. Short terms are intentionally handled by the complete lexical path.
func (s *Store) FullTextCandidates(q string) (map[string]float64, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.fullTextCandidates(q)
}
func (s *Store) FullTextCandidatesAt(q string, v ReadVersion) (map[string]float64, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db.Memory == nil || v.Epoch != s.db.Memory.Epoch || v.Seq != s.db.Memory.Seq {
		return nil, false, nil
	}
	return s.fullTextCandidates(q)
}
func (s *Store) fullTextCandidates(q string) (map[string]float64, bool, error) {
	if s.sqlite == nil {
		return nil, false, nil
	}
	terms := strings.Fields(q)
	quoted := []string{}
	for _, t := range terms {
		if len([]rune(t)) < 3 {
			return nil, false, nil
		}
		quoted = append(quoted, "\""+strings.ReplaceAll(t, "\"", "\"\"")+"\"")
	}
	if len(quoted) == 0 {
		return nil, false, nil
	}
	rows, err := s.sqlite.db.Query("SELECT id,bm25(fact_search,0,8,3,1) FROM fact_search WHERE fact_search MATCH ?", strings.Join(quoted, " AND "))
	if err != nil {
		return nil, true, err
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var id string
		var score float64
		if err = rows.Scan(&id, &score); err != nil {
			return nil, true, err
		}
		out[id] = score
	}
	return out, true, rows.Err()
}
