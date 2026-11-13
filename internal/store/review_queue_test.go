package store

import (
	"path/filepath"
	"sort"
	"testing"

	"github.com/kksty/tianwu/internal/model"
)

func openReviewQueueStore(t *testing.T, path string) *Store {
	t.Helper()
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func addReviewFact(t *testing.T, st *Store, intentID, kind, status, title string) model.Fact {
	t.Helper()
	f, err := st.AddFact(model.Fact{IntentID: intentID, Kind: kind, Status: status, Title: title})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func candidateIDs(facts []model.Fact) []string {
	out := make([]string, len(facts))
	for i, f := range facts {
		out[i] = f.ID
	}
	return out
}

// The queue is an enumeration over every eligible finding, in stable ID order,
// and the cursor walks the whole ring. Nothing is excluded because it is old:
// "newest 50" was the starvation bug (F11).
func TestReviewCandidatesWalksEveryEligibleFindingWithWrap(t *testing.T) {
	st := openReviewQueueStore(t, filepath.Join(t.TempDir(), "ledger.json"))
	var eligible []string
	for i := 0; i < 5; i++ {
		eligible = append(eligible, addReviewFact(t, st, "in_1", "vuln", "unverified", "eligible").ID)
	}
	// Non-candidates: a resolved vuln, an observation and a rejected credential.
	addReviewFact(t, st, "in_1", "vuln", "verified", "已复核")
	addReviewFact(t, st, "in_1", "host", "unverified", "主机观察")
	addReviewFact(t, st, "in_1", "cred", "rejected", "已否定")
	// Another intent's finding is not this intent's queue.
	addReviewFact(t, st, "in_2", "vuln", "unverified", "别的方向")
	sort.Strings(eligible)

	cursor := ""
	var firstLap []string
	for len(firstLap) < len(eligible) {
		batch, next := st.ReviewCandidates("in_1", cursor, 2)
		if len(batch) == 0 {
			t.Fatalf("walk stopped with %d/%d candidates seen (cursor %q)", len(firstLap), len(eligible), cursor)
		}
		if next == "" {
			t.Fatalf("a non-empty batch reported an empty next cursor")
		}
		for _, f := range batch {
			if f.Status != "unverified" || (f.Kind != "vuln" && f.Kind != "cred") || f.IntentID != "in_1" {
				t.Fatalf("a non-candidate was returned: %+v", f)
			}
			firstLap = append(firstLap, f.ID)
		}
		cursor = next
	}
	for i := range eligible {
		if firstLap[i] != eligible[i] {
			t.Fatalf("walk order = %v, want stable ID order %v", firstLap, eligible)
		}
	}
	// The tail batch was short; the cursor is the last candidate handled, and a
	// following walk wraps to the ring head instead of stopping.
	if cursor != eligible[len(eligible)-1] {
		t.Fatalf("cursor after the tail = %q, want the last candidate %q", cursor, eligible[len(eligible)-1])
	}
	second, next := st.ReviewCandidates("in_1", cursor, 2)
	if got := candidateIDs(second); len(got) != 2 || got[0] != eligible[0] || got[1] != eligible[1] {
		t.Fatalf("second lap = %v, want the wrapped head %v", got, eligible[:2])
	}
	if next != eligible[1] {
		t.Fatalf("second lap next = %q, want %q", next, eligible[1])
	}
}

// A limit larger than the set returns the whole ring once, never a duplicate,
// and points the next walk at the candidate it started from.
func TestReviewCandidatesClampsTheBatchAndNeverDuplicates(t *testing.T) {
	st := openReviewQueueStore(t, filepath.Join(t.TempDir(), "ledger.json"))
	first := addReviewFact(t, st, "in_1", "vuln", "unverified", "一")
	second := addReviewFact(t, st, "in_1", "cred", "unverified", "二")
	want := []string{first.ID, second.ID}
	sort.Strings(want)

	batch, next := st.ReviewCandidates("in_1", "", 50)
	if got := candidateIDs(batch); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("batch = %v, want %v", got, want)
	}
	if next != want[1] {
		t.Fatalf("next = %q, want the last handled candidate %q", next, want[1])
	}

	// A single candidate stays reachable: a walk after it wraps to it again, and
	// the caller relies on its own cooldown to avoid an immediate second run.
	other := openReviewQueueStore(t, filepath.Join(t.TempDir(), "ledger.json"))
	only := addReviewFact(t, other, "in_1", "vuln", "unverified", "唯一")
	batch, next = other.ReviewCandidates("in_1", "", 8)
	if len(batch) != 1 || batch[0].ID != only.ID || next != only.ID {
		t.Fatalf("single-candidate ring = %v next=%q, want the one candidate", candidateIDs(batch), next)
	}
	batch, next = other.ReviewCandidates("in_1", next, 8)
	if len(batch) != 1 || batch[0].ID != only.ID || next != only.ID {
		t.Fatalf("single-candidate wrap = %v next=%q, want the one candidate again", candidateIDs(batch), next)
	}
}

// Eligibility is status-based, so a claim correction (which returns a verified
// fact to unverified) puts it back in the queue without separate bookkeeping.
func TestReviewCandidatesReopensAfterAClaimChange(t *testing.T) {
	st := openReviewQueueStore(t, filepath.Join(t.TempDir(), "ledger.json"))
	f := addReviewFact(t, st, "in_1", "vuln", "unverified", "会被复核")
	if _, err := st.ReviewFact(model.Review{FactID: f.ID, Verdict: "pass", Notes: "复现"}); err != nil {
		t.Fatal(err)
	}
	if batch, _ := st.ReviewCandidates("in_1", "", 8); len(batch) != 0 {
		t.Fatalf("verified finding stayed in the queue: %+v", batch)
	}
	if _, err := st.UpdateFact(f.ID, func(r *model.Fact) { r.Summary = "更正后的断言" }); err != nil {
		t.Fatal(err)
	}
	batch, _ := st.ReviewCandidates("in_1", "", 8)
	if len(batch) != 1 || batch[0].ID != f.ID || batch[0].ClaimVersion != f.ClaimVersion+1 {
		t.Fatalf("a corrected claim did not return to the queue: %+v", batch)
	}
}

// The resume point is ledger memory, not process state: a reopened store (the
// restart case) continues where the last pass stopped.
func TestReviewCursorSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.json")
	st := openReviewQueueStore(t, path)
	if got := st.ReviewCursor("in_1"); got != "" {
		t.Fatalf("fresh store cursor = %q, want empty", got)
	}
	if err := st.SetReviewCursor("in_1", "f_after_this"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetReviewCursor("in_2", "f_other"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openReviewQueueStore(t, path)
	t.Cleanup(func() { _ = reopened.Close() })
	if got := reopened.ReviewCursor("in_1"); got != "f_after_this" {
		t.Fatalf("cursor after reopen = %q, want f_after_this", got)
	}
	if got := reopened.ReviewCursor("in_2"); got != "f_other" {
		t.Fatalf("second intent cursor after reopen = %q, want f_other", got)
	}
	// An empty value clears the entry instead of pinning an empty cursor that
	// would read as "restart from the top".
	if err := reopened.SetReviewCursor("in_1", ""); err != nil {
		t.Fatal(err)
	}
	if got := reopened.ReviewCursor("in_1"); got != "" {
		t.Fatalf("cleared cursor = %q", got)
	}
}

// The cursor is bookkeeping: moving it must not wake the decider's change
// counter or change the content digest.
func TestReviewCursorDoesNotChangeTheLedgerContent(t *testing.T) {
	st := openReviewQueueStore(t, filepath.Join(t.TempDir(), "ledger.json"))
	addReviewFact(t, st, "in_1", "vuln", "unverified", "一条")
	before := st.ContentHash()
	if err := st.SetReviewCursor("in_1", "f_x"); err != nil {
		t.Fatal(err)
	}
	if got := st.ContentHash(); got != before {
		t.Fatalf("cursor move changed the content digest: %s -> %s", before, got)
	}
}
