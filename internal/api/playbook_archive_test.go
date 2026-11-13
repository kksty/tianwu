package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/retrieval"
	"github.com/kksty/tianwu/internal/store"
)

func TestArchivedExperienceHasStablePagesHistoryAndExplicitRestore(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	first, _, _ := st.UpsertPlaybook(model.Playbook{Title: "first", Origin: model.PlaybookFromExecutor})
	_, _, _ = st.UpsertPlaybook(model.Playbook{Title: "second", Origin: model.PlaybookFromExecutor})
	s := New(st, nil, dir, dir)
	mux := http.NewServeMux()
	s.Register(mux)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rr
	}
	rr := call("GET", "/api/playbooks?limit=1", "")
	var page retrieval.PlaybookPage
	if err = json.Unmarshal(rr.Body.Bytes(), &page); err != nil || page.Total != 2 || !page.HasMore {
		t.Fatal(rr.Body.String())
	}
	body, _ := json.Marshal(map[string]any{"archived": true, "updated_at": first.UpdatedAt})
	rr = call("POST", "/api/playbooks/"+first.ID+"/archive", string(body))
	if rr.Code != 200 {
		t.Fatal(rr.Body.String())
	}
	saved, _ := st.PlaybookByID(first.ID)
	omitted, _ := json.Marshal(map[string]any{"updated_at": saved.UpdatedAt})
	if rr = call("POST", "/api/playbooks/"+first.ID+"/archive", string(omitted)); rr.Code != 400 {
		t.Fatal("omitted archive state was treated as restore")
	}
	if rr = call("POST", "/api/playbooks/"+first.ID+"/archive", string(body)); rr.Code != 409 {
		t.Fatal("stale archive accepted")
	}
	rr = call("GET", "/api/playbooks?archived=only", "")
	if !strings.Contains(rr.Body.String(), first.ID) {
		t.Fatal("archive missing")
	}
	rr = call("GET", "/api/playbooks/"+first.ID+"/history", "")
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "archived") {
		t.Fatal("history missing")
	}
	rr = call("GET", "/api/playbooks?limit=1&cursor="+page.NextCursor, "")
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), first.ID) || strings.Contains(rr.Body.String(), `"archived":true`) {
		t.Fatal("snapshot changed during curation")
	}
}
