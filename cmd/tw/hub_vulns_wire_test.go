package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/vulnlib"
)

// GET /api/vulns is the console's wire contract for the global library. The
// entry must say how it is reproduced (`proof`) and what the last verifier said
// (`review`, omitted when there is none) — after a project is deleted the
// ledger is gone, so these cannot be joined in the frontend (D105).
func TestServeVulnsCarriesProofAndReview(t *testing.T) {
	lib, err := vulnlib.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	if _, err := lib.Upsert(vulnlib.Entry{
		ProjectID: "acme", SourceID: "f_1", Title: "越权读订单", Status: "verified",
		Proof: "dynamic",
		Review: &vulnlib.Review{
			Verdict: "pass", Notes: "重跑 200 且返回全部订单", VerifierRun: "r_7", CreatedAt: created,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := lib.Upsert(vulnlib.Entry{
		ProjectID: "acme", SourceID: "f_2", Title: "待复核的弱口令", Status: "unverified",
	}); err != nil {
		t.Fatal(err)
	}

	h := &hub{vulns: lib}
	w := httptest.NewRecorder()
	h.serveVulns(w, httptest.NewRequest(http.MethodGet, "/api/vulns", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var out struct {
		Vulns []map[string]json.RawMessage `json:"vulns"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not json: %v", err)
	}
	if len(out.Vulns) != 2 {
		t.Fatalf("vulns = %d, want 2", len(out.Vulns))
	}

	bySource := map[string]map[string]json.RawMessage{}
	for _, v := range out.Vulns {
		var id string
		if err := json.Unmarshal(v["source_id"], &id); err != nil {
			t.Fatalf("source_id = %s", v["source_id"])
		}
		bySource[id] = v
	}

	reviewed := bySource["f_1"]
	if string(reviewed["proof"]) != `"dynamic"` {
		t.Fatalf("proof = %s, want \"dynamic\"", reviewed["proof"])
	}
	var review struct {
		Verdict     string    `json:"verdict"`
		Notes       string    `json:"notes"`
		VerifierRun string    `json:"verifier_run"`
		CreatedAt   time.Time `json:"created_at"`
	}
	if err := json.Unmarshal(reviewed["review"], &review); err != nil {
		t.Fatalf("review = %s: %v", reviewed["review"], err)
	}
	if review.Verdict != "pass" || review.Notes == "" || review.VerifierRun != "r_7" || !review.CreatedAt.Equal(created) {
		t.Fatalf("review = %+v", review)
	}

	// No review and no proof mode: both are omitted rather than emitted empty.
	pending := bySource["f_2"]
	if raw, ok := pending["review"]; ok {
		t.Fatalf("an unreviewed entry must omit review, got %s", raw)
	}
	if raw, ok := pending["proof"]; ok {
		t.Fatalf("an entry with no proof mode must omit proof, got %s", raw)
	}
}
