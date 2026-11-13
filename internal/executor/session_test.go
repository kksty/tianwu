package executor

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSegment(t *testing.T, workDir, name, body string) {
	t.Helper()
	dir := filepath.Join(workDir, SessionDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The merged file is what `-r` eats. It must be the segments in turn order and
// nothing else — a resume that reads turn 2 before turn 1 loads a child session
// with no parent.
func TestMergeSessionConcatenatesInOrder(t *testing.T) {
	dir := t.TempDir()
	// Written out of order on purpose: names, not mtimes, decide.
	writeSegment(t, dir, "turn-0002.aop.jsonl", "{\"n\":2}\n")
	writeSegment(t, dir, "turn-0001.aop.jsonl", "{\"n\":1}\n")
	writeSegment(t, dir, "turn-0010.aop.jsonl", "{\"n\":10}\n")

	n, err := MergeSession(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("merged %d segments, want 3", n)
	}
	got, err := os.ReadFile(SessionPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"n\":1}\n{\"n\":2}\n{\"n\":10}\n"
	if string(got) != want {
		t.Fatalf("merged =\n%q\nwant\n%q", got, want)
	}
	if !HasSession(dir) {
		t.Fatal("HasSession = false after a merge")
	}
}

// Merging happens at the end of every run, including after a crash and retry.
// Rebuilding (rather than appending) is what keeps that from duplicating events.
func TestMergeSessionIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	writeSegment(t, dir, "turn-0001.aop.jsonl", "{\"n\":1}\n")
	writeSegment(t, dir, "turn-0002.aop.jsonl", "{\"n\":2}\n")

	if _, err := MergeSession(dir); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(SessionPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := MergeSession(dir); err != nil {
			t.Fatal(err)
		}
	}
	again, err := os.ReadFile(SessionPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(again) {
		t.Fatalf("second merge changed the file:\n%q\n%q", first, again)
	}
	if want := "{\"n\":1}\n{\"n\":2}\n"; string(again) != want {
		t.Fatalf("merged = %q, want %q", again, want)
	}
}

// A killed process leaves a half-written last line. Gluing it to the next
// segment would corrupt the event before it, so the fragment is dropped.
func TestMergeSessionDropsPartialTail(t *testing.T) {
	dir := t.TempDir()
	writeSegment(t, dir, "turn-0001.aop.jsonl", "{\"n\":1}\n{\"n\":2}\n{\"n\":")
	writeSegment(t, dir, "turn-0002.aop.jsonl", "{\"n\":3}\n")

	if _, err := MergeSession(dir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(SessionPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if want := "{\"n\":1}\n{\"n\":2}\n{\"n\":3}\n"; string(got) != want {
		t.Fatalf("merged = %q, want %q", got, want)
	}
}

func TestMergeSessionWithoutSegmentsWritesNothing(t *testing.T) {
	dir := t.TempDir()
	n, err := MergeSession(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("merged %d segments, want 0", n)
	}
	if _, err := os.Stat(SessionPath(dir)); !os.IsNotExist(err) {
		t.Fatalf("a merged file appeared without segments: %v", err)
	}
}

// `-o` refuses to overwrite, so the number of the next segment is the highest
// one used plus one. Counting files would hand the same name out twice after a
// turn that died before writing anything.
func TestNextSegmentSkipsUsedNumbers(t *testing.T) {
	dir := t.TempDir()
	for i, name := range []string{"turn-0001.aop.jsonl", "turn-0007.aop.jsonl"} {
		writeSegment(t, dir, name, "{}")
		_ = i
	}
	got, err := nextSegment(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(SessionDir, "turn-0008.aop.jsonl"); got != want {
		t.Fatalf("next = %q, want %q", got, want)
	}
}

func TestHasSessionRejectsEmptyFile(t *testing.T) {
	dir := t.TempDir()
	if HasSession(dir) {
		t.Fatal("HasSession = true without a file")
	}
	if err := os.WriteFile(SessionPath(dir), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if HasSession(dir) {
		t.Fatal("HasSession = true for an empty trajectory")
	}
}
