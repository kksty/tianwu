package executor

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestNormalizePathAddsExeSuffix(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the .exe suffix rule is Windows-only")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "aiscan-full.exe")
	if err := os.WriteFile(exe, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := NormalizePath(filepath.Join(dir, "aiscan-full")); got != exe {
		t.Fatalf("extensionless path = %q, want %q", got, exe)
	}
	if got := NormalizePath(exe); got != exe {
		t.Fatalf("existing .exe path changed: %q", got)
	}
	missing := filepath.Join(dir, "nope")
	if got := NormalizePath(missing); got != missing {
		t.Fatalf("missing path = %q, want it unchanged", got)
	}
	if got := NormalizePath(""); got != "" {
		t.Fatalf("empty path = %q", got)
	}
}
