package executor

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// A run leaves more than facts: cyber-harness can write the whole AOP event
// stream of a turn to a JSONL file (`-o`) and rebuild a later turn from it
// (`-r`). That file is the difference between "the run was cancelled" and "here
// is everything it tried before it was cancelled" (docs/06, D42).
//
// Layout, inside one case directory:
//
//	session/turn-0001.aop.jsonl   one segment per turn, exclusive-create
//	session/turn-0002.aop.jsonl
//	session.aop.jsonl             every segment concatenated, in order
//
// `-r` needs the merged file: a segment on its own refers to a parent task that
// lives in the earlier segments, and the harness refuses to load it
// ("refers to missing parent task", measured). The merged file is *derived* and
// rebuilt from the segments every time, so merging twice cannot duplicate
// events — the failure mode a blind append would have.
const (
	// SessionDir holds one segment per turn.
	SessionDir = "session"
	// SessionFile is the merged trajectory at the case root. It is also the
	// file handed to `-r`.
	SessionFile = "session.aop.jsonl"
)

const (
	segmentPrefix = "turn-"
	segmentSuffix = ".aop.jsonl"
)

// SessionPath is the merged trajectory a case would resume from.
func SessionPath(workDir string) string {
	return filepath.Join(workDir, SessionFile)
}

// HasSession reports whether case dir has a trajectory worth resuming. An empty
// file is not one: resuming from it would start a turn with no history.
func HasSession(workDir string) bool {
	if workDir == "" {
		return false
	}
	st, err := os.Stat(SessionPath(workDir))
	return err == nil && !st.IsDir() && st.Size() > 0
}

// segments returns the turn segments of a case, oldest first. Order comes from
// the number in the name, never from the directory listing: a resumed turn is
// written later but must not be read before the one it resumes.
func segments(workDir string) []string {
	dir := filepath.Join(workDir, SessionDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	type seg struct {
		path string
		n    int
	}
	var found []seg
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, segmentPrefix) || !strings.HasSuffix(name, segmentSuffix) {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, segmentPrefix), segmentSuffix))
		if err != nil {
			continue
		}
		found = append(found, seg{filepath.Join(dir, name), n})
	}
	sort.Slice(found, func(i, j int) bool { return found[i].n < found[j].n })
	out := make([]string, 0, len(found))
	for _, s := range found {
		out = append(out, s.path)
	}
	return out
}

// nextSegment reserves the next segment file for this turn. `-o` refuses to
// overwrite, so the number must be one nobody has used: the highest existing
// number plus one, not a count (a failed turn leaves its segment behind).
func nextSegment(workDir string) (string, error) {
	if workDir == "" {
		return "", fmt.Errorf("no work dir")
	}
	dir := filepath.Join(workDir, SessionDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	highest := 0
	for _, p := range segments(workDir) {
		name := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(p), segmentPrefix), segmentSuffix)
		if n, err := strconv.Atoi(name); err == nil && n > highest {
			highest = n
		}
	}
	return filepath.Join(SessionDir, fmt.Sprintf("%s%04d%s", segmentPrefix, highest+1, segmentSuffix)), nil
}

// MergeSession rebuilds the merged trajectory from the segments and returns how
// many it concatenated. It is a pure function of the segment directory: merge
// twice, get the same file (that is what makes "merge at the end of every run"
// safe, including after a crash).
//
// A segment whose last line has no newline was cut off mid-write (the process
// was killed). That half line is dropped instead of glued onto the next
// segment: a truncated *event* is not an event.
func MergeSession(workDir string) (int, error) {
	segs := segments(workDir)
	if len(segs) == 0 {
		return 0, nil
	}
	tmp, err := os.CreateTemp(filepath.Join(workDir, SessionDir), "merged-*.tmp")
	if err != nil {
		return 0, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	for _, p := range segs {
		data, err := os.ReadFile(p)
		if err != nil {
			tmp.Close()
			return 0, err
		}
		if _, err := tmp.Write(completeLines(data)); err != nil {
			tmp.Close()
			return 0, err
		}
	}
	if err := tmp.Close(); err != nil {
		return 0, err
	}
	if err := os.Rename(tmpName, SessionPath(workDir)); err != nil {
		return 0, err
	}
	return len(segs), nil
}

// completeLines drops a trailing line that was never finished. Empty input
// stays empty: a segment created and then killed before its first byte must not
// contribute a stray newline.
func completeLines(data []byte) []byte {
	if len(data) == 0 || data[len(data)-1] == '\n' {
		return data
	}
	if i := strings.LastIndexByte(string(data), '\n'); i >= 0 {
		return data[:i+1]
	}
	return nil
}
