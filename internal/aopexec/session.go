package aopexec

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// A run keeps its trajectory on the host (the session's event history), not as
// a file in the case directory. What the case needs locally is a pointer to it:
// which host, which agent, which session. That pointer is also what "is there
// something to continue?" means (D42).
const markerFile = ".tw/aop-session.json"

type marker struct {
	URL     string `json:"url"`
	Agent   string `json:"agent,omitempty"`
	Session string `json:"session"`
	Turn    string `json:"turn,omitempty"`
	// Cursor is the last delivery cursor seen, so a continued turn can ask the
	// host to replay only the tail (WatchEvents after_cursor) instead of the
	// whole session from zero.
	Cursor string `json:"cursor,omitempty"`
}

func markerPath(workDir string) string {
	return filepath.Join(workDir, filepath.FromSlash(markerFile))
}

// SessionInfo is the exported view of a case's trajectory pointer. The API
// needs the session id to deliver a mid-run message; the host and agent are
// there so a caller can refuse a marker that belongs to another node.
type SessionInfo struct {
	URL     string
	Agent   string
	Session string
	Turn    string
}

// ReadSession returns the trajectory pointer a previous AOP run left behind.
// It is the same file CanResume consults; exposing it lets "say" target the
// exact live session instead of guessing from the run id.
func ReadSession(workDir string) (SessionInfo, bool) {
	m, ok := readMarker(workDir)
	if !ok {
		return SessionInfo{}, false
	}
	return SessionInfo{URL: m.URL, Agent: m.Agent, Session: m.Session, Turn: m.Turn}, true
}

func readMarker(workDir string) (marker, bool) {
	var m marker
	if workDir == "" {
		return m, false
	}
	data, err := os.ReadFile(markerPath(workDir))
	if err != nil || len(data) == 0 {
		return m, false
	}
	if err := json.Unmarshal(data, &m); err != nil || m.Session == "" {
		return m, false
	}
	return m, true
}

func writeMarker(workDir string, m marker) error {
	if workDir == "" {
		return nil
	}
	path := markerPath(workDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	// Replace, do not append: a half-written marker would make the next steer
	// point at a session that never opened.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// updateMarkerCursor records the stream position after a turn. It is
// best-effort: losing the cursor only costs a replay on the next steer, not
// correctness (the turn filter still protects the result).
func updateMarkerCursor(workDir, cursor string) {
	if workDir == "" || cursor == "" {
		return
	}
	m, ok := readMarker(workDir)
	if !ok {
		return
	}
	m.Cursor = cursor
	_ = writeMarker(workDir, m)
}

// CanResume reports whether the case has an AOP session to continue. It is the
// transport-specific half of executor.CanResume: a case run by AOP has no
// session.aop.jsonl, and refusing every steer on such a case would quietly
// disable the feature for the transport that supports it best.
func CanResume(workDir string) bool {
	_, ok := readMarker(workDir)
	return ok
}
