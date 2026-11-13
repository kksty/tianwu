package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// The console reads playbooks through their own endpoint, not /api/facts:
// confirmed entries lead, and retire is a state change, not a delete.
func TestListAndRetirePlaybooks(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	confirmed, _, err := st.UpsertPlaybook(model.Playbook{
		Title: "ThinkPHP RCE", When: "5.0.x 且日志可写", Doc: "curl -s /index.php?s=...",
		Tags: []string{"product:thinkphp"}, Origin: model.PlaybookFromExecutor, Status: model.PlaybookConfirmed,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.UpsertPlaybook(model.Playbook{
		Title: "Redis 未授权", When: "6379 无密码",
		Origin: model.PlaybookFromCommander, Status: model.PlaybookCandidate, FromFactIDs: []string{"f_1"},
	}); err != nil {
		t.Fatal(err)
	}

	s := New(st, nil, t.TempDir(), t.TempDir())
	mux := http.NewServeMux()
	s.Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	res, err := http.Get(srv.URL + "/api/playbooks")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	var got struct {
		Count     int              `json:"count"`
		Playbooks []model.Playbook `json:"playbooks"`
	}
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Count != 2 || len(got.Playbooks) != 2 {
		t.Fatalf("list = %+v", got)
	}
	if got.Playbooks[0].ID != confirmed.ID || got.Playbooks[0].Status != model.PlaybookConfirmed {
		t.Fatalf("confirmed entry must lead: %+v", got.Playbooks)
	}

	resp, err := http.Post(srv.URL+"/api/playbooks/"+confirmed.ID+"/retire", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("retire status = %d", resp.StatusCode)
	}
	p, ok := st.PlaybookByID(confirmed.ID)
	if !ok || p.Status != model.PlaybookRetired {
		t.Fatalf("retired row = %+v ok=%v", p, ok)
	}
}
