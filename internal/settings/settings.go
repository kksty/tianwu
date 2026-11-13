// Package settings is the instance-level model configuration.
//
// It is a face over the harness's own configuration: the active profile in
// <home>/.cyber/cyber.yaml (llm.providers + llm.active_profile) is what the
// console shows and edits, so the decider, the REPL and every local harness run
// resolve the same model. The knobs are the harness's: CYBER_BASE_URL /
// CYBER_API_KEY / CYBER_MODEL / CYBER_PROVIDER plus context_window /
// max_tokens; Env() and Args() hand them to a local run, and the file is the
// durable copy the harness itself reads.
//
// settings.json is kept as the pre-harness location: it is read when the harness
// file has no profile (an upgrade) and written alongside every save so an older
// binary pointed at the same data directory still starts configured.
package settings

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/kksty/tianwu/internal/modelclient"
)

// MaxAuxiliaryConcurrency is the upper bound of the operator-tunable gate that
// serialises review and report runs (one auxiliary model call at a time by
// default). It is a console setting, not a harness setting.
const MaxAuxiliaryConcurrency = 8

// Model is the shared LLM configuration.
type Model struct {
	BaseURL       string `json:"base_url"`
	APIKey        string `json:"api_key"`
	Model         string `json:"model"`
	Provider      string `json:"provider,omitempty"`
	ContextWindow int    `json:"context_window,omitempty"`
	MaxTokens     int    `json:"max_tokens,omitempty"`
}

// Runtime groups the instance-level knobs that belong to the live console
// rather than to a project or to a harness profile. The review/report gate is
// process-wide and shared by every project, so it is not a project setting.
//
// It persists in the legacy settings.json only: the harness file (cyber.yaml)
// is the harness's own model configuration and must not gain these fields.
type Runtime struct {
	AuxiliaryConcurrency int `json:"auxiliary_concurrency,omitempty"`
}

// Validate rejects values the gate cannot honour.
func (r Runtime) Validate() error {
	if r.AuxiliaryConcurrency < 1 || r.AuxiliaryConcurrency > MaxAuxiliaryConcurrency {
		return fmt.Errorf("auxiliary_concurrency 必须在 1..%d 之间", MaxAuxiliaryConcurrency)
	}
	return nil
}

// File is what lands on disk.
type File struct {
	Model   Model   `json:"model"`
	Runtime Runtime `json:"runtime,omitempty"`
}

// Store is the instance configuration: the harness profile plus the legacy
// settings.json fallback.
type Store struct {
	mu   sync.RWMutex
	path string // legacy settings.json
	// writeMu serialises read-modify-write updates of the harness file. The
	// active-profile form and the profile list edit the same YAML document;
	// without it two saves could each write back the other's stale view.
	writeMu sync.Mutex
	// harness is the file the console actually edits (<home>/.cyber/cyber.yaml).
	// Empty means the account has no home directory; only the legacy file is
	// usable then.
	harness string
	f       File

	profile string
	lastErr error

	// keyMu guards keyCursors, the in-memory rotation cursor for profiles with
	// an api_keys pool (profiles.go). Rotation is runtime state: not persisted,
	// not part of Revision, and a restart starts again from the first key.
	keyMu      sync.Mutex
	keyCursors map[string]int

	// creds is the per-profile credential generation and failure-episode state
	// (credentials.go). Like the cursor it is runtime state: not persisted and
	// not part of Revision. It lives on the shared Store — not on a project's
	// api.Server — because the pool it describes is instance-wide.
	creds credentialRegistry

	// probeMu guards probeCache: the endpoint model-catalogue verdicts used to
	// refuse a profile whose model the gateway does not serve (profiles.go).
	// Network truth is runtime state: nothing is persisted and a restart
	// re-probes.
	probeMu    sync.Mutex
	probeCache map[string]probeEntry
}

// Load reads the harness profile; a missing file is an empty configuration, not
// an error. settings.json is the fallback for a data directory configured before
// the console read the harness file.
func Load(path string) (*Store, error) {
	return LoadAt(path, HarnessConfigPath())
}

// LoadAt is Load with an explicit harness path (tests, deployments where the
// control plane runs as a different account).
func LoadAt(path, harness string) (*Store, error) {
	s := &Store{path: path, harness: harness}
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(b, &s.f); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
	case os.IsNotExist(err):
	default:
		return nil, err
	}
	return s, nil
}

// Path is the file the console edits: the harness profile when there is one.
func (s *Store) Path() string {
	if s.harness != "" {
		return s.harness
	}
	return s.path
}

// LegacyPath is the pre-harness settings.json, kept for the console's migration
// note and for an operator who needs the old file.
func (s *Store) LegacyPath() string { return s.path }

// Get returns a copy of the legacy file.
func (s *Store) Get() File {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.f
}

// RuntimeSettings is the effective instance runtime configuration. An absent or
// non-positive value reads as the default of 1 (one auxiliary model call at a
// time, the historic behaviour), and a value above the maximum is clamped to
// MaxAuxiliaryConcurrency instead of being rejected, so a hand-edited file
// cannot take the console down.
func (s *Store) RuntimeSettings() Runtime {
	s.mu.RLock()
	rt := s.f.Runtime
	s.mu.RUnlock()
	if rt.AuxiliaryConcurrency <= 0 {
		rt.AuxiliaryConcurrency = 1
	}
	if rt.AuxiliaryConcurrency > MaxAuxiliaryConcurrency {
		rt.AuxiliaryConcurrency = MaxAuxiliaryConcurrency
	}
	return rt
}

// SetRuntime validates and persists the instance runtime knobs to the legacy
// settings.json, preserving the model block. The harness file is deliberately
// not touched: it is the model configuration, not the console's runtime state.
// writeMu is shared with Save because both rewrite the same legacy file.
func (s *Store) SetRuntime(rt Runtime) error {
	if err := rt.Validate(); err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.mu.RLock()
	f := s.f
	s.mu.RUnlock()
	f.Runtime = rt
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	s.mu.Lock()
	s.f = f
	s.mu.Unlock()
	return nil
}

// harnessModel reads the active profile, then falls back to the legacy file and
// the process environment. Reading on every call is deliberate: the harness's
// own web UI writes the same file, and the console must show that edit without a
// restart.
func (s *Store) harnessModel() Model {
	if s.harness == "" {
		return s.Get().Model
	}
	m, profile, err := readHarnessModel(s.harness)
	s.mu.Lock()
	s.lastErr = err
	if err == nil {
		s.profile = profile
	}
	legacy := s.f.Model
	s.mu.Unlock()
	if err != nil {
		// A malformed file must not take the console down: the last good value
		// (usually CYBER_* in the environment, or settings.json) keeps working
		// and View() reports the parse error.
		return legacy
	}
	// A profile may carry its credential only as an api_keys pool (the watchdog
	// rotates inside it). The harness file keeps one api_key, so the default
	// host must start with the pool's current key; otherwise "configured" reads
	// false and the host boots without a provider.
	if strings.TrimSpace(m.APIKey) == "" && profile != "" {
		if p, perr := s.SavedProfile(profile); perr == nil {
			if key := s.currentKey(p); key != "" {
				m.APIKey = key
			}
		}
	}
	if !m.any() {
		return legacy
	}
	return m
}

// Effective is the configuration a decider and a local run actually use.
func (s *Store) Effective() Model {
	return withEnv(s.harnessModel())
}

// withEnv fills the blanks from the process environment: a deployment that
// never opened the settings page keeps working, exactly like the harness.
func withEnv(m Model) Model {
	if m.BaseURL == "" {
		m.BaseURL = strings.TrimSpace(os.Getenv("CYBER_BASE_URL"))
	}
	if m.APIKey == "" {
		m.APIKey = strings.TrimSpace(os.Getenv("CYBER_API_KEY"))
	}
	if m.Model == "" {
		m.Model = strings.TrimSpace(os.Getenv("CYBER_MODEL"))
	}
	if m.Provider == "" {
		m.Provider = strings.TrimSpace(os.Getenv("CYBER_PROVIDER"))
	}
	if m.ContextWindow <= 0 {
		if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("CYBER_CONTEXT_WINDOW"))); err == nil {
			m.ContextWindow = n
		}
	}
	if m.MaxTokens <= 0 {
		if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("CYBER_MAX_TOKENS"))); err == nil {
			m.MaxTokens = n
		}
	}
	// No default here on purpose: 0 means "not set", Args() omits --max-tokens
	// and the decider omits max_tokens, so both sides use the harness/provider
	// default. An invented cap is how the two consumers drifted apart.
	m.BaseURL = modelclient.NormalizeOpenCodeBase(m.BaseURL)
	return m
}

// any reports whether the file said anything about the model at all.
func (m Model) any() bool {
	return m.BaseURL != "" || m.APIKey != "" || m.Model != "" || m.Provider != "" ||
		m.ContextWindow > 0 || m.MaxTokens > 0
}

// Save writes the harness profile atomically with owner-only permissions (it
// holds an API key), then mirrors the legacy settings.json so an older binary on
// the same data directory still starts configured.
func (s *Store) Save(f File) error {
	f.Model.BaseURL = modelclient.NormalizeOpenCodeBase(f.Model.BaseURL)
	if err := f.Model.Validate(); err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	// A caller that only edits the model (the usual console save) passes no
	// Runtime: keep the stored one instead of wiping the operator's setting.
	if f.Runtime.AuxiliaryConcurrency == 0 {
		s.mu.RLock()
		f.Runtime = s.f.Runtime
		s.mu.RUnlock()
	}
	if s.harness != "" {
		if err := writeHarnessModel(s.harness, f.Model); err != nil {
			return err
		}
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	s.mu.Lock()
	s.f = f
	s.lastErr = nil
	s.mu.Unlock()
	return nil
}

// Profile is the harness profile id the console edited; empty before the first
// read or when the file has no profile.
func (s *Store) Profile() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.profile
}

// HarnessError is the last read error (a malformed cyber.yaml). It is surfaced
// instead of silently pretending the file was empty.
func (s *Store) HarnessError() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastErr
}

// Configured says whether a decider can be built from this configuration.
func (m Model) Configured() bool {
	return strings.TrimSpace(m.BaseURL) != "" &&
		strings.TrimSpace(m.APIKey) != "" &&
		strings.TrimSpace(m.Model) != ""
}

// Validate rejects shapes the harness would reject anyway, before they are
// written down.
func (m Model) Validate() error {
	if m.BaseURL != "" {
		u, err := url.Parse(m.BaseURL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("base_url 不是合法地址：%q", m.BaseURL)
		}
	}
	if m.ContextWindow < 0 || m.MaxTokens < 0 {
		return fmt.Errorf("context_window / max_tokens 不能为负")
	}
	return nil
}

// Env is the harness's own environment contract (CYBER_*).
func (m Model) Env() []string {
	var out []string
	add := func(k, v string) {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, k+"="+v)
		}
	}
	add("CYBER_BASE_URL", m.BaseURL)
	add("CYBER_API_KEY", m.APIKey)
	add("CYBER_MODEL", m.Model)
	add("CYBER_PROVIDER", m.Provider)
	return out
}

// Args are the harness flags the console exposes. They are appended to every
// local run; an AOP node keeps its host-side configuration (docs/14).
func (m Model) Args() []string {
	var out []string
	if m.ContextWindow > 0 {
		out = append(out, "--context-window", strconv.Itoa(m.ContextWindow))
	}
	if m.MaxTokens > 0 {
		out = append(out, "--max-tokens", strconv.Itoa(m.MaxTokens))
	}
	return out
}

// View is the API shape: the key is never echoed back, only whether one is set.
// A PUT may carry api_key; leaving it empty keeps the stored key.
//
// Path is the harness profile the console edits, Profile its llm.active_profile,
// and HarnessError names a file that exists but cannot be parsed (in which case
// the form still shows the effective fallback values).
type View struct {
	BaseURL       string `json:"base_url"`
	APIKey        string `json:"api_key,omitempty"`
	APIKeySet     bool   `json:"api_key_set"`
	Model         string `json:"model"`
	Provider      string `json:"provider,omitempty"`
	ContextWindow int    `json:"context_window,omitempty"`
	MaxTokens     int    `json:"max_tokens,omitempty"`
	Configured    bool   `json:"configured"`
	Path          string `json:"path"`
	LegacyPath    string `json:"legacy_path,omitempty"`
	Profile       string `json:"active_profile,omitempty"`
	HarnessError  string `json:"harness_error,omitempty"`
}

// ViewOf masks the secret.
func ViewOf(m Model, path string) View {
	return View{
		BaseURL:       m.BaseURL,
		APIKeySet:     strings.TrimSpace(m.APIKey) != "",
		Model:         m.Model,
		Provider:      m.Provider,
		ContextWindow: m.ContextWindow,
		MaxTokens:     m.MaxTokens,
		Configured:    m.Configured(),
		Path:          path,
	}
}

// View is the console's read of the store, including where the values live.
func (s *Store) View() View {
	v := ViewOf(s.Effective(), s.Path())
	v.LegacyPath = s.LegacyPath()
	v.Profile = s.Profile()
	if err := s.HarnessError(); err != nil {
		v.HarnessError = err.Error()
	}
	return v
}

// ApplyView merges a console update into the current model. An empty api_key
// means "keep the one already stored", so the UI never has to echo the secret.
func ApplyView(cur Model, in View) Model {
	out := Model{
		BaseURL:       strings.TrimSpace(in.BaseURL),
		Model:         strings.TrimSpace(in.Model),
		Provider:      strings.TrimSpace(in.Provider),
		ContextWindow: in.ContextWindow,
		MaxTokens:     in.MaxTokens,
	}
	if k := strings.TrimSpace(in.APIKey); k != "" {
		out.APIKey = k
	} else {
		out.APIKey = cur.APIKey
	}
	return out
}
