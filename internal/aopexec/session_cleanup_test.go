package aopexec

import (
	"context"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/executor"
)

// A session this attempt opened did not run a single turn, so nothing points at
// it and nothing will ever close it. The commander is the important case: it
// writes no marker, so a rejected first submit used to leak one open session on
// the host per round. Closing it is the only cleanup owner.
func TestRejectedFirstTurnClosesTheFreshSession(t *testing.T) {
	h := &fakeHost{sessions: map[string]bool{}, rejectTurn: true}
	ex, done := newFake(t, h)
	defer done()
	work := t.TempDir()
	_, err := ex.Run(context.Background(), executor.Request{
		IntentID: "in_1", Purpose: "commander", Prompt: "x", WorkDir: work,
	}, func(executor.Event) {})
	if err == nil || !strings.Contains(err.Error(), "UNAVAILABLE") {
		t.Fatalf("want the protocol rejection, got %v", err)
	}
	h.mu.Lock()
	opened, closes := h.opened, h.closes
	h.mu.Unlock()
	if opened != 1 || closes != 1 {
		t.Fatalf("opened=%d closes=%d, want 1 and 1 (the leaked session stays open otherwise)", opened, closes)
	}
	// The pointer must not outlive the session it names.
	if CanResume(work) {
		t.Fatal("the marker of a session that never ran must be removed")
	}
}

// A resumed session keeps its trajectory on the host: a rejected turn is not a
// reason to close it or to drop the marker that makes the next attempt possible.
func TestRejectedContinuedTurnKeepsTheSession(t *testing.T) {
	h := &fakeHost{sessions: map[string]bool{"s1": true}, rejectTurn: true}
	ex, done := newFake(t, h)
	defer done()
	work := t.TempDir()
	if err := writeMarker(work, marker{URL: ex.BaseURL, Agent: "local", Session: "s1"}); err != nil {
		t.Fatal(err)
	}
	_, err := ex.Run(context.Background(), executor.Request{
		IntentID: "in_1", Prompt: "keep going", WorkDir: work, Resume: true,
	}, func(executor.Event) {})
	if err == nil {
		t.Fatal("want the protocol rejection")
	}
	h.mu.Lock()
	closes := h.closes
	h.mu.Unlock()
	if closes != 0 {
		t.Fatalf("closes=%d, a resumed session must not be closed on a failed turn", closes)
	}
	if !CanResume(work) {
		t.Fatal("the resume marker was dropped: the trajectory is still on the host")
	}
}
