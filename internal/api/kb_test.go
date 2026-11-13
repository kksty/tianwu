package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/kb"
)

func TestParticipantNominatesWithServerIdentityAndReadsLiveWorldKnowledge(t *testing.T) {
	s, _ := newBindingServer(t)
	dir := t.TempDir()
	if _, err := kb.Ensure(dir); err != nil {
		t.Fatal(err)
	}
	lib, err := kb.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.KB = lib
	req := httptest.NewRequest("POST", "/api/kb/candidates", strings.NewReader(`{"entry":{"id":"world-peer","title":"共同协议事实","description":"核对条件","detail":"事实内容","source":"协议规范"},"reason":"跨项目成立","origin":"forged","published":true}`))
	req = req.WithContext(WithIdentity(req.Context(), collab.Identity{ProjectID: "actual", ParticipantID: "pt_a", RunID: "r_a"}))
	rec := httptest.NewRecorder()
	s.AgentHandler().ServeHTTP(rec, req)
	if rec.Code != 201 {
		t.Fatal(rec.Body.String())
	}
	var c kb.Candidate
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	if c.Published || c.Origin != "project:actual participant:pt_a run:r_a" {
		t.Fatal("caller forged publication or source")
	}
	before := call(t, s, http.MethodGet, "/api/kb?q=共同协议事实", nil)
	if !strings.Contains(before.Body.String(), `"entries":[]`) {
		t.Fatal("draft became visible as world knowledge")
	}
	if _, err := lib.Publish(c.ID, c.Version); err != nil {
		t.Fatal(err)
	}
	after := call(t, s, http.MethodGet, "/api/kb?q=共同协议事实", nil)
	if !strings.Contains(after.Body.String(), "world-peer") {
		t.Fatal("running project did not see publication")
	}
	var response struct {
		Revisions map[string]string `json:"revisions"`
	}
	if err := json.Unmarshal(after.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Revisions["world-peer"] == "" {
		t.Fatal("reader did not get an edit token")
	}
	stale := call(t, s, http.MethodPost, "/api/kb/candidates", map[string]any{"entry": c.Entry, "reason": c.Reason, "base_revision": "stale"})
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale edit status=%d: %s", stale.Code, stale.Body.String())
	}
}
