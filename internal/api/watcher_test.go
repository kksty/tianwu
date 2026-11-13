package api

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/decider"
	"github.com/kksty/tianwu/internal/ledger"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// The live watcher adds the same row the finish path later settles. A summary
// clipped on the way in would make the early board and the console show a half
// sentence; the declared text must survive the live path too (D114).
func TestLiveWatcherStoresTheSummaryInFull(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, nil, dir, filepath.Join(dir, "cases"))
	caseRoot := filepath.Join(dir, "cases")
	work := filepath.Join(caseRoot, "in_1")
	if err := os.MkdirAll(filepath.Join(work, ledger.HandoffDir), 0o755); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("被动源与字典枚举，新增 host:port。", 120) // 1440 runes
	line := `{"kind":"note","title":"长摘要","summary":"` + long + `"}` + "\n"
	if err := os.WriteFile(ledger.HandoffPath(work), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	stop := s.watchFacts(context.Background(), "in_1", work)
	stop()

	facts := st.ListFacts("in_1", 0)
	if len(facts) != 1 {
		t.Fatalf("facts = %d, want 1", len(facts))
	}
	if got := facts[0].Summary; got != long {
		t.Fatalf("live summary was clipped: stored %d runes, declared %d", len([]rune(got)), len([]rune(long)))
	}
}

func TestNewLiveFactWakesCommanderBeforeWatcherStops(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	in, err := st.CreateIntent(model.Intent{Title: "open"})
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, nil, dir, filepath.Join(dir, "cases"))
	work := filepath.Join(s.CaseRoot, in.ID)
	if err := os.MkdirAll(filepath.Join(work, ledger.HandoffDir), 0700); err != nil {
		t.Fatal(err)
	}
	r := decider.NewRunner(st, nil, "")
	r.ModelName, r.Debounce, r.Heartbeat = "fixture", time.Millisecond, time.Hour
	called := make(chan int, 2)
	r.Commander = func(context.Context, string, bool) (decider.RoundResult, error) {
		called <- len(st.ListFacts(in.ID, 0))
		return decider.RoundResult{}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Decider = r
	r.Start(ctx)
	stop := s.watchFacts(ctx, in.ID, work)
	defer stop()
	if err := os.WriteFile(ledger.HandoffPath(work), []byte("{\"kind\":\"note\",\"title\":\"live\",\"summary\":\"observed\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case n := <-called:
		if n != 1 {
			t.Fatalf("commander saw %d facts", n)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("live declaration did not wake commander")
	}
	stop() // The same row must not trigger a second wake at settle.
	select {
	case <-called:
		t.Fatal("duplicate declaration woke commander")
	case <-time.After(30 * time.Millisecond):
	}
}

func TestFailedLiveFactCommitCanBeRetried(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.json")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, nil, dir, filepath.Join(dir, "cases"))
	work := filepath.Join(s.CaseRoot, "in_1")
	if err := os.MkdirAll(filepath.Join(work, ledger.HandoffDir), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ledger.HandoffPath(work), []byte("{\"kind\":\"note\",\"title\":\"retry\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TRIGGER fail_fact BEFORE INSERT ON objects WHEN NEW.kind='fact' BEGIN SELECT RAISE(ABORT,'fixture failure'); END"); err != nil {
		t.Fatal(err)
	}
	s.watchFacts(context.Background(), "in_1", work)()
	if len(st.ListFacts("", 0)) != 0 {
		t.Fatal("uncommitted fact was published")
	}
	if _, err := db.Exec("DROP TRIGGER fail_fact"); err != nil {
		t.Fatal(err)
	}
	s.watchFacts(context.Background(), "in_1", work)()
	if len(st.ListFacts("", 0)) != 1 {
		t.Fatal("failed declaration was lost")
	}
}

// The live watcher must stop (and settle what it has) before the finish path
// runs: both used to add facts by title, so a declaration written just before
// the run ended could land twice. Stopping is also idempotent — dispatch defers
// the same func it calls explicitly.
func TestWatchFactsStopsAndSettlesTheFinalLines(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, nil, dir, filepath.Join(dir, "cases"))
	caseRoot := filepath.Join(dir, "cases")
	work := filepath.Join(caseRoot, "in_1")
	if err := os.MkdirAll(filepath.Join(work, ledger.HandoffDir), 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"kind":"note","title":"刚写下的一条","summary":"s"}` + "\n"
	if err := os.WriteFile(ledger.HandoffPath(work), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	stop := s.watchFacts(context.Background(), "in_1", work)
	stop()
	stop() // a second call must not deadlock or duplicate

	facts := st.ListFacts("in_1", 0)
	if len(facts) != 1 || facts[0].Title != "刚写下的一条" {
		t.Fatalf("final lines not settled once: %+v", facts)
	}
}
