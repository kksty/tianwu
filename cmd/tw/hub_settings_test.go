package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/project"
)

func TestInvalidProjectSettingsDoNotApplyEarlierFields(t *testing.T) {
	for _, body := range []string{
		`{"scope":"changed","max_runs":-1}`,
		`{"scope":"changed","max_runs":2,"round_timeout":"bad-duration"}`,
		`{"scope":"changed","max_runs":2,"round_timeout":"10m","verify_auto":"wrong"}`,
		`{"scope":"changed","max_runs":2,"verify_auto":true,"worker_profile":"missing"}`,
	} {
		t.Run(body, func(t *testing.T) {
			h, _ := profileHub(t)
			mgr, err := project.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			p, err := mgr.Create("settings", "original")
			if err != nil {
				t.Fatal(err)
			}
			h.mgr = mgr
			w := httptest.NewRecorder()
			h.updateProject(w, httptest.NewRequest(http.MethodPut, "/api/projects/"+p.ID, strings.NewReader(body)), p.ID)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d: %s", w.Code, w.Body.String())
			}
			// Reopen from disk too: a rejected request must leave no partial save.
			reopened, err := project.Open(mgr.Root)
			if err != nil {
				t.Fatal(err)
			}
			got := reopened.Get(p.ID)
			if got.ScopeText() != "original" || got.RoundTimeoutRaw() != "" {
				t.Fatalf("rejected settings changed scope or timeout")
			}
			if _, ok := got.MaxRunsOverride(); ok {
				t.Fatal("rejected settings changed concurrency")
			}
			if _, ok := got.VerifyAutoOverride(); ok {
				t.Fatal("rejected settings changed review policy")
			}
			if worker, strong := got.ProfileDefaults(); worker != "" || strong != "" {
				t.Fatal("rejected settings changed model defaults")
			}
		})
	}
}
