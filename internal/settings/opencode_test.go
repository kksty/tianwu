package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenCodeGoProfileKeepsSubscriptionRouteThroughHostConfig(t *testing.T) {
	s, _ := loadProfiles(t, "")
	base := "https://opencode.ai/inference/go/openai/v1"
	p, err := s.SaveProfile(Profile{ID: "opencode-go", Provider: "openai", BaseURL: base + "/chat/completions/", APIKey: "fixture-key", Model: "fixture-model"})
	if err != nil {
		t.Fatal(err)
	}
	if p.BaseURL != base {
		t.Fatalf("saved base = %q", p.BaseURL)
	}
	if got := modelListURL(base + "/chat/completions/"); got != base+"/models" {
		t.Fatalf("catalogue = %q", got)
	}
	dest := filepath.Join(t.TempDir(), "host.yaml")
	if err := s.WriteProfileConfig(p.ID, dest); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "base_url: "+base) || strings.Contains(string(body), "/chat/completions") {
		t.Fatalf("host base changed: %s", body)
	}
}

func TestOpenCodeMeteredProfileIsNotConvertedToGo(t *testing.T) {
	s, _ := loadProfiles(t, "")
	base := "https://opencode.ai/inference/openai/v1"
	p, err := s.SaveProfile(Profile{ID: "opencode-metered", Provider: "openai", BaseURL: base, APIKey: "fixture-key", Model: "fixture-model"})
	if err != nil {
		t.Fatal(err)
	}
	if p.BaseURL != base {
		t.Fatalf("billing route changed: %q", p.BaseURL)
	}
}

func TestOpenCodeCatalogueLive(t *testing.T) {
	key := strings.TrimSpace(os.Getenv("TW_TEST_OPENCODE_KEY"))
	if key == "" {
		t.Skip("set TW_TEST_OPENCODE_KEY to verify the Go catalogue")
	}
	out := fetchCatalogue("https://opencode.ai/inference/go/openai/v1/chat/completions", key)
	if out.err != "" {
		t.Fatal("OpenCode catalogue request failed")
	}
	if len(out.models) == 0 {
		t.Fatal("OpenCode catalogue is empty")
	}
	if out.endpoint != "https://opencode.ai/inference/go/openai/v1/models" {
		t.Fatalf("endpoint = %q", out.endpoint)
	}
	t.Logf("OpenCode Go catalogue returned %d models", len(out.models))
}
