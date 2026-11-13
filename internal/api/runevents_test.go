package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A run id is one path segment. %2F is decoded before PathValue, so
// `/api/runs/..%2Fsecret/events` used to resolve outside data/runs and return
// any *.jsonl on the machine.
func TestRunEventsRefusesPathTraversal(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "runs"), 0o755); err != nil {
		t.Fatal(err)
	}
	secret := `{"kind":"message","role":"assistant","text":"OUTSIDE"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "secret.jsonl"), []byte(secret), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Server{DataDir: dir}
	srv := httptest.NewServer(s.eventsMux())
	defer srv.Close()

	res, err := http.Get(srv.URL + "/api/runs/..%2Fsecret/events")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", res.StatusCode, body)
	}
	if strings.Contains(string(body), "OUTSIDE") {
		t.Fatalf("the handler served a file outside data/runs: %s", body)
	}
}

func TestRunEventsServesARealRunJournal(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "runs"), 0o755); err != nil {
		t.Fatal(err)
	}
	journal := `{"kind":"tool_call","tool":"bash"}` + "\n" + `{"kind":"turn_ended","stop_reason":"completed"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "runs", "r_abc.jsonl"), []byte(journal), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Server{DataDir: dir}
	srv := httptest.NewServer(s.eventsMux())
	defer srv.Close()

	res, err := http.Get(srv.URL + "/api/runs/r_abc/events")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", res.StatusCode, body)
	}
	if !strings.Contains(string(body), "turn_ended") || !strings.Contains(string(body), "tool_call") {
		t.Fatalf("journal not returned: %s", body)
	}
}

// eventsMux is the one route the test needs, registered the way Register does.
func (s *Server) eventsMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/runs/{id}/events", s.runEvents)
	return mux
}
