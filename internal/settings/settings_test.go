package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestEffectiveFallsBackToEnv(t *testing.T) {
	t.Setenv("TW_HARNESS_CONFIG", filepath.Join(t.TempDir(), "cyber.yaml"))
	t.Setenv("CYBER_BASE_URL", "http://env:8080")
	t.Setenv("CYBER_API_KEY", "k")
	t.Setenv("CYBER_MODEL", "m")
	s, err := Load(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	m := s.Effective()
	if !m.Configured() || m.BaseURL != "http://env:8080" || m.Model != "m" {
		t.Fatalf("effective = %+v", m)
	}
}

// The console shows the harness's active profile, not a private copy of it.
func TestHarnessReadsTheActiveProfile(t *testing.T) {
	dir := t.TempDir()
	harness := filepath.Join(dir, "cyber.yaml")
	writeTestConfig(t, harness, `llm:
    active_profile: fast
    providers:
        - id: slow
          name: Slow
          provider: openai
          base_url: http://slow:8080
          api_key: slow-key
          model: slow-model
        - id: fast
          name: Fast
          provider: anthropic
          base_url: http://fast:8080
          api_key: fast-key
          model: fast-model
          context_window: 64000
          max_tokens: 8192
agent:
    timeout: 60
`)

	s, err := LoadAt(filepath.Join(dir, "settings.json"), harness)
	if err != nil {
		t.Fatal(err)
	}
	m := s.Effective()
	if m.BaseURL != "http://fast:8080" || m.APIKey != "fast-key" || m.Model != "fast-model" ||
		m.Provider != "anthropic" || m.ContextWindow != 64000 || m.MaxTokens != 8192 {
		t.Fatalf("effective = %+v", m)
	}
	if s.Path() != harness || s.Profile() != "fast" {
		t.Fatalf("path/profile = %q / %q", s.Path(), s.Profile())
	}
	if err := s.HarnessError(); err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
}

// Flat llm.* fields win over the profile in the harness merge, so they win here.
func TestHarnessFlatFieldsOverrideProfile(t *testing.T) {
	dir := t.TempDir()
	harness := filepath.Join(dir, "cyber.yaml")
	writeTestConfig(t, harness, `llm:
    provider: openai
    base_url: http://flat:8080
    model: flat-model
    providers:
        - id: default
          provider: openai
          base_url: http://profile:8080
          model: profile-model
`)

	s, err := LoadAt(filepath.Join(dir, "settings.json"), harness)
	if err != nil {
		t.Fatal(err)
	}
	m := s.Effective()
	if m.BaseURL != "http://flat:8080" || m.Model != "flat-model" {
		t.Fatalf("effective = %+v", m)
	}
}

// Saving edits the active profile in place and leaves every other section and
// profile byte-for-byte meaningful (the document round-trips through YAML).
func TestSaveEditsActiveProfileAndKeepsTheRest(t *testing.T) {
	dir := t.TempDir()
	harness := filepath.Join(dir, "cyber.yaml")
	writeTestConfig(t, harness, `llm:
    active_profile: fast
    providers:
        - id: slow
          provider: openai
          base_url: http://slow:8080
          api_key: slow-key
          model: slow-model
        - id: fast
          provider: openai
          base_url: http://old:8080
          api_key: old-key
          model: old-model
          proxy: http://proxy:3128
          timeout: 90
agent:
    timeout: 60
    tools:
        - shell
`)

	s, err := LoadAt(filepath.Join(dir, "settings.json"), harness)
	if err != nil {
		t.Fatal(err)
	}
	// An empty api_key means "keep the stored one" (ApplyView does that merge).
	next := ApplyView(s.Effective(), View{
		BaseURL:       "http://new:8080",
		Model:         "new-model",
		Provider:      "anthropic",
		ContextWindow: 128000,
		MaxTokens:     4096,
	})
	if err := s.Save(File{Model: next}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(harness)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("saved file is not YAML: %v\n%s", err, data)
	}
	llm := doc["llm"].(map[string]any)
	if llm["active_profile"] != "fast" {
		t.Fatalf("active_profile = %v", llm["active_profile"])
	}
	profiles := llm["providers"].([]any)
	if len(profiles) != 2 {
		t.Fatalf("profiles = %v", profiles)
	}
	fast := profiles[1].(map[string]any)
	for key, want := range map[string]any{
		"provider":       "anthropic",
		"base_url":       "http://new:8080",
		"api_key":        "old-key",
		"model":          "new-model",
		"context_window": 128000,
		"max_tokens":     4096,
		"proxy":          "http://proxy:3128",
		"timeout":        90,
	} {
		if fast[key] != want {
			t.Fatalf("fast.%s = %v, want %v", key, fast[key], want)
		}
	}
	slow := profiles[0].(map[string]any)
	if slow["model"] != "slow-model" || slow["base_url"] != "http://slow:8080" {
		t.Fatalf("inactive profile changed: %v", slow)
	}
	agent := doc["agent"].(map[string]any)
	if agent["timeout"] != 60 || len(agent["tools"].([]any)) != 1 {
		t.Fatalf("unrelated section changed: %v", agent)
	}
	if !strings.HasSuffix(s.Path(), "cyber.yaml") {
		t.Fatalf("path = %q", s.Path())
	}
}

// A broken harness file must not take the console down: the effective values
// fall back and the view says why.
func TestBrokenHarnessFileFallsBack(t *testing.T) {
	dir := t.TempDir()
	harness := filepath.Join(dir, "cyber.yaml")
	writeTestConfig(t, harness, "llm: [broken\n")
	legacy := filepath.Join(dir, "settings.json")
	writeTestConfig(t, legacy, `{"model":{"base_url":"http://legacy:8080","api_key":"k","model":"legacy"}}`)

	s, err := LoadAt(legacy, harness)
	if err != nil {
		t.Fatal(err)
	}
	if m := s.Effective(); m.Model != "legacy" {
		t.Fatalf("fallback = %+v", m)
	}
	if v := s.View(); v.HarnessError == "" {
		t.Fatal("view hid the parse error")
	}
}

func writeTestConfig(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The auxiliary concurrency gate is a runtime setting: it lives in the legacy
// settings.json under runtime.auxiliary_concurrency, defaults to 1 (the
// historic behaviour), survives a reload, and rejects values outside 1..8.
// The harness file (cyber.yaml) is the model configuration and must not gain
// the field.
func TestRuntimeSettingsDefaultPersistAndValidate(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "settings.json")
	harness := filepath.Join(dir, "cyber.yaml")
	writeTestConfig(t, harness, "agent:\n    timeout: 60\n")

	s, err := LoadAt(legacy, harness)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.RuntimeSettings().AuxiliaryConcurrency; got != 1 {
		t.Fatalf("default auxiliary_concurrency = %d, want 1", got)
	}

	if err := s.SetRuntime(Runtime{AuxiliaryConcurrency: 3}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(legacy)
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Runtime struct {
			AuxiliaryConcurrency int `json:"auxiliary_concurrency"`
		} `json:"runtime"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("settings.json is not JSON: %v\n%s", err, data)
	}
	if file.Runtime.AuxiliaryConcurrency != 3 {
		t.Fatalf("persisted runtime.auxiliary_concurrency = %d, want 3\n%s",
			file.Runtime.AuxiliaryConcurrency, data)
	}
	// The runtime knob never leaks into the harness's own file.
	hdata, err := os.ReadFile(harness)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(hdata), "auxiliary_concurrency") {
		t.Fatalf("harness file gained a runtime setting:\n%s", hdata)
	}
	if !strings.Contains(string(hdata), "timeout: 60") {
		t.Fatalf("harness file was rewritten:\n%s", hdata)
	}

	// It survives a store reload (a process restart).
	reloaded, err := LoadAt(legacy, harness)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.RuntimeSettings().AuxiliaryConcurrency; got != 3 {
		t.Fatalf("reloaded auxiliary_concurrency = %d, want 3", got)
	}

	// Out-of-range values are rejected before anything is written.
	for _, n := range []int{-1, 0, 9, 100} {
		if err := s.SetRuntime(Runtime{AuxiliaryConcurrency: n}); err == nil {
			t.Fatalf("SetRuntime(%d) was accepted", n)
		}
	}
	if got := s.RuntimeSettings().AuxiliaryConcurrency; got != 3 {
		t.Fatalf("rejected SetRuntime changed the value to %d", got)
	}
	again, err := LoadAt(legacy, harness)
	if err != nil {
		t.Fatal(err)
	}
	if got := again.RuntimeSettings().AuxiliaryConcurrency; got != 3 {
		t.Fatalf("rejected SetRuntime reached disk: %d", got)
	}
}

// A hand-edited file is clamped, never a crash: absent or non-positive reads as
// 1 and a value above the maximum is clamped to it.
func TestRuntimeSettingsClampsStoredValues(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "settings.json")
	writeTestConfig(t, legacy, `{"model":{},"runtime":{"auxiliary_concurrency":99}}`)
	s, err := LoadAt(legacy, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.RuntimeSettings().AuxiliaryConcurrency; got != MaxAuxiliaryConcurrency {
		t.Fatalf("stored 99 read as %d, want %d", got, MaxAuxiliaryConcurrency)
	}

	writeTestConfig(t, legacy, `{"model":{}}`)
	s, err = LoadAt(legacy, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.RuntimeSettings().AuxiliaryConcurrency; got != 1 {
		t.Fatalf("absent runtime read as %d, want 1", got)
	}
}

// A console model save passes no Runtime: Save must preserve the stored knob
// instead of wiping it. An explicit Runtime (the runtime-settings path) wins.
func TestSavePreservesRuntime(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "settings.json")
	s, err := LoadAt(legacy, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetRuntime(Runtime{AuxiliaryConcurrency: 5}); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(File{Model: Model{BaseURL: "http://h:8080", APIKey: "k", Model: "m"}}); err != nil {
		t.Fatal(err)
	}
	if got := s.RuntimeSettings().AuxiliaryConcurrency; got != 5 {
		t.Fatalf("model save wiped the runtime setting: %d", got)
	}
	reloaded, err := LoadAt(legacy, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.RuntimeSettings().AuxiliaryConcurrency; got != 5 {
		t.Fatalf("runtime after reload = %d, want 5", got)
	}
	if m := reloaded.Get().Model; m.BaseURL != "http://h:8080" || m.APIKey != "k" || m.Model != "m" {
		t.Fatalf("model save lost the model: %+v", m)
	}
}

func TestHarnessConfigPath(t *testing.T) {
	t.Setenv("TW_HARNESS_CONFIG", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory in this environment")
	}
	if got, want := HarnessConfigPath(), filepath.Join(home, ".cyber", "cyber.yaml"); got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
	t.Setenv("TW_HARNESS_CONFIG", filepath.Join(t.TempDir(), "custom.yaml"))
	if got := HarnessConfigPath(); got != os.Getenv("TW_HARNESS_CONFIG") {
		t.Fatalf("override = %q", got)
	}
}

// A deployment without a harness file (or a home directory) keeps using the
// pre-harness settings.json.
func TestNoHarnessFileUsesLegacySettings(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "settings.json")
	writeTestConfig(t, legacy, `{"model":{"base_url":"http://legacy:8080","api_key":"k","model":"legacy"}}`)
	s, err := LoadAt(legacy, "")
	if err != nil {
		t.Fatal(err)
	}
	if m := s.Effective(); m.Model != "legacy" || m.BaseURL != "http://legacy:8080" {
		t.Fatalf("effective = %+v", m)
	}
	if s.Path() != legacy {
		t.Fatalf("path = %q", s.Path())
	}
}

// Saving from the console must not wipe a key the UI never saw.
func TestApplyViewKeepsStoredKey(t *testing.T) {
	out := ApplyView(Model{APIKey: "old"}, View{BaseURL: "http://x:8080", Model: "m"})
	if out.APIKey != "old" || out.BaseURL != "http://x:8080" || out.Model != "m" {
		t.Fatalf("applied = %+v", out)
	}
}

func TestEnvAndArgsAreTheHarnessContract(t *testing.T) {
	m := Model{
		BaseURL:       "http://h:8080",
		APIKey:        "sk",
		Model:         "qwen",
		Provider:      "openai",
		ContextWindow: 131072,
		MaxTokens:     4096,
	}
	if got := strings.Join(m.Env(), " "); !strings.Contains(got, "CYBER_BASE_URL=http://h:8080") ||
		!strings.Contains(got, "CYBER_MODEL=qwen") || !strings.Contains(got, "CYBER_PROVIDER=openai") {
		t.Fatalf("env = %v", m.Env())
	}
	if got := strings.Join(m.Args(), " "); got != "--context-window 131072 --max-tokens 4096" {
		t.Fatalf("args = %q", got)
	}
}

func TestViewOfHidesTheKey(t *testing.T) {
	v := ViewOf(Model{BaseURL: "http://h", APIKey: "secret", Model: "m"}, "p")
	if v.APIKey != "" || !v.APIKeySet || !v.Configured {
		t.Fatalf("view = %+v", v)
	}
}
