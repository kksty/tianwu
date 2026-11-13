package collab

import (
	"testing"
	"time"
)

func baseIdentity() Identity {
	return Identity{
		ProjectID: "p1", ParticipantID: ParticipantID("in_1"),
		IntentID: "in_1", RunID: "r_1", Generation: 2, APIBase: "http://127.0.0.1:8080",
	}
}

func TestIssueResolveRevoke(t *testing.T) {
	r := NewRegistry(time.Hour)
	id, err := r.Issue(baseIdentity())
	if err != nil {
		t.Fatal(err)
	}
	if id.Token == "" || id.ExpiresAt.Before(time.Now()) {
		t.Fatalf("issued = %+v", id)
	}
	got, ok := r.Resolve(id.Token)
	if !ok || got.RunID != "r_1" || got.ProjectID != "p1" || got.Generation != 2 {
		t.Fatalf("resolve = %+v ok=%v", got, ok)
	}
	r.RevokeRun("r_1")
	if _, ok := r.Resolve(id.Token); ok {
		t.Fatal("token survived revoke")
	}
	if _, ok := r.Resolve("not-a-token"); ok {
		t.Fatal("unknown token resolved")
	}
}

func TestStaleTokensAreRevokedByRunNotGeneration(t *testing.T) {
	r := NewRegistry(time.Hour)
	first, err := r.Issue(baseIdentity())
	if err != nil {
		t.Fatal(err)
	}
	next := baseIdentity()
	next.RunID, next.Generation = "r_2", 3
	second, err := r.Issue(next)
	if err != nil {
		t.Fatal(err)
	}
	// A new generation does not silently invalidate the old token; the old run
	// ending does. Until then both are live, which is what lets a finishing run
	// reply to a message it received during handover.
	if _, ok := r.Resolve(first.Token); !ok {
		t.Fatal("old run token revoked early")
	}
	if _, ok := r.Resolve(second.Token); !ok {
		t.Fatal("new run token missing")
	}
	r.RevokeRun("r_1")
	if _, ok := r.Resolve(first.Token); ok {
		t.Fatal("old run token survived its run")
	}
	if _, ok := r.Resolve(second.Token); !ok {
		t.Fatal("new run token revoked with the old run")
	}
}

func TestExpiredTokenDoesNotResolve(t *testing.T) {
	r := NewRegistry(time.Nanosecond)
	id, err := r.Issue(baseIdentity())
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if _, ok := r.Resolve(id.Token); ok {
		t.Fatal("expired token resolved")
	}
}

func TestParticipantIDRoundTrip(t *testing.T) {
	if got := ParticipantID("in_abc"); got != "pt_in_abc" {
		t.Fatalf("ParticipantID = %q", got)
	}
	if id, ok := IntentIDFromParticipant("pt_in_abc"); !ok || id != "in_abc" {
		t.Fatalf("round trip = %q %v", id, ok)
	}
	for _, bad := range []string{"commander", "pt_", "in_abc", ""} {
		if _, ok := IntentIDFromParticipant(bad); ok {
			t.Fatalf("%q parsed as a participant", bad)
		}
	}
}

func TestIssueRejectsIncompleteIdentity(t *testing.T) {
	r := NewRegistry(time.Hour)
	if _, err := r.Issue(Identity{ProjectID: "p"}); err == nil {
		t.Fatal("incomplete identity accepted")
	}
	var nilRegistry *Registry
	if _, err := nilRegistry.Issue(baseIdentity()); err == nil {
		t.Fatal("nil registry issued a token")
	}
	if _, ok := nilRegistry.Resolve("x"); ok {
		t.Fatal("nil registry resolved a token")
	}
}
