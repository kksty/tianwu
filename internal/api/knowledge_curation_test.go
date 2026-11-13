package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/kb"
	"github.com/kksty/tianwu/internal/model"
)

func TestHumanPlaybookNominationDoesNotPublishAndDeletionDoesNotCascade(t *testing.T) {
	s, _ := newBindingServer(t)
	root := t.TempDir()
	if _, err := kb.Ensure(root); err != nil {
		t.Fatal(err)
	}
	var err error
	s.KB, err = kb.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	p, _, err := s.Store.UpsertPlaybook(model.Playbook{Title: "项目现场知识", When: "现场条件", Doc: "项目具体方法", Origin: model.PlaybookFromExecutor})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/playbooks/" + p.ID
	if r := call(t, s, http.MethodGet, path, nil); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	payload := map[string]any{"entry": kb.Entry{ID: "world-from-human", Title: "通用事实", Description: "何时查", Detail: "人工提取的事实", Source: "核对后的规范"}, "reason": "与具体现场无关", "source_updated_at": p.UpdatedAt}
	payload["source_updated_at"] = time.Time{}
	if r := call(t, s, http.MethodPost, path+"/kb-candidate", payload); r.Code != 409 {
		t.Fatal("outdated source version accepted")
	}
	payload["source_updated_at"] = p.UpdatedAt
	r := call(t, s, http.MethodPost, path+"/kb-candidate", payload)
	if r.Code != 201 {
		t.Fatal(r.Body.String())
	}
	var c kb.Candidate
	if err := json.Unmarshal(r.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.Origin, p.ID) || !strings.HasPrefix(c.Origin, "operator ") {
		t.Fatal("source identity missing")
	}
	if len(s.KB.Search("人工提取", nil, 5)) != 0 {
		t.Fatal("human nomination automatically published")
	}
	if _, err := s.KB.Publish(c.ID, c.Version); err != nil {
		t.Fatal(err)
	}
	if r := call(t, s, http.MethodDelete, path, map[string]any{"updated_at": time.Time{}}); r.Code != 409 {
		t.Fatal("stale source deletion accepted")
	}
	revision := s.Store.Revision()
	if r := call(t, s, http.MethodDelete, path, map[string]any{"updated_at": p.UpdatedAt}); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	if s.Store.Revision() != revision {
		t.Fatal("curation changed the commander's wake revision")
	}
	if _, ok := s.Store.PlaybookByID(p.ID); ok {
		t.Fatal("playbook remains")
	}
	if len(s.KB.Search("人工提取", nil, 5)) != 1 {
		t.Fatal("source deletion removed world knowledge")
	}
	if r := call(t, s, http.MethodPost, path+"/kb-candidate", payload); r.Code != 404 {
		t.Fatal("deleted source nominated")
	}
}
