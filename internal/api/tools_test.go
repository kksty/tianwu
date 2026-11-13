package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func getTools(t *testing.T, s *Server) toolsResponse {
	t.Helper()
	mux := http.NewServeMux()
	s.Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	res, err := http.Get(srv.URL + "/api/tools")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	var got toolsResponse
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	return got
}

// TestToolLeaderboardCountsCalls pins the ranking source: tool_call events in
// the run logs, not facts (a call that found nothing still cost a turn).
func TestToolLeaderboardCountsCalls(t *testing.T) {
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	if err := os.MkdirAll(runs, 0o755); err != nil {
		t.Fatal(err)
	}
	r1 := `
{"kind":"tool_call","tool":"bash","at":"2026-01-01T00:00:00Z"}
{"kind":"tool_result","tool":"bash","at":"2026-01-01T00:00:01Z"}
{"kind":"tool_call","tool":"bash","at":"2026-01-02T00:00:00Z"}
not json
`
	if err := os.WriteFile(filepath.Join(runs, "r1.jsonl"), []byte(r1), 0o644); err != nil {
		t.Fatal(err)
	}
	r2 := `{"kind":"tool_call","tool":"rg","at":"2026-01-03T00:00:00Z"}
`
	if err := os.WriteFile(filepath.Join(runs, "r2.jsonl"), []byte(r2), 0o644); err != nil {
		t.Fatal(err)
	}

	s := New(nil, nil, dir, filepath.Join(dir, "cases"))
	s.ArsenalDirs = []string{filepath.Join(dir, "no-arsenal")}
	got := getTools(t, s)

	if got.RunsScanned != 2 {
		t.Fatalf("runs_scanned = %d, want 2", got.RunsScanned)
	}
	if len(got.Leaderboard) != 2 {
		t.Fatalf("leaderboard = %+v", got.Leaderboard)
	}
	top := got.Leaderboard[0]
	if top.Tool != "bash" || top.Calls != 2 || top.Runs != 1 {
		t.Fatalf("top = %+v, want bash 2 calls in 1 run", top)
	}
	if top.LastAt == nil || top.LastAt.UTC().Format("2006-01-02") != "2026-01-02" {
		t.Fatalf("last_at = %v", top.LastAt)
	}
	if got.Leaderboard[1].Tool != "rg" || got.Leaderboard[1].Calls != 1 {
		t.Fatalf("second = %+v", got.Leaderboard[1])
	}

	// Append to a run log: the cache must notice the size change.
	f, err := os.OpenFile(filepath.Join(runs, "r1.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"kind":"tool_call","tool":"bash","at":"2026-01-04T00:00:00Z"}` + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	again := getTools(t, s)
	if again.Leaderboard[0].Calls != 3 {
		t.Fatalf("after append calls = %d, want 3", again.Leaderboard[0].Calls)
	}
}

// TestArsenalToolsReadsManifest: the page shows what is installed, with the
// version the arsenal manifest recorded (not guessed from the filename).
func TestArsenalToolsReadsManifest(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "arsenal")
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "rg.exe"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := `{"rg":{"version":"15.2.0","installed_at":"2026-11-01T13:58:30+08:00","source":"bundle","managed_by":"cyber-audit"}}`
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	s := New(nil, nil, dir, filepath.Join(dir, "cases"))
	s.ArsenalDirs = []string{root}
	got := getTools(t, s)

	if len(got.Arsenal) != 1 {
		t.Fatalf("arsenal = %+v", got.Arsenal)
	}
	row := got.Arsenal[0]
	if row.Name != "rg" || row.Version != "15.2.0" || row.Source != "bundle" || row.ManagedBy != "cyber-audit" {
		t.Fatalf("row = %+v", row)
	}
	if len(row.Dirs) != 1 || row.Dirs[0] != bin {
		t.Fatalf("dirs = %v, want [%q]", row.Dirs, bin)
	}
	if len(got.ArsenalDirs) != 1 || got.ArsenalDirs[0] != bin {
		t.Fatalf("arsenal_dirs = %v", got.ArsenalDirs)
	}
}
