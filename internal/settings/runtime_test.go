package settings

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRuntimeConfigFiltersAllProfilesAndPreservesSource(t *testing.T) {
	s, source := loadProfiles(t, `llm:
    active_profile: fast
    base_url: http://flat:8080
    providers:
        - id: fast
          paused: true
          provider: openai
          base_url: http://fast:8080
          model: fast-model
          api_key: fixture-fast
          tier: strong
          max_concurrency: 2
          fallback: slow
          pricing:
            currency: USD
            input_per_1m: 1
        - id: slow
          paused: true
          provider: openai
          base_url: http://slow:8080
          model: slow-model
          api_keys: [fixture-slow-one, fixture-slow-two]
          proxy: http://proxy:7890
          timeout: 300
agent:
    timeout: 60
misc:
    data_dir: harness-data
extensions:
    fixture:
        enabled: true
`)
	before, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	path, err := s.RuntimeConfig(filepath.Join(t.TempDir(), "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		LLM struct {
			ActiveProfile string           `yaml:"active_profile"`
			BaseURL       string           `yaml:"base_url"`
			Providers     []map[string]any `yaml:"providers"`
		} `yaml:"llm"`
		Agent      map[string]any `yaml:"agent"`
		Misc       map[string]any `yaml:"misc"`
		Extensions map[string]any `yaml:"extensions"`
	}
	if err := yaml.Unmarshal([]byte(readFile(t, path)), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.LLM.ActiveProfile != "fast" || doc.LLM.BaseURL != "http://flat:8080" || len(doc.LLM.Providers) != 2 || doc.Agent["timeout"] != 60 || doc.Extensions["fixture"] == nil {
		t.Fatalf("runtime configuration lost user fields: %+v", doc)
	}
	if doc.Misc["data_dir"] != filepath.Join(filepath.Dir(source), "harness-data") {
		t.Fatalf("runtime configuration relocated harness data: %v", doc.Misc["data_dir"])
	}
	for _, p := range doc.LLM.Providers {
		for _, key := range []string{"paused", "pricing", "tier", "api_keys", "max_concurrency", "fallback"} {
			if _, ok := p[key]; ok {
				t.Fatalf("profile %s leaked %s into harness configuration", p["id"], key)
			}
		}
	}
	if doc.LLM.Providers[1]["api_key"] != "fixture-slow-one" || doc.LLM.Providers[1]["proxy"] != "http://proxy:7890" || doc.LLM.Providers[1]["timeout"] != 300 {
		t.Fatal("runtime profile lost its effective credential or harness options")
	}
	if after := readFile(t, source); after != string(before) {
		t.Fatal("runtime materialisation changed the operator's configuration")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("runtime configuration permissions=%v", info.Mode().Perm())
	}
}

func TestRuntimeConfigPathTracksRuntimeChangesOnly(t *testing.T) {
	s, _ := loadProfiles(t, twoProfiles)
	dir := t.TempDir()
	before, err := s.RuntimeConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetProfilePaused("fast", true); err != nil {
		t.Fatal(err)
	}
	paused, err := s.RuntimeConfig(dir)
	if err != nil || paused != before {
		t.Fatalf("admission change replaced runtime configuration: %s, %v", paused, err)
	}
	p, err := s.SavedProfile("fast")
	if err != nil {
		t.Fatal(err)
	}
	p.Proxy = "http://changed-proxy:7890"
	if _, err := s.SaveProfile(p); err != nil {
		t.Fatal(err)
	}
	changed, err := s.RuntimeConfig(dir)
	if err != nil || changed == before {
		t.Fatalf("runtime change kept stale configuration: %s, %v", changed, err)
	}
	if _, err := os.Stat(before); err != nil {
		t.Fatal("old runtime snapshot was removed while a host could still use it")
	}
}

func TestRuntimeConfigWithoutSavedHarnessFile(t *testing.T) {
	for _, harness := range []string{"", filepath.Join(t.TempDir(), "missing.yaml")} {
		s, err := LoadAt(filepath.Join(t.TempDir(), "settings.json"), harness)
		if err != nil {
			t.Fatal(err)
		}
		path, err := s.RuntimeConfig(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := yaml.Unmarshal([]byte(readFile(t, path)), &doc); err != nil || !reflect.DeepEqual(doc, map[string]any{}) {
			t.Fatalf("empty harness snapshot=%v err=%v", doc, err)
		}
	}
}
