package store

import (
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/kksty/tianwu/internal/model"
)

// A retried "append a goal and continue" request must find the goal it already
// created, not append a second one, and the lookup must survive a restart: the
// key is durable ledger state, not process memory.
func TestAddGoalIdempotentReturnsTheExistingGoalForRepeatedKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	goal := model.Goal{Statement: "验证 /admin 越权", QuantifiedTarget: "拿到一条可复核证据", IdempotencyKey: "append-1"}
	first, created, err := s.AddGoalIdempotent(goal)
	if err != nil {
		t.Fatal(err)
	}
	if !created || first.ID == "" || first.IdempotencyKey != "append-1" {
		t.Fatalf("first append = %+v created=%v", first, created)
	}
	second, created, err := s.AddGoalIdempotent(goal)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("a repeated key reported a new goal")
	}
	if second.ID != first.ID {
		t.Fatalf("repeated key returned %s, want the original %s", second.ID, first.ID)
	}
	if got := s.Goals(); len(got) != 1 {
		t.Fatalf("repeated key appended a duplicate: %d goals", len(got))
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	afterRestart, created, err := reopened.AddGoalIdempotent(goal)
	if err != nil {
		t.Fatal(err)
	}
	if created || afterRestart.ID != first.ID {
		t.Fatalf("after restart created=%v id=%s, want the persisted goal %s", created, afterRestart.ID, first.ID)
	}
}

// Two concurrent retries are the real duplicate risk: the lookup and the append
// must be one atomic step under the store lock, or both would append.
func TestAddGoalIdempotentConcurrentCallsCreateOneGoal(t *testing.T) {
	s := open(t)
	const calls = 16
	var wg sync.WaitGroup
	ids := make([]string, calls)
	created := make([]bool, calls)
	errs := make([]error, calls)
	start := make(chan struct{})
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			g, c, err := s.AddGoalIdempotent(model.Goal{Statement: "只追加一次", IdempotencyKey: "race-1"})
			ids[i], created[i], errs[i] = g.ID, c, err
		}(i)
	}
	close(start)
	wg.Wait()
	winners := 0
	for i := 0; i < calls; i++ {
		if errs[i] != nil {
			t.Fatalf("call %d: %v", i, errs[i])
		}
		if created[i] {
			winners++
		}
		if ids[i] != ids[0] {
			t.Fatalf("call %d returned %s, call 0 returned %s", i, ids[i], ids[0])
		}
	}
	if winners != 1 {
		t.Fatalf("%d concurrent calls reported created, want exactly 1", winners)
	}
	if got := s.Goals(); len(got) != 1 {
		t.Fatalf("concurrent calls appended %d goals, want 1", len(got))
	}
}

// The key identifies a request, not a slot to be reused for different work:
// returning the first goal for a changed payload would hide the change.
func TestAddGoalIdempotentRejectsChangedPayloadAndDoesNotDedupeEmptyKeys(t *testing.T) {
	s := open(t)
	first, _, err := s.AddGoalIdempotent(model.Goal{Statement: "目标 A", IdempotencyKey: "key-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AddGoalIdempotent(model.Goal{Statement: "目标 B", IdempotencyKey: "key-1"}); !errors.Is(err, ErrGoalIdempotencyConflict) {
		t.Fatalf("changed payload error = %v, want ErrGoalIdempotencyConflict", err)
	}
	if got := s.Goals(); len(got) != 1 || got[0].ID != first.ID {
		t.Fatalf("conflicting retry changed the ledger: %+v", got)
	}
	// The plain create path has no key: it must keep appending, never dedupe by
	// an empty key.
	if _, _, err := s.AddGoalIdempotent(model.Goal{Statement: "无 key 1"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AddGoalIdempotent(model.Goal{Statement: "无 key 2"}); err != nil {
		t.Fatal(err)
	}
	if got := len(s.Goals()); got != 3 {
		t.Fatalf("empty idempotency keys deduped: %d goals, want 3", got)
	}
}

// Appending a new goal is not an edit of an old one: a stamped goal's receipt
// (met/evidence/note/from/versions/completed_at) must come back byte-for-byte as
// it was, including through the idempotent retry path that returns it.
func TestAddGoalIdempotentLeavesStampedReceiptAlone(t *testing.T) {
	s := open(t)
	e, err := s.AddEvidence(model.Evidence{URI: "in_1/proof.txt"})
	if err != nil {
		t.Fatal(err)
	}
	f := verifiedSupport(t, s, e.ID, "支持事实")
	stamped, err := s.AddGoal(model.Goal{Statement: "旧目标", IdempotencyKey: "old-key"})
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := s.PinGoal(stamped.ID, e.ID, "证据足以证明旧目标", []string{f.ID})
	if err != nil {
		t.Fatal(err)
	}
	// A retry of the old append request returns the stamped goal, not a fresh one.
	retried, created, err := s.AddGoalIdempotent(model.Goal{Statement: "旧目标", IdempotencyKey: "old-key"})
	if err != nil {
		t.Fatal(err)
	}
	if created || retried.ID != stamped.ID || !retried.Met || !reflect.DeepEqual(retried.From, pinned.From) {
		t.Fatalf("retry did not return the stamped goal: %+v", retried)
	}
	if _, created, err := s.AddGoalIdempotent(model.Goal{Statement: "新目标", IdempotencyKey: "new-key"}); err != nil || !created {
		t.Fatalf("new append: created=%v err=%v", created, err)
	}
	got := s.Goals()
	if len(got) != 2 {
		t.Fatalf("goals = %d, want 2", len(got))
	}
	old := got[0]
	if old.ID != stamped.ID {
		old = got[1]
	}
	if !old.Met || old.EvidenceID != pinned.EvidenceID || old.Note != pinned.Note ||
		!reflect.DeepEqual(old.From, pinned.From) ||
		!reflect.DeepEqual(old.FromClaimVersions, pinned.FromClaimVersions) ||
		old.CompletedAt == nil || !old.CompletedAt.Equal(*pinned.CompletedAt) ||
		old.ReceiptStale || len(old.ReceiptChanged) != 0 {
		t.Fatalf("stamped receipt changed after appending a new goal: before=%+v after=%+v", pinned, old)
	}
}
