package settings

// Fallback is Tianwu's one-hop credential failover metadata stored next to the
// harness profile: it round-trips through the operator's file, never reaches
// the per-host configuration the harness parses, and never moves the binding
// revision. FallbackProfile is the only validation point before the api layer
// switches a run, and it refuses a target that is missing, incomplete or on a
// different routing tier rather than silently running on the wrong model.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fallbackProfiles = `llm:
    active_profile: fast
    providers:
        - id: fast
          provider: openai
          base_url: http://fast:8080
          api_key: fast-secret
          model: fast-model
          tier: worker
          fallback: backup
        - id: backup
          provider: openai
          base_url: http://backup:8080
          api_key: backup-secret
          model: backup-model
          tier: worker
        - id: strong
          provider: openai
          base_url: http://strong:8080
          api_key: strong-secret
          model: strong-model
          tier: strong
        - id: broken
          provider: openai
          base_url: http://broken:8080
          model: broken-model
`

func TestProfileFallbackRoundTripsThroughYAML(t *testing.T) {
	s, _ := loadProfiles(t, fallbackProfiles)
	cat, err := s.Profiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Profiles) != 4 || cat.Profiles[0].Fallback != "backup" || cat.Profiles[1].Fallback != "" {
		t.Fatalf("fallback views = %+v", cat.Profiles)
	}
	if !strings.Contains(string(mustJSON(t, cat)), `"fallback":"backup"`) {
		t.Fatal("catalog json does not expose fallback")
	}
	saved, err := s.SavedProfile("fast")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Fallback != "backup" {
		t.Fatalf("saved fallback = %q", saved.Fallback)
	}
}

// The console does not author fallback yet: a save must keep the operator's
// target instead of clearing it, while a programmatic non-empty value is
// written as given (even if the target does not exist yet).
func TestSaveProfileFallbackKeepsStoredTarget(t *testing.T) {
	s, harness := loadProfiles(t, fallbackProfiles)
	if _, err := s.SaveProfile(Profile{
		ID: "fast", Provider: "openai", BaseURL: "http://fast:8080",
		Model: "fast-model", Tier: "worker",
	}); err != nil {
		t.Fatal(err)
	}
	saved, err := s.SavedProfile("fast")
	if err != nil || saved.Fallback != "backup" {
		t.Fatalf("empty fallback cleared the stored target: %+v err=%v", saved, err)
	}
	if !strings.Contains(readFile(t, harness), "fallback: backup") {
		t.Fatal("operator file lost the fallback target")
	}

	if _, err := s.SaveProfile(Profile{
		ID: "backup", Provider: "openai", BaseURL: "http://backup:8080",
		Model: "backup-model", Tier: "worker", Fallback: "fast",
	}); err != nil {
		t.Fatal(err)
	}
	if saved, err := s.SavedProfile("backup"); err != nil || saved.Fallback != "fast" {
		t.Fatalf("fallback write = %+v err=%v", saved, err)
	}
	// A forward reference is allowed: the operator may write A→B before B is
	// created. FallbackProfile is what refuses the dangling hop at switch time.
	if _, err := s.SaveProfile(Profile{
		ID: "backup", Provider: "openai", BaseURL: "http://backup:8080",
		Model: "backup-model", Tier: "worker", Fallback: "not-created-yet",
	}); err != nil {
		t.Fatalf("saving a forward-reference fallback must be allowed: %v", err)
	}
	if !strings.Contains(readFile(t, harness), "fallback: not-created-yet") {
		t.Fatal("operator file lost the forward-reference fallback")
	}
}

func TestSaveProfileRefusesSelfFallback(t *testing.T) {
	s, _ := loadProfiles(t, fallbackProfiles)
	if _, err := s.SaveProfile(Profile{
		ID: "backup", Provider: "openai", BaseURL: "http://backup:8080",
		Model: "backup-model", Tier: "worker", Fallback: "backup",
	}); err == nil || !strings.Contains(err.Error(), "自己") {
		t.Fatalf("self fallback = %v, want a validation error", err)
	}
}

// FallbackProfile is the api layer's only validation point: it returns the
// target when the hop is safe and reports every unsafe case explicitly. An
// empty target with a nil error is the "no failover configured" signal.
func TestFallbackProfile(t *testing.T) {
	cases := []struct {
		name    string
		source  string
		body    string
		want    string
		wantErr string
	}{
		{
			name: "configured",
			body: `llm:
    providers:
        - {id: fast, base_url: http://fast:1, model: m, api_key: k, tier: worker, fallback: backup}
        - {id: backup, base_url: http://backup:1, model: m, api_key: k, tier: worker}
`,
			want: "backup",
		},
		{
			name: "not configured",
			body: `llm:
    providers:
        - {id: fast, base_url: http://fast:1, model: m, api_key: k, tier: worker}
        - {id: backup, base_url: http://backup:1, model: m, api_key: k, tier: worker}
`,
			want: "",
		},
		{
			name: "missing target",
			body: `llm:
    providers:
        - {id: fast, base_url: http://fast:1, model: m, api_key: k, tier: worker, fallback: ghost}
`,
			wantErr: "不存在",
		},
		{
			name: "target not runnable",
			body: `llm:
    providers:
        - {id: fast, base_url: http://fast:1, model: m, api_key: k, tier: worker, fallback: broken}
        - {id: broken, base_url: http://broken:1, model: m, tier: worker}
`,
			wantErr: "不能执行",
		},
		{
			name: "tier mismatch",
			body: `llm:
    providers:
        - {id: fast, base_url: http://fast:1, model: m, api_key: k, tier: worker, fallback: strong}
        - {id: strong, base_url: http://strong:1, model: m, api_key: k, tier: strong}
`,
			wantErr: "档位不同",
		},
		{
			name: "self",
			body: `llm:
    providers:
        - {id: fast, base_url: http://fast:1, model: m, api_key: k, tier: worker, fallback: fast}
`,
			wantErr: "自己",
		},
		{
			name:   "source missing",
			source: "ghost",
			body: `llm:
    providers:
        - {id: fast, base_url: http://fast:1, model: m, api_key: k, tier: worker}
`,
			wantErr: "不存在",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := loadProfiles(t, tc.body)
			source := tc.source
			if source == "" {
				source = "fast"
			}
			got, err := s.FallbackProfile(source)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("FallbackProfile(%s) = %q, %v; want error containing %q", source, got, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("FallbackProfile(%s) = %v", source, err)
			}
			if got != tc.want {
				t.Fatalf("FallbackProfile(%s) = %q, want %q", source, got, tc.want)
			}
		})
	}
}

// The host configuration is parsed by the harness strictly, so Tianwu-only
// fallback metadata must stay in the operator's file.
func TestFallbackIsStrippedFromHostConfig(t *testing.T) {
	s, harness := loadProfiles(t, fallbackProfiles)
	dest := filepath.Join(t.TempDir(), "host.yaml")
	if err := s.WriteProfileConfig("fast", dest); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "fallback") {
		t.Fatalf("host config carries Tianwu-only fallback metadata:\n%s", data)
	}
	if !strings.Contains(string(data), "model: fast-model") {
		t.Fatalf("host config lost harness fields:\n%s", data)
	}
	if !strings.Contains(readFile(t, harness), "fallback: backup") {
		t.Fatal("the operator's file must keep its fallback metadata")
	}
}

// A fallback edit is routing metadata, not binding identity: it must not
// restart a host or invalidate a frozen run binding.
func TestFallbackChangeDoesNotChangeRevision(t *testing.T) {
	s, _ := loadProfiles(t, fallbackProfiles)
	before, err := s.SavedProfile("backup")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveProfile(Profile{
		ID: "backup", Provider: "openai", BaseURL: "http://backup:8080",
		Model: "backup-model", Tier: "worker", Fallback: "fast",
	}); err != nil {
		t.Fatal(err)
	}
	after, err := s.SavedProfile("backup")
	if err != nil {
		t.Fatal(err)
	}
	if after.Fallback != "fast" {
		t.Fatalf("saved fallback = %q", after.Fallback)
	}
	if before.Revision() != after.Revision() {
		t.Fatalf("revision moved on a fallback edit: %s -> %s", before.Revision(), after.Revision())
	}
	base := Profile{ID: "a", BaseURL: "http://x:1", Model: "m", APIKey: "k"}
	other := base
	other.Fallback = "b"
	if base.Revision() != other.Revision() {
		t.Fatal("fallback target entered the binding revision")
	}
}
