package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/collab"
)

// The participant API authenticates with its own token, so the console login
// gate must let the path through without opening any other route.
func TestAgentPathBypassesConsoleToken(t *testing.T) {
	called := false
	h := withConsoleAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }), "console-token")
	req := httptest.NewRequest(http.MethodGet, "/agent/p1/api/participants", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !called {
		t.Fatalf("agent path did not reach the hub: %d", rec.Code)
	}

	// A console route still needs the token.
	called = false
	req = httptest.NewRequest(http.MethodGet, "/p/p1/api/board", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if called || rec.Code != http.StatusUnauthorized {
		t.Fatalf("console path without a token: called=%v code=%d", called, rec.Code)
	}
}

func TestServeAgentRejectsWrongProjectAndMissingToken(t *testing.T) {
	reg := collab.NewRegistry(time.Hour)
	id, err := reg.Issue(collab.Identity{
		ProjectID: "p1", ParticipantID: collab.ParticipantID("in_1"),
		IntentID: "in_1", RunID: "r_1", Generation: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	h := &hub{cfg: hubConfig{Collab: reg}, kits: map[string]*kit{}}

	req := httptest.NewRequest(http.MethodGet, "/agent/p2/api/participants", nil)
	req.Header.Set("Authorization", "Bearer "+id.Token)
	rec := httptest.NewRecorder()
	h.serveAgent(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-project token: %d %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/agent/p1/api/participants", nil)
	rec = httptest.NewRecorder()
	h.serveAgent(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing token: %d %s", rec.Code, rec.Body.String())
	}

	// The right project with no live kit is a 404, not a 403 or a panic.
	req = httptest.NewRequest(http.MethodGet, "/agent/p1/api/participants", nil)
	req.Header.Set("Authorization", "Bearer "+id.Token)
	rec = httptest.NewRecorder()
	h.serveAgent(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing project: %d %s", rec.Code, rec.Body.String())
	}
}
