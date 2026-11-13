package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const twoProfiles = `llm:
    active_profile: fast
    providers:
        - id: slow
          name: Slow
          provider: openai
          base_url: http://slow:8080
          api_key: slow-secret
          model: slow-model
          proxy: http://proxy:7890
          timeout: 300
          images: true
        - id: fast
          name: Fast
          provider: anthropic
          base_url: http://fast:8080
          api_key: fast-secret
          model: fast-model
          context_window: 64000
          max_tokens: 8192
agent:
    timeout: 60
extensions:
    custom.profile:
        workspace: /srv/project
`

func loadProfiles(t *testing.T, body string) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	harness := filepath.Join(dir, "cyber.yaml")
	if body != "" {
		writeTestConfig(t, harness, body)
	}
	s, err := LoadAt(filepath.Join(dir, "settings.json"), harness)
	if err != nil {
		t.Fatal(err)
	}
	return s, harness
}

func TestProfilesListWithoutCredentials(t *testing.T) {
	s, harness := loadProfiles(t, twoProfiles)
	cat, err := s.Profiles()
	if err != nil {
		t.Fatal(err)
	}
	if cat.ActiveProfile != "fast" || cat.Path != harness || cat.HasOverrides {
		t.Fatalf("catalog = %+v", cat)
	}
	if len(cat.Profiles) != 2 {
		t.Fatalf("profiles = %d", len(cat.Profiles))
	}
	slow := cat.Profiles[0]
	if slow.ID != "slow" || !slow.APIKeySet || !slow.Runnable || slow.Active ||
		slow.Revision == "" || slow.Proxy != "http://proxy:7890" || slow.Timeout != 300 {
		t.Fatalf("slow = %+v", slow)
	}
	if strings.Contains(string(mustJSON(t, cat)), "slow-secret") {
		t.Fatal("catalog leaked an api key")
	}
	if !cat.Profiles[1].Active || cat.Profiles[1].Model != "fast-model" {
		t.Fatalf("fast = %+v", cat.Profiles[1])
	}
}

func TestProfilesReportDuplicateAndDanglingActive(t *testing.T) {
	cases := map[string]string{
		"duplicate": `llm:
    providers:
        - {id: a, model: m}
        - {id: a, model: n}
`,
		"dangling": `llm:
    active_profile: missing
    providers:
        - {id: a, model: m}
`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			s, _ := loadProfiles(t, body)
			if _, err := s.Profiles(); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

// An update must touch only the fields the console owns: another profile, the
// unknown `timeout`/`images` keys and non-llm sections survive.
func TestSaveProfilePreservesUnknownFieldsAndStoredKey(t *testing.T) {
	s, harness := loadProfiles(t, twoProfiles)
	saved, err := s.SaveProfile(Profile{
		ID: "slow", Name: "Slow edited", Provider: "openai",
		BaseURL: "http://slow2:8080", Model: "slow-v2", Proxy: "http://proxy2:7890",
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.APIKey != "slow-secret" || saved.Model != "slow-v2" || saved.Timeout != 300 || saved.Images == nil || !*saved.Images {
		t.Fatalf("saved = %+v", saved)
	}
	data, err := os.ReadFile(harness)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"agent:", "custom.profile:", "timeout: 300", "images: true", "fast-secret"} {
		if !strings.Contains(text, want) {
			t.Fatalf("file lost %q:\n%s", want, text)
		}
	}
	if err := yaml.Unmarshal(data, new(map[string]any)); err != nil {
		t.Fatal(err)
	}
}

func TestSaveProfileRefusesEmptyKeyOnNewEntry(t *testing.T) {
	s, _ := loadProfiles(t, twoProfiles)
	if _, err := s.SaveProfile(Profile{ID: "new", BaseURL: "http://n:1", Model: "m"}); err == nil {
		t.Fatal("want api_key error")
	}
	if _, err := s.SaveProfile(Profile{ID: "bad/id", BaseURL: "http://n:1", Model: "m", APIKey: "k"}); err == nil {
		t.Fatal("want id error")
	}
}

func TestDeleteProfileRefusesActive(t *testing.T) {
	s, harness := loadProfiles(t, twoProfiles)
	if err := s.DeleteProfile("fast"); err == nil {
		t.Fatal("deleted the active profile")
	}
	if err := s.DeleteProfile("slow"); err != nil {
		t.Fatal(err)
	}
	cat, err := s.Profiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Profiles) != 1 || cat.Profiles[0].ID != "fast" {
		t.Fatalf("profiles = %+v", cat.Profiles)
	}
	if !strings.Contains(readFile(t, harness), "fast-secret") {
		t.Fatal("delete removed the wrong entry")
	}
}

// A profile host gets only its own provider, its own active_profile and no
// flat llm.* overrides; every other section survives.
func TestWriteProfileConfigIsolatesOneProfile(t *testing.T) {
	s, _ := loadProfiles(t, `llm:
    provider: flat
    base_url: http://flat:1
    model: flat-model
    active_profile: fast
    providers:
        - id: slow
          provider: openai
          base_url: http://slow:8080
          api_key: slow-secret
          model: slow-model
          timeout: 300
        - id: fast
          provider: anthropic
          base_url: http://fast:8080
          api_key: fast-secret
          model: fast-model
agent:
    timeout: 60
extensions:
    custom.profile:
        workspace: /srv/project
`)
	dest := filepath.Join(t.TempDir(), "profiles", "slow.yaml")
	if err := s.WriteProfileConfig("slow", dest); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		LLM struct {
			ActiveProfile string           `yaml:"active_profile"`
			Provider      string           `yaml:"provider"`
			BaseURL       string           `yaml:"base_url"`
			Providers     []map[string]any `yaml:"providers"`
		} `yaml:"llm"`
		Agent struct {
			Timeout int `yaml:"timeout"`
		} `yaml:"agent"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.LLM.ActiveProfile != "slow" || doc.LLM.Provider != "" || doc.LLM.BaseURL != "" {
		t.Fatalf("llm = %+v", doc.LLM)
	}
	if len(doc.LLM.Providers) != 1 || doc.LLM.Providers[0]["id"] != "slow" ||
		doc.LLM.Providers[0]["api_key"] != "slow-secret" {
		t.Fatalf("providers = %+v", doc.LLM.Providers)
	}
	if doc.Agent.Timeout != 60 || !strings.Contains(string(data), "custom.profile:") {
		t.Fatalf("other sections lost:\n%s", data)
	}
	st, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", st.Mode().Perm())
	}
}

// The host configuration is the harness's file and the harness parses it
// strictly: Tianwu-only pricing metadata must stay in the operator's file and
// never be copied into the per-profile configuration a host is started with.
func TestWriteProfileConfigOmitsPricingMetadata(t *testing.T) {
	s, harness := loadProfiles(t, `llm:
    active_profile: priced
    providers:
        - id: priced
          provider: openai
          base_url: http://priced:8080
          api_key: k
          model: m
          pricing:
              currency: USD
              input_per_mtoken: 1.5
              output_per_mtoken: 6
              cached_input_per_mtoken: 0.3
`)
	dest := filepath.Join(t.TempDir(), "priced.yaml")
	if err := s.WriteProfileConfig("priced", dest); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "pricing") || strings.Contains(string(data), "input_per_mtoken") {
		t.Fatalf("host config carries Tianwu-only pricing metadata:\n%s", data)
	}
	if !strings.Contains(string(data), "model: m") || !strings.Contains(string(data), "api_key: k") {
		t.Fatalf("host config lost harness fields:\n%s", data)
	}
	if src, err := os.ReadFile(harness); err != nil || !strings.Contains(string(src), "input_per_mtoken") {
		t.Fatal("operator's file must keep the pricing entry")
	}
}

func TestLegacyPricingIsOpaqueAndSurvivesProfileEdits(t *testing.T) {
	s, path := loadProfiles(t, `llm:
    active_profile: priced
    providers:
        - id: priced
          provider: openai
          base_url: http://priced:8080
          api_key: k
          model: m
          pricing:
              currency: USD
              input_per_mtoken: 1.5
              output_per_mtoken: 6
              cached_input_per_mtoken: 0.3
`)
	p, err := s.SavedProfile("priced")
	if err != nil {
		t.Fatal(err)
	}
	cat, err := s.Profiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Profiles) != 1 {
		t.Fatal("missing profile")
	}
	p.Name = "renamed"
	if _, err := s.SaveProfile(p); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "input_per_mtoken: 1.5") {
		t.Fatalf("legacy metadata lost: %s %v", data, err)
	}
	view, err := s.Profiles()
	encoded, _ := json.Marshal(view)
	if err != nil || strings.Contains(string(encoded), "pricing") {
		t.Fatalf("legacy money exposed: %s %v", encoded, err)
	}
}

// A profile whose credential exists only as an api_keys pool is still a
// configured model: the default local host must start with the pool's current
// key, and a rotation must be visible to the next Effective()/Env() read.
func TestEffectiveUsesPoolKeyWhenProfileHasNoSingleKey(t *testing.T) {
	s, _ := loadProfiles(t, `llm:
    active_profile: pooled
    providers:
        - id: pooled
          provider: openai
          base_url: http://pooled:8080
          api_keys:
              - key-one
              - key-two
          model: pooled-model
`)
	eff := s.Effective()
	if eff.APIKey != "key-one" || !eff.Configured() {
		t.Fatalf("effective = %+v configured=%v", eff, eff.Configured())
	}
	if !strings.Contains(strings.Join(eff.Env(), "\n"), "CYBER_API_KEY=key-one") {
		t.Fatalf("env does not carry the pool key: %v", eff.Env())
	}
	if _, rotated, err := s.RotateProfileKey("pooled"); err != nil || !rotated {
		t.Fatalf("rotate = rotated:%v err:%v", rotated, err)
	}
	if got := s.Effective().APIKey; got != "key-two" {
		t.Fatalf("effective key after rotation = %q, want key-two", got)
	}
}

func TestProfileRevisionTracksBindingNotKeyValue(t *testing.T) {
	base := Profile{ID: "a", BaseURL: "http://x:1", Model: "m", APIKey: "one"}
	if base.Revision() != (Profile{ID: "a", BaseURL: "http://x:1", Model: "m", APIKey: "two"}).Revision() {
		t.Fatal("key rotation changed the revision")
	}
	if base.Revision() == (Profile{ID: "a", BaseURL: "http://x:1", Model: "n", APIKey: "one"}).Revision() {
		t.Fatal("model change kept the revision")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
