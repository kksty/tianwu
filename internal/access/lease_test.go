package access

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBrowserLeasePersistsAndRejectsOtherOwnersAndStaleReturns(t *testing.T) {
	root := t.TempDir()
	entry, err := Create(root, Input{Name: "共享现场", Kind: "browser", Origin: "https://example.test"})
	if err != nil {
		t.Fatal(err)
	}
	owner := BrowserLease{Owner: "pt_a", RunID: "r_a", IntentID: "in_a", Generation: 1}
	first, err := AcquireBrowser(root, entry.ID, owner)
	if err != nil {
		t.Fatal(err)
	}
	again, err := AcquireBrowser(root, entry.ID, owner)
	if err != nil || again.ID != first.ID {
		t.Fatalf("retry changed ownership: %+v %v", again, err)
	}
	other := BrowserLease{Owner: "pt_b", RunID: "r_b", IntentID: "in_b", Generation: 1}
	if _, err := AcquireBrowser(root, entry.ID, other); err == nil {
		t.Fatal("another worker took the occupied browser")
	}
	read, err := Get(root, entry.ID)
	if err != nil || read.Lease == nil || read.Lease.ID != first.ID {
		t.Fatalf("reopened ownership = %+v %v", read, err)
	}
	if err := Delete(root, entry.ID); err == nil {
		t.Fatal("deleted occupied browser")
	}
	if err := ReleaseBrowser(root, entry.ID, first.ID, other); err == nil {
		t.Fatal("another worker returned the owner lease")
	}
	if err := ReleaseBrowser(root, entry.ID, first.ID, owner); err != nil {
		t.Fatal(err)
	}
	second, err := AcquireBrowser(root, entry.ID, other)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID {
		t.Fatal("acquisition identity was reused")
	}
	if err := ReleaseBrowser(root, entry.ID, first.ID, owner); err == nil {
		t.Fatal("old return released a new holder")
	}
}

func TestDamagedLeaseCannotBeTreatedAsAnIdleBrowser(t *testing.T) {
	root := t.TempDir()
	entry, err := Create(root, Input{Name: "现场", Kind: "browser", Origin: "https://example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".access", entry.ID, "lease.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Get(root, entry.ID); err == nil {
		t.Fatal("invalid lease read as idle")
	}
	if _, err := AcquireBrowser(root, entry.ID, BrowserLease{Owner: "operator"}); err == nil {
		t.Fatal("invalid lease overwritten")
	}
}

// The release fence is the acquisition id plus the persisted holder identity
// (owner+run+intent). The intent's attempt counter can move backwards when a
// cancelled run settles, so it must not decide ownership: a holder whose
// generation changed but whose run/intent/lease match is still the holder, while
// a different run of the same intent never is.
func TestLeaseFenceIsHolderIdentityNotAttemptGeneration(t *testing.T) {
	root := t.TempDir()
	entry, err := Create(root, Input{Name: "现场", Kind: "browser", Origin: "https://example.test"})
	if err != nil {
		t.Fatal(err)
	}
	holder := BrowserLease{Owner: "pt_a", RunID: "r_a", IntentID: "in_a", Generation: 5}
	lease, err := AcquireBrowser(root, entry.ID, holder)
	if err != nil {
		t.Fatal(err)
	}
	// Attempts reverted after a cancelled settle; the same holder can still
	// release its own lease.
	reverted := holder
	reverted.Generation = 3
	if err := ReleaseBrowser(root, entry.ID, lease.ID, reverted); err != nil {
		t.Fatalf("same holder could not release after the attempt counter moved: %v", err)
	}
	// A new holder may repeat a generation number; identity, not generation, is
	// what the fence compares.
	next := BrowserLease{Owner: "pt_b", RunID: "r_b", IntentID: "in_b", Generation: 3}
	second, err := AcquireBrowser(root, entry.ID, next)
	if err != nil {
		t.Fatal(err)
	}
	// The same persisted holder but a new run (handoff/retry) is a different
	// holder: it cannot release the newer acquisition by replaying the lease id.
	oldRunSameIntent := holder
	oldRunSameIntent.RunID = "r_a2"
	if err := ReleaseBrowser(root, entry.ID, second.ID, oldRunSameIntent); err == nil {
		t.Fatal("a run of the same intent released another run's lease")
	}
	if err := ReleaseBrowser(root, entry.ID, second.ID, next); err != nil {
		t.Fatal(err)
	}
}

// A half-filled holder would make "who owns this" ambiguous; it is refused
// instead of guessed.
func TestLeaseRefusesHalfAnOwnerIdentity(t *testing.T) {
	root := t.TempDir()
	entry, err := Create(root, Input{Name: "现场", Kind: "browser", Origin: "https://example.test"})
	if err != nil {
		t.Fatal(err)
	}
	for _, holder := range []BrowserLease{
		{Owner: "pt_a", RunID: "r_a"},                       // run without intent
		{Owner: "pt_a", IntentID: "in_a"},                   // intent without run
		{Owner: "operator", RunID: "r_a", IntentID: "in_a"}, // operator with a run
		{Owner: "not-a-participant", RunID: "r_a", IntentID: "in_a"},
	} {
		if _, err := AcquireBrowser(root, entry.ID, holder); err == nil {
			t.Fatalf("half/incorrect owner identity was accepted: %+v", holder)
		}
	}
	if read, err := Get(root, entry.ID); err != nil || read.Lease != nil {
		t.Fatalf("a refused acquisition still wrote a lease: %+v %v", read.Lease, err)
	}
}
