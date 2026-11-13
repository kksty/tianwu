package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/project"
)

// openPolicyFixture is the default: no manifest policy, so every participant may
// message every other one (the pre-policy behaviour).
func TestPeerPolicyOpenByDefaultAuthorizesEveryone(t *testing.T) {
	f := newAgentFixture(t)
	f.makeLive(t)
	third, err := f.p.Store.CreateIntent(model.Intent{Title: "第三个方向"})
	if err != nil {
		t.Fatal(err)
	}
	rec := f.call(t, f.srcID, http.MethodGet, "/api/participants", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("participants: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Participants []participantView `json:"participants"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	byID := map[string]participantView{}
	for _, p := range resp.Participants {
		if p.Authorized == nil {
			t.Fatalf("participant %s has no authorized field", p.ID)
		}
		byID[p.ID] = p
	}
	if !*byID[collab.ParticipantID(third.ID)].Authorized || !*byID[collab.CommanderID].Authorized {
		t.Fatalf("open policy should authorize everyone: %+v", resp.Participants)
	}
	if rec := f.call(t, f.srcID, http.MethodPost, "/api/messages",
		sendBody(collab.ParticipantID(third.ID), "open-k", "你好")); rec.Code != http.StatusAccepted {
		t.Fatalf("open send: %d %s", rec.Code, rec.Body.String())
	}
}

// allowlistFixture publishes a one-way allowance src→target and a third
// participant that is not on the list.
func allowlistFixture(t *testing.T) (*agentFixture, model.Intent) {
	t.Helper()
	f := newAgentFixture(t)
	third, err := f.p.Store.CreateIntent(model.Intent{Title: "未授权方向"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.SetPeerPolicy(project.PeerPolicy{
		Mode: project.PeerPolicyAllowlist,
		Allow: []project.PeerRule{{
			From: f.srcID.ParticipantID, To: f.tgtID.ParticipantID,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	return f, third
}

func TestPeerAllowlistAuthorizesOnlyListedPairs(t *testing.T) {
	f, third := allowlistFixture(t)
	f.makeLive(t)
	// The listed direction works.
	if rec := f.call(t, f.srcID, http.MethodPost, "/api/messages",
		sendBody(f.tgtID.ParticipantID, "allow-k", "授权方向")); rec.Code != http.StatusAccepted {
		t.Fatalf("allowed send: %d %s", rec.Code, rec.Body.String())
	}
	// The unlisted one is refused before the target's state is revealed.
	rec := f.call(t, f.srcID, http.MethodPost, "/api/messages",
		sendBody(collab.ParticipantID(third.ID), "deny-k", "不该到"))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "peer_denied") {
		t.Fatalf("denied send: %d %s", rec.Code, rec.Body.String())
	}
	// Nothing was stored for the denied direction.
	for _, m := range f.p.Store.Messages(100) {
		if m.ToParticipantID == collab.ParticipantID(third.ID) {
			t.Fatalf("denied message was stored: %+v", m)
		}
	}
	// The roster says which peers are reachable.
	rec = f.call(t, f.srcID, http.MethodGet, "/api/participants", nil)
	var resp struct {
		Participants []participantView `json:"participants"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	byID := map[string]participantView{}
	for _, p := range resp.Participants {
		byID[p.ID] = p
	}
	if !*byID[f.tgtID.ParticipantID].Authorized {
		t.Fatal("listed pair is not authorized in the roster")
	}
	if *byID[collab.ParticipantID(third.ID)].Authorized {
		t.Fatal("unlisted peer is authorized in the roster")
	}
	if !*byID[collab.CommanderID].Authorized {
		t.Fatal("commander must stay exempt from the peer policy")
	}
}

// The commander channel is the operator's, not a peer direction: allowlist mode
// must not close it.
func TestPeerAllowlistDoesNotBlockCommander(t *testing.T) {
	f, _ := allowlistFixture(t)
	rec := f.call(t, f.srcID, http.MethodPost, "/api/messages",
		map[string]any{"to": collab.CommanderID, "text": "报到", "idempotency_key": "cmd-k"})
	if rec.Code == http.StatusForbidden {
		t.Fatalf("commander was blocked: %s", rec.Body.String())
	}
	if rec.Code != http.StatusAccepted && rec.Code != http.StatusOK {
		t.Fatalf("commander send: %d %s", rec.Code, rec.Body.String())
	}
}

// The operator maintains the rule set through PUT /p/<p>/api/peers; the edit is
// persisted in the manifest and takes effect for the next send.
func TestPeerPolicyConsoleEndpointPersists(t *testing.T) {
	f := newAgentFixture(t)
	f.makeLive(t)
	body := project.PeerPolicy{
		Mode:  project.PeerPolicyAllowlist,
		Allow: []project.PeerRule{{From: f.srcID.ParticipantID, To: f.tgtID.ParticipantID}},
	}
	rec := call(t, f.srv, http.MethodPut, "/api/peers", body)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"mode":"allowlist"`) {
		t.Fatalf("put peers: %d %s", rec.Code, rec.Body.String())
	}
	rec = call(t, f.srv, http.MethodGet, "/api/peers", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), f.tgtID.ParticipantID) {
		t.Fatalf("get peers: %d %s", rec.Code, rec.Body.String())
	}
	if !f.p.PeerAllowed(f.srcID.ParticipantID, f.tgtID.ParticipantID) {
		t.Fatal("allowed pair not effective after PUT")
	}
	// Invalid modes are refused instead of being treated as open.
	rec = call(t, f.srv, http.MethodPut, "/api/peers", map[string]any{"mode": "sometimes"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad mode: %d %s", rec.Code, rec.Body.String())
	}
}
