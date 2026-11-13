package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/kb"
)

func TestKnowledgeManagementPublishesAndExportsWithoutRestart(t *testing.T) {
	h := newKBHub(t)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRecorder()
		h.serveKBManagement(r, httptest.NewRequest(method, path, strings.NewReader(body)))
		return r
	}
	r := call("POST", "/api/kb/candidates", `{"entry":{"id":"world-api","title":"事实","description":"摘要","detail":"正文","source":"规范"},"reason":"协议通用事实","origin":"forged-origin"}`)
	if r.Code != 201 {
		t.Fatal(r.Body.String())
	}
	var c kb.Candidate
	if err := json.Unmarshal(r.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	if c.Origin != "operator" {
		t.Fatal("origin accepted from body")
	}
	path := "/api/kb/candidates/" + c.ID + "/publish"
	if r := call("POST", path, fmt.Sprintf(`{"version":%d}`, c.Version)); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	if !strings.Contains(call("GET", "/api/kb/candidates", "").Body.String(), `"published":true`) {
		t.Fatal("candidate not marked published")
	}
	if r := call("POST", path, `{"version":0}`); r.Code != 409 {
		t.Fatal("stale publication accepted")
	}
	zip := call("GET", "/api/kb/export?id=world-api", "")
	if zip.Code != 200 || zip.Header().Get("Content-Type") != "application/zip" || !strings.HasPrefix(zip.Body.String(), "PK") {
		t.Fatal("missing asset archive")
	}
	if r := call("POST", "/api/kb/reload", ""); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	found := false
	for _, e := range getKBEntries(t, h, "/api/kb") {
		if e.ID == "world-api" {
			found = true
		}
	}
	if !found {
		t.Fatal("publication not in shared library")
	}
}

func TestAgentSeesKnowledgeNominationAndTrafficButNotWorldPublication(t *testing.T) {
	for _, path := range []string{"/api/kb/export", "/api/kb/reload", "/api/kb/candidates", "/api/kb/candidates/id/publish", "/p/p1/api/traffic/import"} {
		if !agentRouteHidden(path) {
			t.Fatalf("operator route exposed: %s", path)
		}
	}
	for _, path := range []string{"/api/kb", "/p/p1/api/kb/candidates", "/p/p1/api/traffic/flows", "/p/p1/api/traffic/flow"} {
		if agentRouteHidden(path) {
			t.Fatalf("project capability hidden: %s", path)
		}
	}
}

func TestKnowledgeDeleteHTTPRequiresTheReadRevision(t *testing.T) {
	h := newKBHub(t)
	e := h.cfg.KB.Entries()[0]
	request := func(revision string) *httptest.ResponseRecorder {
		r := httptest.NewRecorder()
		body, _ := json.Marshal(map[string]string{"revision": revision})
		h.serveKBManagement(r, httptest.NewRequest("DELETE", "/api/kb/entries/"+e.ID, strings.NewReader(string(body))))
		return r
	}
	if r := request("old"); r.Code != 409 {
		t.Fatal("stale deletion accepted")
	}
	if r := request(kb.Revision(e)); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	for _, entry := range getKBEntries(t, h, "/api/kb") {
		if entry.ID == e.ID {
			t.Fatal("deleted knowledge still listed")
		}
	}
	if h.cfg.KB.DeletedRevisions()[e.ID] == "" {
		t.Fatal("instance deletion not recorded")
	}
}
