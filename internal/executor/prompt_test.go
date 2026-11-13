package executor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The prompt must not travel in argv: Linux caps one argument at ~128 KiB, and
// the harness reads `-p` as a file path first, so a long prompt used to fail
// with "file name too long" (seen on the first real VPS run). Audit is the one
// caller left that hands the harness a prompt file on disk.
func TestPromptGoesThroughAFile(t *testing.T) {
	dir := t.TempDir()
	long := strings.Repeat("找一下这个服务有没有越权。", 400)

	path, err := writePromptFile(dir, AuditPromptFile, long)
	if err != nil {
		t.Fatal(err)
	}
	if path != AuditPromptFile {
		t.Fatalf("path = %q, want %q", path, AuditPromptFile)
	}
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(AuditPromptFile)))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != long {
		t.Fatalf("prompt file does not match what was sent (%d bytes)", len(data))
	}
}

func TestPromptFileNeedsAWorkDir(t *testing.T) {
	if _, err := writePromptFile("", AuditPromptFile, "x"); err == nil {
		t.Fatal("an empty work dir must not silently write anywhere")
	}
}
