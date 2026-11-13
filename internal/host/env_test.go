package host

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeGit makes a ProgramFiles tree that looks like Git for Windows.
func fakeGit(t *testing.T) string {
	t.Helper()
	pf := t.TempDir()
	bin := filepath.Join(pf, "Git", "usr", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "ls.exe"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return bin
}

// A control plane started from PowerShell has no coreutils on PATH; the host
// must get Git's usr/bin or the agent's shell falls back to cmd (and `cmd //c`
// blocks forever). Measured: `ls` was missing, `cmd //c dir` hung 83s.
func TestChildEnvAddsCoreutilsOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows PATH fixup")
	}
	bin := fakeGit(t)
	t.Setenv("ProgramFiles", filepath.Dir(filepath.Dir(filepath.Dir(bin))))
	t.Setenv("ProgramFiles(x86)", "")
	t.Setenv("LOCALAPPDATA", "")
	t.Setenv("PATH", `C:\Windows\System32`)

	h := &Host{cfg: Config{Logf: func(string, ...any) {}}}
	path := envValue(h.childEnv(nil), "PATH")
	if !strings.HasPrefix(path, bin+string(os.PathListSeparator)) {
		t.Fatalf("PATH = %q, want it to start with %q", path, bin)
	}
	if !hasExe(path, "ls.exe") {
		t.Fatalf("ls.exe still not resolvable: %q", path)
	}
}

// An operator who already has coreutils keeps exactly their PATH: no shadowing,
// no second PATH entry.
func TestChildEnvLeavesGoodPathAlone(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows PATH fixup")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ls.exe"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	h := &Host{cfg: Config{Logf: func(string, ...any) {}}}
	if got := envValue(h.childEnv(nil), "PATH"); got != dir {
		t.Fatalf("PATH = %q, want %q", got, dir)
	}
}

// Settings arrive as "K=V"; they override the inherited environment instead of
// being appended twice.
func TestChildEnvAppliesOverrides(t *testing.T) {
	t.Setenv("TW_HOST_TEST", "old")
	h := &Host{cfg: Config{Logf: func(string, ...any) {}}}
	env := h.childEnv([]string{"TW_HOST_TEST=new"})
	if got := envValue(env, "TW_HOST_TEST"); got != "new" {
		t.Fatalf("override = %q", got)
	}
	n := 0
	for _, kv := range env {
		if strings.HasPrefix(kv, "TW_HOST_TEST=") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("TW_HOST_TEST appears %d times", n)
	}
}

// A profile host must not inherit the control plane's CYBER_* configuration:
// the profile's own values are appended after the inherited block is removed.
func TestChildEnvDropsInheritedPrefixes(t *testing.T) {
	t.Setenv("CYBER_API_KEY", "inherited-key")
	t.Setenv("cyber_base_url", "http://inherited:1")
	t.Setenv("CYBER_MODEL", "inherited-model")
	h := &Host{cfg: Config{
		Logf:            func(string, ...any) {},
		DropEnvPrefixes: []string{"CYBER_"},
		Env:             []string{"CYBER_MODEL=profile-model"},
	}}
	env := h.childEnv(h.cfg.Env)
	if got := envValue(env, "CYBER_API_KEY"); got != "" {
		t.Fatalf("inherited key survived: %q", got)
	}
	if got := envValue(env, "CYBER_BASE_URL"); got != "" {
		t.Fatalf("inherited base URL survived: %q", got)
	}
	if got := envValue(env, "CYBER_MODEL"); got != "profile-model" {
		t.Fatalf("profile model = %q", got)
	}
}

func TestEnvValueIsCaseInsensitive(t *testing.T) {
	env := []string{"Path=C:\\a", "PATH=C:\\b"}
	// The last definition wins, as it does for a child process.
	if got := envValue(env, "PATH"); got != `C:\b` {
		t.Fatalf("envValue = %q", got)
	}
	setEnv(&env, "path", `C:\c`)
	if got := envValue(env, "PATH"); got != `C:\c` {
		t.Fatalf("after setEnv = %q (%v)", got, env)
	}
}
