package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/access"
	"github.com/kksty/tianwu/internal/api"
	"github.com/kksty/tianwu/internal/project"
)

func TestProjectDeletionKeepsBrowserOwnershipAndLeavesTheProjectUsable(t *testing.T) {
	mgr, err := project.Open(filepath.Join(t.TempDir(), "projects"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := mgr.Create("浏览器占用项目", "")
	if err != nil {
		t.Fatal(err)
	}
	srv := api.New(p.Store, nil, p.Dir, p.CaseDir)
	entry, err := access.Create(p.CaseDir, access.Input{Name: "登录现场", Kind: "browser", Origin: "https://example.test"})
	if err != nil {
		t.Fatal(err)
	}
	owner := access.BrowserLease{Owner: "operator"}
	lease, err := access.AcquireBrowser(p.CaseDir, entry.ID, owner)
	if err != nil {
		t.Fatal(err)
	}
	canceled := false
	h := &hub{mgr: mgr, kits: map[string]*kit{p.ID: {proj: p, srv: srv, cancel: func() { canceled = true }}}}
	rec := httptest.NewRecorder()
	h.deleteProject(rec, httptest.NewRequest("DELETE", "/api/projects/"+p.ID+"?confirm="+p.ID, nil), p.ID)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "浏览器现场") {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body.String())
	}
	if canceled {
		t.Fatal("failed deletion stopped the project")
	}
	if _, err := os.Stat(p.Dir); err != nil {
		t.Fatal("failed deletion removed project data")
	}
	if err := access.ReleaseBrowser(p.CaseDir, entry.ID, lease.ID, owner); err != nil {
		t.Fatal(err)
	}
	if err := srv.CheckBrowserReleaseForDeletion(); err != nil {
		t.Fatal(err)
	}
}
