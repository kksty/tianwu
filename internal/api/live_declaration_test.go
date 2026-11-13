package api

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/ledger"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
	"github.com/kksty/tianwu/internal/verifier"
)

// F8: one identity-based upsert is shared by the live watcher and the finish
// settlement. These tests drive the watcher and the real Ingestor, not just the
// helper, so the cursor and notification contracts are covered too.

func newLiveDeclarationServer(t *testing.T) (*Server, *store.Store, string, model.Intent, model.Run) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.json")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cases := filepath.Join(dir, "cases")
	s := New(st, nil, dir, cases)
	in, err := st.CreateIntent(model.Intent{Title: "边做边记"})
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(cases, in.ID)
	if err := os.MkdirAll(filepath.Join(work, ledger.HandoffDir), 0o700); err != nil {
		t.Fatal(err)
	}
	start := int64(0)
	run, err := st.AddRun(model.Run{IntentID: in.ID, Node: "local", DeclarationStart: &start})
	if err != nil {
		t.Fatal(err)
	}
	return s, st, work, in, run
}

func appendHandoff(t *testing.T, work, line string) {
	t.Helper()
	f, err := os.OpenFile(ledger.HandoffPath(work), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(line + "\n"); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func findFactByTitle(t *testing.T, st *store.Store, intentID, title string) model.Fact {
	t.Helper()
	for _, f := range st.ListFacts(intentID, 0) {
		if f.Title == title {
			return f
		}
	}
	t.Fatalf("fact %q not found in %+v", title, st.ListFacts(intentID, 0))
	return model.Fact{}
}

// The writer re-declares the same observation with a corrected summary. Both the
// mid-run view and the finish settlement (which replays the whole file segment)
// must show the correction: the later line supersedes, and a live row is
// updated, never duplicated.
func TestLiveDeclarationCorrectionIsVisibleImmediatelyAndSurvivesFinish(t *testing.T) {
	s, st, work, in, run := newLiveDeclarationServer(t)
	appendHandoff(t, work, `{"kind":"vuln","title":"同一观察","declaration_id":"obs-1","summary":"第一次"}`)
	appendHandoff(t, work, `{"kind":"vuln","title":"同一观察","declaration_id":"obs-1","summary":"更正后的结论"}`)

	stop := s.watchFacts(context.Background(), in.ID, work, run.ID)
	defer stop()
	waitFor(t, 5*time.Second, func() bool {
		facts := st.ListFacts(in.ID, 10)
		return len(facts) == 1 && facts[0].Summary == "更正后的结论"
	})
	facts := st.ListFacts(in.ID, 10)
	if len(facts) != 1 {
		t.Fatalf("one observation became %d rows: %+v", len(facts), facts)
	}
	liveID, liveVersion := facts[0].ID, facts[0].ClaimVersion
	if liveVersion != 1 {
		t.Fatalf("a batch with one final declaration produced v%d", liveVersion)
	}

	// The finish still reads from the persisted cursor (the watcher never
	// commits it), so it replays both lines. The superseding line must be the
	// one applied: the correction is not reverted by the settlement.
	stop()
	if n := s.Ingest.OnFinish(run.ID, in.ID, s.CaseRoot, work, time.Now().Add(time.Hour)); n != 1 {
		t.Fatalf("declared = %d, want one observation", n)
	}
	settled, ok := st.FactByID(liveID)
	if !ok {
		t.Fatal("finish replaced the live row instead of updating it")
	}
	if settled.Summary != "更正后的结论" {
		t.Fatalf("finish reverted the live correction: %q", settled.Summary)
	}
	// The replayed declaration was identical to the settled state, so the
	// duplicate read did not start another assertion version.
	if settled.ClaimVersion != liveVersion {
		t.Fatalf("duplicate read bumped the claim version v%d -> v%d", liveVersion, settled.ClaimVersion)
	}
	if rows := st.ListFacts(in.ID, 10); len(rows) != 1 {
		t.Fatalf("finish duplicated the live row: %+v", rows)
	}
}

// A failed live commit must not advance the consumed cursor: the next pass
// re-reads the same range, and rows already committed by the failed pass dedupe
// without a claim-version bump.
func TestFailedLiveDeclarationKeepsTheCursorAndRetries(t *testing.T) {
	s, st, work, in, run := newLiveDeclarationServer(t)
	path := filepath.Join(filepath.Dir(s.CaseRoot), "ledger.json")
	appendHandoff(t, work, `{"kind":"note","title":"已提交","declaration_id":"obs-a"}`)
	appendHandoff(t, work, `{"kind":"note","title":"会被阻断","declaration_id":"obs-b"}`)

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// The first fact commits; every later fact insert fails. The second line is
	// therefore a real failure after a committed row.
	if _, err := db.Exec("CREATE TRIGGER fail_later_fact BEFORE INSERT ON objects WHEN NEW.kind='fact' AND (SELECT COUNT(*) FROM objects WHERE kind='fact') >= 1 BEGIN SELECT RAISE(ABORT,'fixture failure'); END"); err != nil {
		t.Fatal(err)
	}

	offset, err := s.readLiveDeclarations(in.ID, work, run.ID, 0)
	if err == nil {
		t.Fatal("a failing declaration was reported as consumed")
	}
	if offset != 0 {
		t.Fatalf("cursor advanced to %d after a failed apply", offset)
	}
	first := findFactByTitle(t, st, in.ID, "已提交")
	if first.ClaimVersion != 1 {
		t.Fatalf("committed row version = v%d, want v1", first.ClaimVersion)
	}
	if len(st.ListFacts(in.ID, 0)) != 1 {
		t.Fatalf("failed line was published: %+v", st.ListFacts(in.ID, 0))
	}

	if _, err := db.Exec("DROP TRIGGER fail_later_fact"); err != nil {
		t.Fatal(err)
	}
	offset, err = s.readLiveDeclarations(in.ID, work, run.ID, offset)
	if err != nil {
		t.Fatalf("retry of the same range failed: %v", err)
	}
	if offset == 0 {
		t.Fatal("retry did not advance past the lines it committed")
	}
	again := findFactByTitle(t, st, in.ID, "已提交")
	if again.ID != first.ID || again.ClaimVersion != 1 {
		t.Fatalf("retry changed the already-committed row: %+v (was %s v%d)", again, first.ID, first.ClaimVersion)
	}
	findFactByTitle(t, st, in.ID, "会被阻断")
}

// A mid-run correction is a new assertion version, so a review that started
// against the old version must be refused (ErrReviewConflict) instead of letting
// the old proof bless the corrected content.
func TestLiveCorrectionInvalidatesAnInflightReview(t *testing.T) {
	s, st, work, in, run := newLiveDeclarationServer(t)
	appendHandoff(t, work, `{"kind":"vuln","title":"会被复核的结论","declaration_id":"obs-r","summary":"旧断言"}`)
	stop := s.watchFacts(context.Background(), in.ID, work, run.ID)
	waitFor(t, 5*time.Second, func() bool { return len(st.ListFacts(in.ID, 10)) == 1 })
	fact := findFactByTitle(t, st, in.ID, "会被复核的结论")
	if fact.Status != "unverified" {
		t.Fatalf("fresh live fact status = %q", fact.Status)
	}

	ex := &blockingVerdictExec{entered: make(chan struct{}, 1), release: make(chan struct{})}
	s.Verifier = verifier.New(st, ex, s.CaseRoot)
	type outcome struct {
		review model.Review
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		r, err := s.Verifier.Run(s.Ctx, fact.ID)
		done <- outcome{r, err}
	}()
	select {
	case <-ex.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("review never started")
	}

	// Correct the observation while the review is proving the old assertion.
	appendHandoff(t, work, `{"kind":"vuln","title":"会被复核的结论","declaration_id":"obs-r","summary":"更正后的断言"}`)
	stop() // final live read applies the correction
	if got := findFactByTitle(t, st, in.ID, "会被复核的结论"); got.ClaimVersion != 2 {
		t.Fatalf("live correction version = v%d, want v2", got.ClaimVersion)
	}
	close(ex.release)

	select {
	case out := <-done:
		if !errors.Is(out.err, store.ErrReviewConflict) {
			t.Fatalf("stale review error = %v, want ErrReviewConflict", out.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("review did not finish")
	}
	got, _ := st.FactByID(fact.ID)
	if got.Status != "unverified" || got.Summary != "更正后的断言" {
		t.Fatalf("stale verdict was applied: %+v", got)
	}
}

// The finish path owns evidence. A live row without evidence must gain the
// preserved ids at settle while keeping the live content, on the same row.
func TestFinishFillsEvidenceWithoutLosingTheLiveDeclaration(t *testing.T) {
	s, st, work, in, run := newLiveDeclarationServer(t)
	if err := os.WriteFile(filepath.Join(work, "proof.txt"), []byte("PROOF-BYTES"), 0o644); err != nil {
		t.Fatal(err)
	}
	appendHandoff(t, work, `{"kind":"vuln","title":"带证据的结论","declaration_id":"obs-e","summary":"运行中写下的结论","evidence":["proof.txt"]}`)

	stop := s.watchFacts(context.Background(), in.ID, work, run.ID)
	waitFor(t, 5*time.Second, func() bool { return len(st.ListFacts(in.ID, 10)) == 1 })
	live := findFactByTitle(t, st, in.ID, "带证据的结论")
	if len(live.EvidenceIDs) != 0 {
		t.Fatalf("the live path attached evidence before settlement: %v", live.EvidenceIDs)
	}
	stop()
	if n := s.Ingest.OnFinish(run.ID, in.ID, s.CaseRoot, work, time.Now().Add(time.Hour)); n != 1 {
		t.Fatalf("declared = %d, want 1", n)
	}
	final := findFactByTitle(t, st, in.ID, "带证据的结论")
	if final.ID != live.ID {
		t.Fatalf("settlement created a second row: %s -> %s", live.ID, final.ID)
	}
	if final.Summary != "运行中写下的结论" {
		t.Fatalf("settlement lost the live content: %q", final.Summary)
	}
	if len(final.EvidenceIDs) != 1 {
		t.Fatalf("settlement did not attach evidence: %v", final.EvidenceIDs)
	}
	if e, ok := st.EvidenceByID(final.EvidenceIDs[0]); !ok || e.Digest == "" {
		t.Fatalf("attached evidence row is not the preserved object: %+v", e)
	}
}

// blockingVerdictExec stands in for a review that is still proving its claim
// when the live declaration corrects it.
type blockingVerdictExec struct {
	entered chan struct{}
	release chan struct{}
}

func (e *blockingVerdictExec) Name() string { return "blocking-verdict" }

func (e *blockingVerdictExec) Run(ctx context.Context, req executor.Request, emit func(executor.Event)) (executor.Result, error) {
	select {
	case e.entered <- struct{}{}:
	default:
	}
	select {
	case <-e.release:
	case <-ctx.Done():
		return executor.Result{ExitCode: 1}, ctx.Err()
	}
	if err := os.WriteFile(filepath.Join(req.WorkDir, verifier.VerdictPath), []byte(`{"verdict":"pass","notes":"复现了旧断言"}`), 0o644); err != nil {
		return executor.Result{ExitCode: 1}, err
	}
	return executor.Result{ExitCode: 0}, nil
}
