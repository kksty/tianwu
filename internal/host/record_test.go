package host

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/aop"
)

func writeMarkerFile(t *testing.T, caseDir, url, agent, session string) string {
	t.Helper()
	path := filepath.Join(caseDir, ".tw", "aop-session.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]string{
		"url": url, "agent": agent, "session": session, "turn": "t1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRecordRoundTripAndRemove(t *testing.T) {
	dir := t.TempDir()
	rec := Record{
		Endpoint: "http://127.0.0.1:18766", PID: 4242, Agent: LocalAgent,
		DB: filepath.Join(dir, "host", "cyber-web.db"), Dir: dir,
		Bin: "aiscan-full.exe", StartedAt: time.Now().Truncate(time.Second), Restarts: 2,
	}
	if err := WriteRecord(dir, rec); err != nil {
		t.Fatal(err)
	}
	got, found, err := ReadRecord(dir)
	if err != nil || !found {
		t.Fatalf("ReadRecord found=%v err=%v", found, err)
	}
	if got.Endpoint != rec.Endpoint || got.PID != rec.PID || got.Restarts != 2 || got.Version != 1 {
		t.Fatalf("round trip = %+v", got)
	}
	// No token may ever be written to this file.
	raw, err := os.ReadFile(recordPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "token") {
		t.Fatalf("record mentions a token: %s", raw)
	}
	if err := RemoveRecord(dir); err != nil {
		t.Fatal(err)
	}
	if _, found, err := ReadRecord(dir); found || err != nil {
		t.Fatalf("record survived RemoveRecord found=%v err=%v", found, err)
	}
	// Removing twice is not an error: Stop can run after a previous cleanup.
	if err := RemoveRecord(dir); err != nil {
		t.Fatal(err)
	}
}

func TestNonLoopbackRecordIsIgnored(t *testing.T) {
	dir := t.TempDir()
	if err := WriteRecord(dir, Record{Endpoint: "http://10.0.0.5:18766", PID: 1}); err != nil {
		t.Fatal(err)
	}
	rec, found, err := ReadRecord(dir)
	if found {
		t.Fatalf("non-loopback record was trusted: %+v", rec)
	}
	if err == nil {
		t.Fatal("non-loopback record should be reported (so the caller can WARN)")
	}
}

func TestSweepRenamesStaleLoopbackSessionMarkers(t *testing.T) {
	data := t.TempDir()
	current := "http://127.0.0.1:18766"
	old := "http://127.0.0.1:18767"

	stalePath := writeMarkerFile(t, filepath.Join(data, "projects", "p1", "cases", "in_a"), old, LocalAgent, "sess-a")
	livePath := writeMarkerFile(t, filepath.Join(data, "projects", "p1", "cases", "in_b"), current, LocalAgent, "sess-b")
	commanderPath := writeMarkerFile(t, filepath.Join(data, "projects", "p1", "commander"), old, LocalAgent, "sess-c")

	invalidPath := filepath.Join(data, "projects", "p1", "cases", "in_c", ".tw", "aop-session.json")
	if err := os.MkdirAll(filepath.Dir(invalidPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(invalidPath, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	swept, err := SweepStaleSessions(context.Background(), data, current, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if swept.Cleaned != 3 || swept.Failed != 0 || swept.Kept != 1 {
		t.Fatalf("swept = %+v", swept)
	}
	for _, path := range []string{stalePath, commanderPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("stale marker %s still present", path)
		}
		if _, err := os.Stat(path + staleSuffix); err != nil {
			t.Fatalf("stale marker %s was not renamed: %v", path, err)
		}
	}
	if _, err := os.Stat(invalidPath + staleInvalidSuffix); err != nil {
		t.Fatalf("invalid marker was not renamed: %v", err)
	}
	// The live endpoint's marker must be left alone.
	if _, err := os.Stat(livePath); err != nil {
		t.Fatalf("live marker was touched: %v", err)
	}
}

// An agent rebuild is stale even when the endpoint is unchanged: the session
// belongs to an agent that no longer exists on that host.
func TestSweepRenamesMarkerWithUnknownAgent(t *testing.T) {
	data := t.TempDir()
	current := "http://127.0.0.1:18766"
	path := writeMarkerFile(t, filepath.Join(data, "projects", "p1", "cases", "in_a"), current, "worker-old", "sess-a")
	swept, err := SweepStaleSessions(context.Background(), data, current, nil)
	if err != nil {
		t.Fatal(err)
	}
	if swept.Cleaned != 1 {
		t.Fatalf("swept = %+v", swept)
	}
	if _, err := os.Stat(path + staleSuffix); err != nil {
		t.Fatalf("marker with an unknown agent not renamed: %v", err)
	}
}

func TestSweepLeavesRemoteNodeMarkersAlone(t *testing.T) {
	data := t.TempDir()
	remote := "http://10.10.2.5:18766"
	path := writeMarkerFile(t, filepath.Join(data, "projects", "p1", "cases", "in_remote"), remote, "local", "sess-r")
	swept, err := SweepStaleSessions(context.Background(), data, "http://127.0.0.1:18766", nil)
	if err != nil {
		t.Fatal(err)
	}
	if swept.Cleaned != 0 || swept.Kept != 1 {
		t.Fatalf("swept = %+v", swept)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("remote node marker was touched: %v", err)
	}
}

// The ready host persists its endpoint/PID; a clean Stop removes the record.
func TestHostPersistsAndRemovesRecord(t *testing.T) {
	dir := t.TempDir()
	var launcher testLauncher
	agents := &testAgents{decide: func(int) ([]aop.Agent, error) { return readyAgents(dir), nil }}
	installSeams(t, &launcher, agents)
	cfg := testHostConfig(t, dir, &testLog{})
	cfg.Persist = true
	h, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	rec, found, err := ReadRecord(dir)
	if err != nil || !found {
		t.Fatalf("ready host did not persist a record: found=%v err=%v", found, err)
	}
	if rec.Endpoint != h.Endpoint() || rec.PID != h.PID() || rec.Dir != dir {
		t.Fatalf("record = %+v, host endpoint=%s pid=%d", rec, h.Endpoint(), h.PID())
	}
	h.Stop()
	if _, found, err := ReadRecord(dir); found || err != nil {
		t.Fatalf("clean stop left the record behind: found=%v err=%v", found, err)
	}
}
