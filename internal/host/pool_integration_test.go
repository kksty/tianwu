package host

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/settings"
)

// TestProfileHostsUseTheirOwnEndpoint is the P1 acceptance check that needs the
// real fork binary: two fake OpenAI-compatible gateways, two saved profiles, two
// hosts. Each host must call its own base URL with its own key and model, and
// never the other's.
//
// Skipped by default; run with:
//
//	TW_AISCAN_BIN=<fork>/aiscan-full.exe go test ./internal/host -run ProfileHosts -v
func TestProfileHostsUseTheirOwnEndpoint(t *testing.T) {
	bin := strings.TrimSpace(os.Getenv("TW_AISCAN_BIN"))
	if bin == "" {
		t.Skip("set TW_AISCAN_BIN to the fork aiscan-full to run the two-host check")
	}
	type hit struct {
		auth  string
		model string
	}
	var mu sync.Mutex
	calls := map[string][]hit{}
	gateway := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			var req struct {
				Model string `json:"model"`
			}
			_ = json.Unmarshal(body, &req)
			mu.Lock()
			calls[name] = append(calls[name], hit{auth: r.Header.Get("Authorization"), model: req.Model})
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"id":"x","object":"chat.completion","created":0,"model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`, req.Model)
		}))
	}
	a, b := gateway("a"), gateway("b")
	defer a.Close()
	defer b.Close()

	dir := t.TempDir()
	harness := filepath.Join(dir, "cyber.yaml")
	body := fmt.Sprintf(`llm:
    active_profile: a
    providers:
        - id: a
          provider: openai
          base_url: %s/v1
          api_key: key-a
          model: model-a
        - id: b
          provider: openai
          base_url: %s/v1
          api_key: key-b
          model: model-b
`, a.URL, b.URL)
	if err := os.WriteFile(harness, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := settings.LoadAt(filepath.Join(dir, "settings.json"), harness)
	if err != nil {
		t.Fatal(err)
	}
	pool := NewPool(PoolConfig{Bin: bin, Dir: dir, Settings: store, Logf: t.Logf})
	defer pool.Stop()
	exA, err := pool.Executor("a")
	if err != nil {
		t.Fatal(err)
	}
	exB, err := pool.Executor("b")
	if err != nil {
		t.Fatal(err)
	}
	if exA.Name() == exB.Name() {
		t.Fatalf("profiles share one host: %s", exA.Name())
	}

	waitForCalls := func(name string) []hit {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			got := append([]hit(nil), calls[name]...)
			mu.Unlock()
			if len(got) > 0 {
				return got
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("gateway %s was never called", name)
		return nil
	}
	for name, want := range map[string][2]string{
		"a": {"Bearer key-a", "model-a"},
		"b": {"Bearer key-b", "model-b"},
	} {
		for _, call := range waitForCalls(name) {
			if call.auth != want[0] || call.model != want[1] {
				t.Fatalf("gateway %s got auth=%q model=%q, want %q/%q", name, call.auth, call.model, want[0], want[1])
			}
		}
	}
	// No cross-traffic: neither gateway may have seen the other profile's key.
	mu.Lock()
	defer mu.Unlock()
	for name, forbidden := range map[string]string{"a": "key-b", "b": "key-a"} {
		for _, call := range calls[name] {
			if strings.Contains(call.auth, forbidden) {
				t.Fatalf("gateway %s saw the other profile's key", name)
			}
		}
	}
}
