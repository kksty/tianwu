package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/kksty/tianwu/internal/api"
	"github.com/kksty/tianwu/internal/project"
	"github.com/kksty/tianwu/internal/vulnlib"
)

func TestProjectDeletionRemovesWorkspaceAndIndexButKeepsSharedTools(t *testing.T) {
	data := t.TempDir()
	mgr, err := project.Open(filepath.Join(data, "projects"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := mgr.Create("disposable", "")
	if err != nil {
		t.Fatal(err)
	}
	other, err := mgr.Create("remaining", "")
	if err != nil {
		t.Fatal(err)
	}
	vulns, err := vulnlib.Open(filepath.Join(data, "vulns"))
	if err != nil {
		t.Fatal(err)
	}
	// All directories are fixtures. Never inspect or remove real user tools.
	userDir := filepath.Join(data, "fixture-user")
	t.Setenv("USERPROFILE", userDir)
	t.Setenv("HOME", userDir)
	shared := []string{
		filepath.Join(data, "arsenal", "bin", "shared-tool.exe"),
		filepath.Join(userDir, ".cyber", "arsenal", "bin", "user-tool.exe"),
		filepath.Join(other.CaseDir, "tool.exe"),
	}
	for _, file := range append(shared, filepath.Join(p.CaseDir, "local-tool.exe")) {
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte("fixture tool"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	srv := api.New(p.Store, nil, p.Dir, p.CaseDir)
	canceled := false
	h := &hub{mgr: mgr, vulns: vulns, kits: map[string]*kit{
		p.ID: {proj: p, srv: srv, cancel: func() { canceled = true }},
	}}
	rec := httptest.NewRecorder()
	h.deleteProject(rec, httptest.NewRequest(http.MethodDelete, "/api/projects/"+p.ID+"?confirm="+p.ID, nil), p.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body.String())
	}
	if !canceled || h.kit(p.ID) != nil || mgr.Get(p.ID) != nil {
		t.Fatal("deleted project remains registered or running")
	}
	if _, err := os.Stat(p.Dir); !os.IsNotExist(err) {
		t.Fatalf("deleted project directory still exists: %v", err)
	}
	for _, file := range shared {
		got, err := os.ReadFile(file)
		if err != nil || string(got) != "fixture tool" {
			t.Fatalf("shared tool was changed: %s: %v", file, err)
		}
	}
	index := httptest.NewRecorder()
	h.serveProjects(index, httptest.NewRequest(http.MethodGet, "/api/projects", nil))
	var body struct {
		Projects []projectInfo `json:"projects"`
	}
	if err := json.Unmarshal(index.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Projects) != 1 || body.Projects[0].ID != other.ID {
		t.Fatalf("index did not immediately reflect deletion: %+v", body.Projects)
	}
}
