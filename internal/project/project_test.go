package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// The scope is a human snapshot of the current boundary, not a whitelist: the
// engagement keeps working when the operator writes prose or when assets change
// mid-test. Nothing may parse it, validate it as URLs/IPs, or reject it — a
// programmatic check here would forbid exactly the dynamic additions the
// operator makes through messages.
func TestScopeIsFreeTextAndNeverValidated(t *testing.T) {
	root := t.TempDir()
	m, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	scope := "目标主站及同单位相关资产（子域/IP 以现场发现为准）\n" +
		"过程中新增的站点以操作员下发为准；不做破坏性操作\n" +
		"not-a-url / 10.0.0.0/24 / 任意中文"
	p, err := m.Create("free scope", scope)
	if err != nil {
		t.Fatalf("free-text scope was rejected: %v", err)
	}
	if p.Scope != scope {
		t.Fatalf("scope was normalized: %q", p.Scope)
	}
	if err := m.SetScope(p.ID, "改成只测 a.example 和 b.example"); err != nil {
		t.Fatalf("updating the boundary was rejected: %v", err)
	}
	if m.Get(p.ID).Scope != "改成只测 a.example 和 b.example" {
		t.Fatal("scope update was not stored verbatim")
	}
}

func TestProjectPauseSurvivesReopenUntilExplicitResume(t *testing.T) {
	root := t.TempDir()
	m, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	p, err := m.Create("paused", "scope")
	if err != nil {
		t.Fatal(err)
	}
	if err = p.SetPaused(true); err != nil {
		t.Fatal(err)
	}
	m, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	p = m.Get(p.ID)
	if !p.IsPaused() {
		t.Fatal("restart silently resumed project")
	}
	if err = p.SetPaused(false); err != nil {
		t.Fatal(err)
	}
	m, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if m.Get(p.ID).IsPaused() {
		t.Fatal("explicit resume was not persisted")
	}
}

// The whole point of the project layer: two engagements share a process but not
// a ledger, so a fact declared in one is invisible in the other.
// The directory is an opaque address, not a summary of the engagement: a name
// containing a target host or URL must not leak into the path or console URL,
// and two same-named projects must still get distinct directories.
func TestProjectDirectoryIDIsOpaqueAndIndependentOfName(t *testing.T) {
	m, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	name := "61.133.196.36 与 http://www.nx.example/ 门户外网评估"
	a, err := m.Create(name, "scope")
	if err != nil {
		t.Fatal(err)
	}
	if a.Name != name {
		t.Fatalf("display name = %q, want the operator's name verbatim", a.Name)
	}
	for _, leak := range []string{"61", "133", "nx", "example", "www"} {
		if strings.Contains(a.ID, leak) {
			t.Fatalf("directory id %q leaks the name (%q)", a.ID, leak)
		}
	}
	b, err := m.Create("same name", "scope")
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Create("same name", "scope")
	if err != nil {
		t.Fatal(err)
	}
	if b.ID == c.ID || b.Dir == c.Dir {
		t.Fatalf("same-named projects share an address: %q / %q", b.ID, c.ID)
	}
}

func TestCreateKeepsLedgersApart(t *testing.T) {
	root := t.TempDir()
	m, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	a, err := m.Create("Acme Web", "acme.example")
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Create("Beta", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(a.ID) != 10 || !strings.HasPrefix(a.ID, "p-") {
		t.Fatalf("id = %q, want the short opaque p-xxxxxxxx directory id", a.ID)
	}
	if strings.Contains(a.ID, "acme") {
		t.Fatalf("id = %q, want the name kept out of the directory address", a.ID)
	}
	if _, err := a.Store.AddFact(model.Fact{Title: "only in acme", Kind: "vuln"}); err != nil {
		t.Fatal(err)
	}
	if got := len(b.Store.Snapshot().Facts); got != 0 {
		t.Fatalf("beta sees %d facts, want 0", got)
	}
	if a.CaseDir == b.CaseDir {
		t.Fatalf("projects share a workspace: %s", a.CaseDir)
	}
	if _, err := os.Stat(filepath.Join(a.Dir, ManifestFile)); err != nil {
		t.Fatalf("manifest not written: %v", err)
	}
	if _, err := os.Stat(a.CaseDir); err != nil {
		t.Fatalf("workspace not created: %v", err)
	}

	// A restart must find the same projects, names and facts.
	m2, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(m2.All()) != 2 {
		t.Fatalf("reopened %d projects, want 2", len(m2.All()))
	}
	again := m2.Get(a.ID)
	if again == nil {
		t.Fatalf("project %s lost on reopen", a.ID)
	}
	if again.Name != "Acme Web" || again.Scope != "acme.example" {
		t.Fatalf("manifest not restored: %+v", again.Manifest)
	}
	if got := len(again.Store.Snapshot().Facts); got != 1 {
		t.Fatalf("facts after reopen = %d, want 1", got)
	}
}

// The console's per-project concurrency cap lives in the manifest: an override
// survives a restart, and clearing it restores "inherit the instance default"
// instead of quietly writing 0 (which means uncapped).
func TestMaxRunsOverridePersists(t *testing.T) {
	root := t.TempDir()
	m, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	p, err := m.Create("Acme", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.MaxRunsOverride(); ok {
		t.Fatal("a new project has an override, want inherit")
	}
	two := 2
	if err := m.SetMaxRuns(p.ID, &two); err != nil {
		t.Fatal(err)
	}

	m2, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	again := m2.Get(p.ID)
	if v, ok := again.MaxRunsOverride(); !ok || v != 2 {
		t.Fatalf("override after reopen = %d, %v; want 2, true", v, ok)
	}

	if err := m2.SetMaxRuns(p.ID, nil); err != nil {
		t.Fatal(err)
	}
	m3, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m3.Get(p.ID).MaxRunsOverride(); ok {
		t.Fatal("cleared override survived the restart")
	}
}

// The review policy is a project setting too: an override survives a restart
// and clearing it restores "inherit the instance --verify-auto" (D100).
func TestVerifyAutoOverridePersists(t *testing.T) {
	root := t.TempDir()
	m, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	p, err := m.Create("Acme", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.VerifyAutoOverride(); ok {
		t.Fatal("a new project has an override, want inherit")
	}
	on := true
	if err := m.SetVerifyAuto(p.ID, &on); err != nil {
		t.Fatal(err)
	}
	m2, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := m2.Get(p.ID).VerifyAutoOverride(); !ok || !v {
		t.Fatalf("override after reopen = %v, %v; want true, true", v, ok)
	}
	off := false
	if err := m2.SetVerifyAuto(p.ID, &off); err != nil {
		t.Fatal(err)
	}
	m3, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := m3.Get(p.ID).VerifyAutoOverride(); !ok || v {
		t.Fatalf("explicit off after reopen = %v, %v; want false, true", v, ok)
	}
	if err := m3.SetVerifyAuto(p.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := m3.Get(p.ID).VerifyAutoOverride(); ok {
		t.Fatal("cleared override survived")
	}
	if err := m3.SetVerifyAuto("missing", &on); err == nil {
		t.Fatal("SetVerifyAuto on an unknown project must fail")
	}
}

func TestEnsureDefaultUsesFixedID(t *testing.T) {
	m, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := m.EnsureDefault("默认项目", "")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != DefaultID {
		t.Fatalf("default project id = %q, want %q", p.ID, DefaultID)
	}
	again, err := m.EnsureDefault("另一个", "")
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != DefaultID || len(m.All()) != 1 {
		t.Fatalf("EnsureDefault created a second project: %v", again.ID)
	}
}

func TestCreateDedupesIDs(t *testing.T) {
	m, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, err := m.Create("dup", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Create("dup", "")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID {
		t.Fatalf("two projects got one id: %s", a.ID)
	}
	if a.Dir == b.Dir {
		t.Fatalf("two projects share a directory: %s", a.Dir)
	}
}

func TestOpenAdoptsDirectoryWithoutManifest(t *testing.T) {
	root := t.TempDir()
	d := filepath.Join(root, "restored")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(d, "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddFact(model.Fact{Title: "restored fact"}); err != nil {
		t.Fatal(err)
	}

	m, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	p := m.Get("restored")
	if p == nil {
		t.Fatal("directory without a manifest was not adopted")
	}
	if p.Name != "restored" {
		t.Fatalf("name = %q, want the directory name", p.Name)
	}
	if got := len(p.Store.Snapshot().Facts); got != 1 {
		t.Fatalf("facts = %d, want 1", got)
	}
	if _, err := os.Stat(filepath.Join(d, ManifestFile)); err != nil {
		t.Fatalf("adopted project has no manifest: %v", err)
	}
}

// A pre-project data directory is moved into projects/default once, workspaces
// included, and the second call is a no-op.

func TestOpenRejectsProjectWithoutSQLiteWithoutCreatingEmptyLedger(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "old")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"version":10,"facts":[]}`)
	path := filepath.Join(dir, "ledger.json")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(root); err == nil {
		t.Fatal("unsupported project accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "ledger.sqlite")); !os.IsNotExist(err) {
		t.Fatal("empty replacement created")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(original) {
		t.Fatal("old project changed")
	}
}
