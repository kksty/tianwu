package settings

// Named model profiles are the harness's own llm.providers entries. The
// console reads them here and, for the profiles a project may run on, hands
// each local host a *single-profile* copy of the file. That copy is what makes
// concurrent hosts independent: the harness treats every other entry in
// llm.providers as a fallback (pkg/config/provider_runtime.go, FallbackProviderConfigs),
// so a host started on the shared file could silently answer with another
// profile's endpoint, key and model after a quota or network failure.
//
// This file is also the only place that edits that list. It edits the YAML
// node tree in place so comments, key order and sections the console does not
// own survive a save.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/kksty/tianwu/internal/modelclient"
)

var ErrProfileNotFound = errors.New("模型配置不存在")

// Profile is one saved harness provider entry, including its credential. It is
// only used inside the control plane (host launch, probe); responses carry
// ProfileView instead.
type Profile struct {
	ID            string `json:"id"`
	Paused        bool   `json:"paused,omitempty"`
	Name          string `json:"name,omitempty"`
	Provider      string `json:"provider,omitempty"`
	BaseURL       string `json:"base_url,omitempty"`
	APIKey        string `json:"api_key,omitempty"`
	Model         string `json:"model,omitempty"`
	Proxy         string `json:"proxy,omitempty"`
	ContextWindow int    `json:"context_window,omitempty"`
	MaxTokens     int    `json:"max_tokens,omitempty"`
	Timeout       int    `json:"timeout,omitempty"`
	Images        *bool  `json:"images,omitempty"`
	// Tier is Tianwu's routing metadata, not a harness provider field: "strong"
	// (commander / hard directions), "worker" (cheap default) or "" (unset).
	// WriteProfileConfig strips it from the per-host copy the harness parses.
	Tier string `json:"tier,omitempty"`
	// APIKeys is the optional rotation pool for this profile (watchdog key
	// rotation). The single api_key is the operator's primary credential; the
	// pool holds the fallbacks. The harness reads exactly one api_key, so the
	// materialised host config carries the entry the rotation cursor selects;
	// the list itself stays in the operator's file and never enters a view or
	// a log.
	APIKeys []string `json:"api_keys,omitempty"`
	// MaxConcurrency caps how many new intent runs may be live on this profile
	// at once (0 = unlimited). It is Tianwu's capacity metadata, not a harness
	// provider field: WriteProfileConfig strips it and Revision ignores it, so
	// changing the cap does not restart a host or invalidate a frozen binding.
	MaxConcurrency int `json:"max_concurrency,omitempty"`
	// Fallback is Tianwu routing metadata, not a harness provider field: the id
	// of the profile an api-layer run may switch to when this profile's
	// credentials are globally unusable (every key rejected for auth/quota).
	// Empty means "never switch"; only one hop is allowed (the target's own
	// fallback is not followed). WriteProfileConfig strips it from the per-host
	// copy the harness parses, and Revision ignores it.
	Fallback string `json:"fallback,omitempty"`
}

// Runnable reports whether a host can actually start on this entry. A profile
// missing any of the three is still listed (the operator is probably halfway
// through editing it) but cannot be selected for an intent.
func (p Profile) Runnable() bool {
	return !p.Paused && strings.TrimSpace(p.BaseURL) != "" &&
		strings.TrimSpace(p.Model) != "" && p.hasKey()
}

// LocalEndpoint labels loopback, private, link-local and mDNS endpoints.
func (p Profile) LocalEndpoint() bool { return isLocalEndpoint(p.BaseURL) }

func isLocalEndpoint(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	host := strings.ToLower(strings.Trim(u.Hostname(), "[]"))
	if host == "" {
		return false
	}
	if host == "localhost" || strings.HasSuffix(host, ".local") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()
	}
	return false
}

// hasKey reports whether any credential is configured, single or pooled.
func (p Profile) hasKey() bool {
	return len(keyPool(p)) > 0
}

// keyPool is the ordered list of credentials this profile may present: the
// single api_key first (the operator's primary), then the api_keys pool as
// rotation fallbacks. Entries are trimmed and de-duplicated so a half-edited
// list does not select an empty credential or spend a rotation on a duplicate.
func keyPool(p Profile) []string {
	out := make([]string, 0, len(p.APIKeys)+1)
	seen := make(map[string]bool, len(p.APIKeys)+1)
	add := func(k string) {
		if k = strings.TrimSpace(k); k != "" && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	add(p.APIKey)
	for _, k := range p.APIKeys {
		add(k)
	}
	return out
}

// normalizeTier validates the routing tier. Unknown values are an error rather
// than a silent downgrade: a typo would otherwise let the commander quietly run
// on the wrong tier.
func normalizeTier(tier string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "":
		return "", nil
	case "strong":
		return "strong", nil
	case "worker":
		return "worker", nil
	default:
		return "", fmt.Errorf("tier 只能是 strong / worker（收到 %q）", tier)
	}
}

// Revision fingerprints the binding-relevant fields. It deliberately leaves the
// API key out: a key rotation changes the credential, not which provider/model
// the run used, and a digest of the key would put credential-derived material
// into the ledger. Key presence is part of the digest so adding a key to an
// incomplete profile still changes the revision.
func (p Profile) Revision() string {
	images := ""
	if p.Images != nil {
		images = fmt.Sprintf("%t", *p.Images)
	}
	// Routing, credentials and capacity do not change a session binding.
	raw := strings.Join([]string{
		p.ID, p.Provider, p.BaseURL, p.Model, p.Proxy,
		fmt.Sprint(p.ContextWindow), fmt.Sprint(p.MaxTokens), fmt.Sprint(p.Timeout),
		images, fmt.Sprintf("%t", p.hasKey()),
	}, "\x00")
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])[:12]
}

// ProfileView is the console-facing profile: no credential, only whether one is
// set.
type ProfileView struct {
	ID            string `json:"id"`
	Paused        bool   `json:"paused,omitempty"`
	Name          string `json:"name,omitempty"`
	Provider      string `json:"provider,omitempty"`
	BaseURL       string `json:"base_url,omitempty"`
	Model         string `json:"model,omitempty"`
	Proxy         string `json:"proxy,omitempty"`
	ContextWindow int    `json:"context_window,omitempty"`
	MaxTokens     int    `json:"max_tokens,omitempty"`
	Timeout       int    `json:"timeout,omitempty"`
	APIKeySet     bool   `json:"api_key_set"`
	// APIKeyCount is how many credentials this profile can present (pool size,
	// or 1 for a single key). It is a count, never the keys themselves.
	APIKeyCount int    `json:"api_key_count,omitempty"`
	Runnable    bool   `json:"runnable"`
	Active      bool   `json:"active"`
	Revision    string `json:"revision"`
	// Tier is the routing metadata ("strong" / "worker"), empty when unset.
	Tier string `json:"tier,omitempty"`
	// Fallback is the one-hop profile to switch to when this profile's
	// credentials are globally unusable, empty when none is configured.
	Fallback string `json:"fallback,omitempty"`
	// MaxConcurrency is the profile-level capacity cap (0 = unlimited). The
	// dispatch gate uses it; the console shows it next to the profile.
	MaxConcurrency int `json:"max_concurrency,omitempty"`
	// Probed/Available/ProbeError are the endpoint model-catalogue verdict
	// (task-13). Runnable only means "every binding field is filled in"; a
	// profile whose gateway does not serve its model is Runnable but not
	// Available, and dispatch refuses it before a run is created. Probed=false
	// means "not checked yet": the zero ProfileView (Profiles()) has no verdict,
	// and only ProfilesProbed/ProbeProfile fill these in.
	Probed        bool   `json:"probed"`
	Available     bool   `json:"available"`
	ProbeError    string `json:"probe_error,omitempty"`
	ProbeKind     string `json:"probe_kind,omitempty"`
	ReturnedModel string `json:"returned_model,omitempty"`
	// Local labels loopback/private-network endpoints for the console.
	Local bool `json:"local,omitempty"`
}

// ProfileCatalog is the instance's saved profiles plus the active one.
type ProfileCatalog struct {
	Profiles      []ProfileView `json:"profiles"`
	ActiveProfile string        `json:"active_profile,omitempty"`
	// HasOverrides warns that flat llm.* fields shadow every profile in the
	// harness merge, so a per-profile copy must be generated instead of edited.
	HasOverrides bool   `json:"has_overrides"`
	Path         string `json:"path,omitempty"`
}

func (s *Store) Profiles() (ProfileCatalog, error) {
	entries, active, overrides, err := s.readProfiles()
	if err != nil {
		return ProfileCatalog{}, err
	}
	out := ProfileCatalog{
		Profiles: make([]ProfileView, 0, len(entries)), ActiveProfile: active,
		HasOverrides: overrides, Path: s.harness,
	}
	for _, p := range entries {
		out.Profiles = append(out.Profiles, viewOfProfile(p, active))
	}
	return out, nil
}

// ---- endpoint model probe (task-13) ----
//
// Runnable answers "are the binding fields filled in?". It does not answer the
// question dispatch actually cares about: does this gateway serve this model?
// A typo in model, a deleted model or a wrong base_url used to show
// runnable=true and fail only after a host started and a run was created
// (docs/17 §1.4.3). The probe asks the endpoint's catalogue and caches the
// verdict, so profile choice can refuse a model the gateway does not serve.

const (
	// probeTTL is how long a catalogue verdict is trusted. Model catalogues
	// change rarely; five minutes keeps a fixed typo visible quickly while
	// keeping the console list cheap.
	probeTTL = 5 * time.Minute
	// probeTimeout bounds one catalogue request. It is short on purpose: this
	// runs while resolving a profile for dispatch, not in the background.
	probeTimeout = 3 * time.Second
)

// probeClient is a seam for tests (httptest servers with an unreachable or slow
// endpoint need a shorter timeout than the production one).
var probeClient = &http.Client{Timeout: probeTimeout}

// probeEntry is one cached catalogue verdict.
type probeEntry struct {
	probe   ModelProbe
	expires time.Time
}

// ModelProbe is the endpoint catalogue verdict for one saved profile.
//
//	Probed=true, Available=true   the gateway answered and lists the model
//	Probed=true, Available=false  definitive no: HTTP error, bad catalogue, or
//	                              the model is absent — dispatch must refuse
//	Probed=false                  not evaluated (fields incomplete)
type ModelProbe struct {
	Kind          string
	ReturnedModel string
	MS            int64
	Probed        bool
	Available     bool
	Error         string
	At            time.Time
}

// ModelCatalogue is an endpoint's model list read for a possibly-unsaved
// binding. It feeds the console's "检测选取模型" action: the operator sees what
// the gateway actually serves and picks an exact model id before saving,
// instead of typing one and finding out only when dispatch refuses it.
type ModelCatalogue struct {
	Endpoint string   `json:"endpoint,omitempty"`
	Models   []string `json:"models"`
	Local    bool     `json:"local"`
	Probed   bool     `json:"probed"`
	Error    string   `json:"error,omitempty"`
}

// Catalogue reads an endpoint's /models list for a draft binding. The key is
// transient: it is used for one request and never returned. When key is empty
// and id names a saved profile, that profile's current key (rotation slot) is
// reused, so editing an existing profile does not require retyping a secret.
func (s *Store) Catalogue(baseURL, id, key string) ModelCatalogue {
	baseURL = strings.TrimSpace(baseURL)
	key = strings.TrimSpace(key)
	if id = strings.TrimSpace(id); id != "" {
		if p, err := s.SavedProfile(id); err == nil {
			if baseURL == "" {
				baseURL = strings.TrimSpace(p.BaseURL)
			}
			if key == "" {
				key = s.currentKey(p)
			}
		}
	}
	cat := fetchCatalogue(baseURL, key)
	out := ModelCatalogue{
		Endpoint: cat.endpoint,
		Models:   cat.models,
		Local:    isLocalEndpoint(baseURL),
		Probed:   cat.probed,
		Error:    cat.err,
	}
	// The wire shape promises an array even when nothing was read.
	if out.Models == nil {
		out.Models = []string{}
	}
	return out
}

// ProbeProfile checks one saved profile's model catalogue, using the cache when
// the revision and base URL are unchanged for probeTTL. The error field never
// contains a credential: only the endpoint, status and model name.
func (s *Store) ProbeProfile(id string) ModelProbe {
	p, err := s.SavedProfile(id)
	if err != nil {
		return ModelProbe{Error: err.Error()}
	}
	return s.probeProfile(p)
}

// ProbeProfileFresh is the operator's "监测 API" action: it drops the cached
// verdict for this revision/base URL and probes now, so fixing a typo or adding
// a key shows the new result immediately instead of after the 5-minute TTL.
func (s *Store) ProbeProfileFresh(id string) ModelProbe {
	p, err := s.SavedProfile(id)
	if err != nil {
		return ModelProbe{Error: err.Error()}
	}
	cacheKey := profileProbeKey(p, s.currentKey(p))
	s.probeMu.Lock()
	delete(s.probeCache, cacheKey)
	s.probeMu.Unlock()
	return s.probeProfile(p)
}

// probeProfile is ProbeProfile for an already-loaded entry.
func (s *Store) probeProfile(p Profile) ModelProbe {
	if p.Paused {
		return ModelProbe{Error: "模型配置已暂停"}
	}
	if !p.Runnable() {
		return ModelProbe{Error: "base_url、model、api_key 必须齐全"}
	}
	cacheKey := profileProbeKey(p, s.currentKey(p))

	s.probeMu.Lock()
	if entry, ok := s.probeCache[cacheKey]; ok && time.Now().Before(entry.expires) {
		s.probeMu.Unlock()
		return entry.probe
	}
	s.probeMu.Unlock()

	probe := s.probeEndpoint(p)
	probe.Kind = "catalogue"
	probe.At = time.Now()

	s.probeMu.Lock()
	if s.probeCache == nil {
		s.probeCache = map[string]probeEntry{}
	}
	now := time.Now()
	for k, entry := range s.probeCache {
		if now.After(entry.expires) {
			delete(s.probeCache, k)
		}
	}
	s.probeCache[cacheKey] = probeEntry{probe: probe, expires: probe.At.Add(probeTTL)}
	s.probeMu.Unlock()
	return probe
}

// catalogueResult is one endpoint's model list, or why the list is unknown.
// models != nil means the catalogue was read; probed=false with an error means
// the endpoint has no catalogue at all (404/405), so a verdict is impossible.
type catalogueResult struct {
	endpoint string
	models   []string
	probed   bool
	err      string
}

// fetchCatalogue turns a base URL plus a transient credential into model ids.
// It is the single place that reads /models, so the dispatch gate
// (probeEndpoint) and the console's "检测选取模型" action cannot disagree about
// what an endpoint serves. The key may be empty: self-hosted gateways often
// need none. No error message ever contains the key.
func fetchCatalogue(baseURL, key string) catalogueResult {
	endpoint := modelListURL(baseURL)
	if endpoint == "" {
		return catalogueResult{err: "base_url 为空"}
	}
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return catalogueResult{endpoint: endpoint, err: "端点点位无法构造请求：" + err.Error()}
	}
	modelclient.ApplyEndpointHeaders(req, "tw-catalogue-"+rand.Text())
	// The call may carry a credential: use the current rotation slot and keep it
	// out of every error message.
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := probeClient.Do(req)
	if err != nil {
		return catalogueResult{endpoint: endpoint, probed: true, err: fmt.Sprintf("探测 %s 失败：%v", endpoint, err)}
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	// A gateway that has no catalogue endpoint at all is not proof that the
	// model is missing: several OpenAI-compatible proxies implement only
	// chat/completions. That verdict stays "unknown" (Probed=false) so dispatch
	// is not blocked forever by a provider that simply cannot be preflighted.
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return catalogueResult{endpoint: endpoint, err: fmt.Sprintf(
			"端点 %s 不提供模型清单（HTTP %d），无法预检模型是否存在", endpoint, resp.StatusCode)}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// Every other status is the endpoint answering: a 401/403 means the key
		// is rejected (definitive broken credential), 5xx means the gateway is
		// up but failing. Both are definitive "do not dispatch now" verdicts;
		// the 5-minute cache bounds how long a recovered gateway is blocked.
		return catalogueResult{endpoint: endpoint, probed: true, err: fmt.Sprintf("端点 %s 返回 HTTP %d", endpoint, resp.StatusCode)}
	}
	if readErr != nil {
		return catalogueResult{endpoint: endpoint, probed: true, err: fmt.Sprintf("读 %s 的模型清单失败：%v", endpoint, readErr)}
	}
	ids, err := parseModelIDs(body)
	if err != nil {
		return catalogueResult{endpoint: endpoint, probed: true, err: fmt.Sprintf("端点 %s 的模型清单不是预期 JSON：%v", endpoint, err)}
	}
	return catalogueResult{endpoint: endpoint, models: ids, probed: true}
}

// probeEndpoint applies the dispatch gate's rule to a saved profile.
func (s *Store) probeEndpoint(p Profile) ModelProbe {
	cat := fetchCatalogue(p.BaseURL, s.currentKey(p))
	if cat.err != "" {
		return ModelProbe{Probed: cat.probed, Error: cat.err}
	}
	for _, id := range cat.models {
		if id == p.Model {
			return ModelProbe{Probed: true, Available: true}
		}
	}
	return ModelProbe{Probed: true, Error: fmt.Sprintf(
		"端点 %s 的模型清单里没有 %q（清单共 %d 个模型）", cat.endpoint, p.Model, len(cat.models))}
}

// modelListURL builds the catalogue URL from a base URL that may or may not
// already include the OpenAI version segment.
func modelListURL(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	base = strings.TrimSuffix(base, "/chat/completions")
	if base == "" {
		return ""
	}
	lower := strings.ToLower(base)
	switch {
	case strings.HasSuffix(lower, "/models"):
		return base
	case strings.HasSuffix(lower, "/v1"):
		return base + "/models"
	default:
		return base + "/v1/models"
	}
}

// parseModelIDs accepts both catalogue shapes seen in the wild: OpenAI's
// {"data":[{"id":"…"}]} and proxies that answer {"models":["…"]} (or
// {"models":[{"id":…}]}).
func parseModelIDs(body []byte) ([]string, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return nil, err
	}
	if raw, ok := top["data"]; ok {
		if ids, ok := decodeModelArray(raw); ok {
			return ids, nil
		}
	}
	if raw, ok := top["models"]; ok {
		if ids, ok := decodeModelArray(raw); ok {
			return ids, nil
		}
	}
	return nil, errors.New(`没有 data/models 数组`)
}

func decodeModelArray(raw json.RawMessage) ([]string, bool) {
	var objects []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &objects); err == nil {
		ids := make([]string, 0, len(objects))
		for _, o := range objects {
			if o.ID != "" {
				ids = append(ids, o.ID)
			}
		}
		return ids, true
	}
	var flat []string
	if err := json.Unmarshal(raw, &flat); err == nil {
		return flat, true
	}
	return nil, false
}

// ProfilesProbed is Profiles plus each runnable profile's cached endpoint
// verdict. Probes run concurrently (bounded) so a list request costs one short
// timeout at most, not one per profile.
func (s *Store) ProfilesProbed() (ProfileCatalog, error) {
	cat, err := s.Profiles()
	if err != nil {
		return cat, err
	}
	entries, _, _, err := s.readProfiles()
	if err != nil {
		// The catalogue itself was readable a moment ago; a probe must not turn
		// the profile list into an error.
		return cat, nil
	}
	byID := make(map[string]Profile, len(entries))
	for _, p := range entries {
		byID[p.ID] = p
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i := range cat.Profiles {
		view := &cat.Profiles[i]
		profile, ok := byID[view.ID]
		if !ok || !profile.Runnable() {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(view *ProfileView, profile Profile) {
			defer wg.Done()
			defer func() { <-sem }()
			probe := s.probeProfile(profile)
			view.Probed, view.Available, view.ProbeError = probe.Probed, probe.Available, probe.Error
			view.ProbeKind, view.ReturnedModel = probe.Kind, probe.ReturnedModel
		}(view, profile)
	}
	wg.Wait()
	return cat, nil
}

// SecretValues returns every credential value the console configured: each
// profile's effective key and its whole api_keys pool, plus the effective flat
// key (which may come from CYBER_API_KEY). It exists only for run-journal and
// ledger redaction — never put it in a view, a log line or an error.
func (s *Store) SecretValues() []string {
	entries, _, _, err := s.readProfiles()
	if err != nil {
		entries = nil
	}
	var out []string
	seen := map[string]bool{}
	add := func(key string) {
		key = strings.TrimSpace(key)
		// One- or two-character values would shred every log line; real
		// credentials are long. The console token is 48 bytes, keys are longer.
		if len(key) < 8 || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, key)
	}
	for _, p := range entries {
		add(p.APIKey)
		for _, key := range p.APIKeys {
			add(key)
		}
	}
	add(s.Effective().APIKey)
	return out
}

// Profile returns one saved entry including its credential. Callers must not
// put it in a response or a log.
func (s *Store) SavedProfile(id string) (Profile, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Profile{}, fmt.Errorf("%w：id 为空", ErrProfileNotFound)
	}
	entries, _, _, err := s.readProfiles()
	if err != nil {
		return Profile{}, err
	}
	for _, p := range entries {
		if p.ID == id {
			return p, nil
		}
	}
	return Profile{}, fmt.Errorf("%w：%s", ErrProfileNotFound, id)
}

// FallbackProfile returns the one-hop credential fallback of a saved profile:
// the profile id the api layer may switch a new run to when every credential of
// source has been rejected (auth/quota). An empty result with a nil error means
// "no fallback configured", which the caller must treat as "do not switch".
// Anything else is a hard error — a missing target, a target that cannot start,
// a different routing tier, or a self-reference — because silently hopping onto
// an incomplete or differently-tuned profile would turn a provider outage into
// a wrong-model run. Only the first hop is validated: the target's own fallback
// is never followed.
func (s *Store) FallbackProfile(source string) (string, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return "", fmt.Errorf("%w：id 为空", ErrProfileNotFound)
	}
	entries, _, _, err := s.readProfiles()
	if err != nil {
		return "", err
	}
	byID := make(map[string]*Profile, len(entries))
	for i := range entries {
		byID[entries[i].ID] = &entries[i]
	}
	from := byID[source]
	if from == nil {
		return "", fmt.Errorf("%w：%s", ErrProfileNotFound, source)
	}
	if from.Paused {
		return "", fmt.Errorf("模型配置 %s 已暂停", source)
	}
	fallback := strings.TrimSpace(from.Fallback)
	if fallback == "" {
		return "", nil
	}
	if fallback == from.ID {
		return "", fmt.Errorf("模型配置 %s 的 fallback 不能指向自己", from.ID)
	}
	next := byID[fallback]
	if next == nil {
		return "", fmt.Errorf("模型配置 %s 的 fallback 指向不存在的配置 %s", from.ID, fallback)
	}
	if next.Paused {
		return "", fmt.Errorf("fallback 模型配置 %s 已暂停", fallback)
	}
	if !next.Runnable() {
		return "", fmt.Errorf("模型配置 %s 的 fallback 指向 %s，但该配置还不能执行：base_url、model、api_key 必须齐全", from.ID, fallback)
	}
	fromTier, err := normalizeTier(from.Tier)
	if err != nil {
		return "", err
	}
	nextTier, err := normalizeTier(next.Tier)
	if err != nil {
		return "", err
	}
	if fromTier != nextTier {
		label := func(tier string) string {
			if tier == "" {
				return "未设置"
			}
			return tier
		}
		return "", fmt.Errorf("模型配置 %s 的 fallback 指向 %s，但档位不同（%s → %s）：只能切换到同档的配置",
			from.ID, fallback, label(fromTier), label(nextTier))
	}
	return fallback, nil
}

// ProfileModel is the old probe entry point: the model catalogue is fetched
// with the selected profile's endpoint and key, never with the default one.
func (s *Store) ProfileModel(id string) (Model, error) {
	p, err := s.SavedProfile(id)
	if err != nil {
		return Model{}, err
	}
	return p.model(), nil
}

func (p Profile) model() Model {
	return Model{
		BaseURL: p.BaseURL, APIKey: p.APIKey, Model: p.Model, Provider: p.Provider,
		ContextWindow: p.ContextWindow, MaxTokens: p.MaxTokens,
	}
}

// Env is the harness's environment contract for this profile. A profile host
// also reads a single-profile file; the environment makes the values explicit
// and prevents an inherited CYBER_* from a different deployment from winning.
func (p Profile) Env() []string { return p.model().Env() }

func viewOfProfile(p Profile, active string) ProfileView {
	return ProfileView{
		ID: p.ID, Paused: p.Paused, Name: p.Name, Provider: p.Provider, BaseURL: p.BaseURL,
		Model: p.Model, Proxy: p.Proxy, ContextWindow: p.ContextWindow,
		MaxTokens: p.MaxTokens, Timeout: p.Timeout,
		APIKeySet: p.hasKey(), APIKeyCount: len(keyPool(p)), Runnable: p.Runnable(),
		Active: p.ID == active, Revision: p.Revision(),
		Tier: p.Tier, Fallback: p.Fallback, MaxConcurrency: p.MaxConcurrency,
		Local: p.LocalEndpoint(),
	}
}

// SaveProfile creates or updates one entry. An empty APIKey on an existing
// entry keeps the stored key (the console never echoes secrets), which mirrors
// the instance settings form. Everything the console does not own in that entry
// (images, unknown keys) is preserved.
func (s *Store) SaveProfile(p Profile) (Profile, error) {
	p.BaseURL = modelclient.NormalizeOpenCodeBase(p.BaseURL)
	p.ID = strings.TrimSpace(p.ID)
	if err := validProfileID(p.ID); err != nil {
		return Profile{}, err
	}
	tier, err := normalizeTier(p.Tier)
	if err != nil {
		return Profile{}, err
	}
	p.Tier = tier
	p.Fallback = strings.TrimSpace(p.Fallback)
	if p.Fallback == p.ID {
		return Profile{}, fmt.Errorf("模型配置 %s 的 fallback 不能指向自己：凭据失效时只能切换到别的配置", p.ID)
	}
	if err := p.model().Validate(); err != nil {
		return Profile{}, err
	}
	if p.ContextWindow < 0 || p.MaxTokens < 0 || p.Timeout < 0 {
		return Profile{}, errors.New("context_window / max_tokens / timeout 不能为负")
	}
	if p.MaxConcurrency < 0 {
		return Profile{}, errors.New("max_concurrency 不能为负（0 = 不限）")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	root, doc, err := s.readDocument()
	if err != nil {
		return Profile{}, err
	}
	llm := ensureMapping(doc, "llm")
	providers := ensureSequence(llm, "providers")
	entry := findEntry(providers, p.ID)
	if entry == nil {
		entry = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		providers.Content = append(providers.Content, entry)
		setScalar(entry, "id", p.ID)
	}
	if strings.TrimSpace(p.APIKey) == "" {
		existing, err := profileString(entry, "api_key")
		if err != nil {
			return Profile{}, err
		}
		existingKeys, err := profileStringList(entry, "api_keys")
		if err != nil {
			return Profile{}, err
		}
		// A nil APIKeys means "keep whatever pool is stored"; a non-nil one is
		// the new list, so an empty non-nil slice is a deliberate clear.
		pool := existingKeys
		if p.APIKeys != nil {
			pool = p.APIKeys
		}
		if existing == "" && len(keyPool(Profile{APIKeys: pool})) == 0 {
			return Profile{}, errors.New("api_key 不能为空：这个配置还不能启动宿主")
		}
	}
	setScalar(entry, "name", p.Name)
	setScalar(entry, "provider", p.Provider)
	setScalar(entry, "base_url", p.BaseURL)
	setScalar(entry, "model", p.Model)
	setScalar(entry, "proxy", p.Proxy)
	setScalar(entry, "tier", p.Tier)
	// fallback is the one-hop credential failover target. An empty value keeps
	// whatever the operator's file already has (the console does not author the
	// hop yet, so a save must not silently clear an operator-written failover;
	// clearing it is a hand edit of that file). A non-empty value is written as
	// given. The target is deliberately not required to exist here: writing A→B
	// before B is created is allowed, and FallbackProfile validates the hop at
	// switch time.
	if p.Fallback != "" {
		setScalar(entry, "fallback", p.Fallback)
	}
	if strings.TrimSpace(p.APIKey) != "" {
		setScalar(entry, "api_key", p.APIKey)
	}
	if p.APIKeys != nil {
		setStringList(entry, "api_keys", p.APIKeys)
	}
	setIntOrDelete(entry, "context_window", p.ContextWindow)
	setIntOrDelete(entry, "max_tokens", p.MaxTokens)
	setIntOrDelete(entry, "max_concurrency", p.MaxConcurrency)
	// timeout and images belong to the harness's own settings page: the
	// console shows them (so a second gateway's limits are not a mystery) but
	// an edit here must not wipe values it never offered for editing.
	if err := s.writeDocument(root); err != nil {
		return Profile{}, err
	}
	return s.SavedProfile(p.ID)
}

// ActivateProfile makes a saved entry the instance default (llm.active_profile).
// It is an explicit operator action, not a runtime model switch: the caller
// restarts the default host so the new profile is read once at startup.
//
// A flat llm.* override shadows every profile in the harness merge, so
// activation is refused while one exists; otherwise the console would claim a
// profile is active while the harness keeps using the flat value.
func (s *Store) ActivateProfile(id string) (Profile, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Profile{}, errors.New("id 不能为空")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	entries, _, overrides, err := s.readProfiles()
	if err != nil {
		return Profile{}, err
	}
	if overrides {
		return Profile{}, errors.New("harness 配置里有 flat llm.* 覆盖，会压过 active_profile；先清掉这些字段")
	}
	var chosen *Profile
	for i := range entries {
		if entries[i].ID == id {
			chosen = &entries[i]
			break
		}
	}
	if chosen == nil {
		return Profile{}, fmt.Errorf("%w：%s", ErrProfileNotFound, id)
	}
	if chosen.Paused {
		return Profile{}, fmt.Errorf("模型配置 %s 已暂停：先恢复使用再设为默认", id)
	}
	if !chosen.Runnable() {
		return Profile{}, fmt.Errorf("模型配置 %s 还不能执行：base_url、model、api_key 必须齐全", id)
	}
	root, doc, err := s.readDocument()
	if err != nil {
		return Profile{}, err
	}
	setScalar(ensureMapping(doc, "llm"), "active_profile", id)
	if err := s.writeDocument(root); err != nil {
		return Profile{}, err
	}
	return *chosen, nil
}

// DeleteProfile removes a saved entry. The active profile is refused: the
// decider and every legacy (profile-less) run read it, so deleting it would
// leave the instance pointing at a profile that no longer exists.
func (s *Store) DeleteProfile(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("%w：id 为空", ErrProfileNotFound)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	root, doc, err := s.readDocument()
	if err != nil {
		return err
	}
	llm := mapGet(doc, "llm")
	if llm == nil {
		return fmt.Errorf("%w：%s", ErrProfileNotFound, id)
	}
	providers := mapGet(llm, "providers")
	active := strings.TrimSpace(scalar(mapGet(llm, "active_profile")))
	if active == "" && providers != nil && len(providers.Content) > 0 {
		// The harness treats the first entry as active when the selector is
		// absent; deleting it would silently change the instance default.
		active = strings.TrimSpace(scalar(mapGet(providers.Content[0], "id")))
	}
	if active == id {
		return errors.New("不能删除当前激活的模型配置：先在设置里切换到别的配置")
	}
	entry := findEntry(providers, id)
	if entry == nil {
		return fmt.Errorf("%w：%s", ErrProfileNotFound, id)
	}
	for i, node := range providers.Content {
		if node == entry {
			providers.Content = append(providers.Content[:i], providers.Content[i+1:]...)
			break
		}
	}
	return s.writeDocument(root)
}

// WriteProfileConfig writes a harness configuration containing exactly one
// provider: the selected profile. It is what a profile host is started with.
//
// The whole document is copied, so agent/traffic/extensions (and any section
// this control plane does not know) stay in force, but flat llm.* overrides are
// removed: they would shadow the chosen profile in the harness merge. The file
// lands with 0600 because it carries the API key.
func (s *Store) WriteProfileConfig(id, dest string) error {
	// The entry and the document must come from the same read: a concurrent
	// save between them would make the host run a newer configuration under the
	// older revision the pool recorded.
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	p, err := s.SavedProfile(id)
	if err != nil {
		return err
	}
	if p.Paused {
		return fmt.Errorf("模型配置 %s 已暂停", id)
	}
	root, doc, err := s.readDocument()
	if err != nil {
		return err
	}
	llm := mapGet(doc, "llm")
	if llm == nil || llm.Kind != yaml.MappingNode {
		return fmt.Errorf("harness 配置里没有 llm 段：%s", s.harness)
	}
	source := findEntry(mapGet(llm, "providers"), p.ID)
	if source == nil {
		return fmt.Errorf("%w：%s", ErrProfileNotFound, p.ID)
	}
	// Drop the flat overrides before selecting the profile.
	for _, key := range []string{"provider", "base_url", "api_key", "model", "proxy", "timeout", "images", "context_window", "max_tokens"} {
		mapDelete(llm, key)
	}
	entry := cloneNode(source)
	// pricing / tier / api_keys / max_concurrency / fallback / paused are Tianwu
	// metadata, not harness provider fields: the harness parses this file
	// strictly and rejects unknown keys, so the host configuration must carry
	// only what the harness understands. The entry in the operator's file keeps
	// them; this materialised copy is for the host.
	stripProfileMetadata(entry)
	// The harness reads exactly one api_key. When the profile has a key pool,
	// the materialised config carries the entry the rotation cursor selects,
	// so a rotation is "write this file again + restart the host", not a
	// rewrite of the operator's configuration.
	if key := s.currentKey(p); key != "" {
		setScalar(entry, "api_key", key)
	}
	providers := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{entry}}
	mapSet(llm, "providers", providers)
	setScalar(llm, "active_profile", p.ID)
	return writeYAML(dest, root)
}

// RotateProfileKey advances the in-memory key-selection cursor of one profile
// and returns its view. rotated is false when the profile has at most one
// credential: there is nothing else to try, and the caller (the watchdog) must
// not restart a host for a no-op. The cursor is runtime state, not
// configuration: it is not persisted and not part of Revision, so a rotation
// never invalidates a frozen run binding.
func (s *Store) RotateProfileKey(id string) (ProfileView, bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return ProfileView{}, false, fmt.Errorf("%w：id 为空", ErrProfileNotFound)
	}
	entries, active, _, err := s.readProfiles()
	if err != nil {
		return ProfileView{}, false, err
	}
	var profile Profile
	found := false
	for _, p := range entries {
		if p.ID == id {
			profile, found = p, true
			break
		}
	}
	if !found {
		return ProfileView{}, false, fmt.Errorf("%w：%s", ErrProfileNotFound, id)
	}
	keys := keyPool(profile)
	view := viewOfProfile(profile, active)
	if len(keys) < 2 {
		return view, false, nil
	}
	s.keyMu.Lock()
	if s.keyCursors == nil {
		s.keyCursors = map[string]int{}
	}
	s.keyCursors[id] = (s.keyCursors[id] + 1) % len(keys)
	s.keyMu.Unlock()
	return view, true, nil
}

// currentKey is the credential a materialised host configuration carries: the
// entry the rotation cursor selects from the pool, or the single stored key.
// Callers must already hold writeMu (WriteProfileConfig does).
func (s *Store) currentKey(p Profile) string {
	keys := keyPool(p)
	if len(keys) == 0 {
		return ""
	}
	s.keyMu.Lock()
	cursor := s.keyCursors[p.ID]
	s.keyMu.Unlock()
	if cursor < 0 || cursor >= len(keys) {
		cursor = 0
	}
	return keys[cursor]
}

// ProfileEnv is the harness environment contract for the profile's *current*
// credential (the one the rotation cursor selects), not the primary api_key.
// A profile host is launched with this: the environment wins over the config
// file, so launching with the primary key would silently undo a rotation.
func (s *Store) ProfileEnv(id string) ([]string, error) {
	p, err := s.SavedProfile(id)
	if err != nil {
		return nil, err
	}
	m := p.model()
	if key := s.currentKey(p); key != "" {
		m.APIKey = key
	}
	return m.Env(), nil
}

// CurrentProfileKey reports which credential the next host configuration for
// this profile would carry, and how many are available. It is used by tests and
// by the watchdog to log a rotation without exposing the key itself.
func (s *Store) CurrentProfileKey(id string) (count, index int) {
	p, err := s.SavedProfile(id)
	if err != nil {
		return 0, 0
	}
	keys := keyPool(p)
	if len(keys) == 0 {
		return 0, 0
	}
	s.keyMu.Lock()
	cursor := s.keyCursors[p.ID]
	s.keyMu.Unlock()
	if cursor < 0 || cursor >= len(keys) {
		cursor = 0
	}
	return len(keys), cursor
}

// readProfiles parses the provider list. A missing file is an empty catalogue;
// a malformed one is an error (the caller shows it instead of pretending).
func (s *Store) readProfiles() ([]Profile, string, bool, error) {
	_, doc, err := s.readDocument()
	if err != nil {
		return nil, "", false, err
	}
	if doc == nil {
		return nil, "", false, nil
	}
	llm := mapGet(doc, "llm")
	if llm == nil {
		return nil, "", false, nil
	}
	if llm.Kind != yaml.MappingNode {
		return nil, "", false, errors.New("harness 的 llm 必须是映射")
	}
	active, err := profileString(llm, "active_profile")
	if err != nil {
		return nil, "", false, err
	}
	providers := mapGet(llm, "providers")
	if providers != nil && providers.Kind != yaml.SequenceNode {
		return nil, "", false, errors.New("harness 的 llm.providers 必须是列表")
	}
	var entries []Profile
	seen := map[string]bool{}
	if providers != nil {
		for _, node := range providers.Content {
			p, err := decodeProfile(node)
			if err != nil {
				return nil, "", false, err
			}
			if seen[p.ID] {
				return nil, "", false, fmt.Errorf("harness 模型配置 id 重复：%s", p.ID)
			}
			seen[p.ID] = true
			entries = append(entries, p)
		}
	}
	if active != "" && !seen[active] {
		return nil, "", false, fmt.Errorf("harness 的 active_profile 指向不存在的配置：%s", active)
	}
	if active == "" && len(entries) > 0 {
		active = entries[0].ID
	}
	overrides := false
	for _, key := range []string{"provider", "base_url", "api_key", "model", "proxy", "context_window", "max_tokens", "timeout", "images"} {
		if node := mapGet(llm, key); node != nil && strings.TrimSpace(scalar(node)) != "" {
			overrides = true
		}
	}
	return entries, active, overrides, nil
}

// readDocument returns the parsed YAML root and its mapping document. A missing
// file yields an empty mapping, so SaveProfile can create one.
func (s *Store) readDocument() (*yaml.Node, *yaml.Node, error) {
	if s.harness == "" {
		return nil, nil, errors.New("没有 harness 配置路径：设置 TW_HARNESS_CONFIG")
	}
	root := &yaml.Node{Kind: yaml.DocumentNode}
	data, err := os.ReadFile(s.harness)
	switch {
	case err == nil:
		if err := yaml.Unmarshal(data, root); err != nil {
			// YAML errors may quote a line containing credentials.
			return nil, nil, fmt.Errorf("%s 不是有效 YAML", s.harness)
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		return nil, nil, err
	}
	doc := documentRoot(root)
	if doc == nil {
		doc = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		root.Content = []*yaml.Node{doc}
	}
	if doc.Kind != yaml.MappingNode {
		return nil, nil, errors.New("harness 配置根节点必须是映射")
	}
	// Node decoding alone accepts duplicate mapping keys; reject ambiguous
	// documents before mapGet can silently pick a different value than harness.
	var checked map[string]any
	if err := doc.Decode(&checked); err != nil {
		return nil, nil, errors.New("harness 配置有重复字段或无效结构")
	}
	return root, doc, nil
}

func (s *Store) writeDocument(root *yaml.Node) error {
	return writeYAML(s.harness, root)
}

func writeYAML(path string, root *yaml.Node) error {
	var buf strings.Builder
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(4)
	if err := enc.Encode(root); err != nil {
		return fmt.Errorf("编码 %s：%w", path, err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("编码 %s：%w", path, err)
	}
	if err := writeFileAtomic(path, []byte(buf.String())); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	return nil
}

func decodeProfile(node *yaml.Node) (Profile, error) {
	if node.Kind != yaml.MappingNode {
		return Profile{}, errors.New("harness 的模型配置条目必须是映射")
	}
	var p Profile
	if paused := mapGet(node, "paused"); paused != nil {
		if paused.Kind != yaml.ScalarNode || paused.Tag != "!!bool" {
			return Profile{}, errors.New("模型配置的 paused 必须是布尔值")
		}
		if err := paused.Decode(&p.Paused); err != nil {
			return Profile{}, err
		}
	}
	for _, field := range []struct {
		key string
		dst *string
	}{
		{"id", &p.ID}, {"name", &p.Name}, {"provider", &p.Provider},
		{"base_url", &p.BaseURL}, {"api_key", &p.APIKey},
		{"model", &p.Model}, {"proxy", &p.Proxy},
	} {
		value, err := profileString(node, field.key)
		if err != nil {
			return Profile{}, err
		}
		*field.dst = value
	}
	p.BaseURL = modelclient.NormalizeOpenCodeBase(p.BaseURL)
	for _, field := range []struct {
		key string
		dst *int
	}{
		{"context_window", &p.ContextWindow},
		{"max_tokens", &p.MaxTokens},
		{"timeout", &p.Timeout},
		{"max_concurrency", &p.MaxConcurrency},
	} {
		value := mapGet(node, field.key)
		if value != nil && (value.Kind != yaml.ScalarNode || value.Tag != "!!int") {
			return Profile{}, fmt.Errorf("harness 模型配置的 %s 必须是非负整数", field.key)
		}
		n, err := intField(node, field.key)
		if err != nil || n < 0 {
			return Profile{}, fmt.Errorf("harness 模型配置的 %s 必须是非负整数", field.key)
		}
		*field.dst = n
	}
	if images := mapGet(node, "images"); images != nil && images.Tag != "!!null" {
		if images.Kind != yaml.ScalarNode || images.Tag != "!!bool" {
			return Profile{}, errors.New("harness 模型配置的 images 必须是布尔值")
		}
		value := images.Value == "true"
		p.Images = &value
	}
	if keys, err := profileStringList(node, "api_keys"); err != nil {
		return Profile{}, err
	} else {
		p.APIKeys = keys
	}
	tier, err := profileString(node, "tier")
	if err != nil {
		return Profile{}, err
	}
	if p.Tier, err = normalizeTier(tier); err != nil {
		// A saved file with an unknown tier is a configuration error, not
		// something to silently read as "unset": the operator wrote it.
		return Profile{}, err
	}
	// Fallback is a plain id reference, not validated here: a dangling target
	// stays readable (the operator may be creating B after writing A→B) and is
	// rejected by FallbackProfile right before a switch.
	fallback, err := profileString(node, "fallback")
	if err != nil {
		return Profile{}, err
	}
	p.Fallback = fallback
	if strings.TrimSpace(p.ID) == "" {
		return Profile{}, errors.New("harness 模型配置 id 为空")
	}
	return p, nil
}

func validProfileID(id string) error {
	if id == "" {
		return errors.New("id 不能为空")
	}
	if len(id) > 64 {
		return errors.New("id 太长（最多 64 字节）")
	}
	for _, r := range id {
		if r < 0x20 || r == '/' || r == '\\' || r == ':' {
			return fmt.Errorf("id 不能包含控制字符、路径分隔符或冒号：%q", id)
		}
	}
	if strings.ContainsAny(id, " \t\r\n") {
		return fmt.Errorf("id 不能包含空白：%q", id)
	}
	return nil
}

func profileString(node *yaml.Node, key string) (string, error) {
	value := mapGet(node, key)
	if value == nil || value.Tag == "!!null" {
		return "", nil
	}
	if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
		return "", fmt.Errorf("harness 模型配置的 %s 必须是字符串", key)
	}
	return strings.TrimSpace(value.Value), nil
}

// profileStringList reads an optional list-of-strings field. A missing or null
// node is nil; anything else must be a sequence of strings.
func profileStringList(node *yaml.Node, key string) ([]string, error) {
	value := mapGet(node, key)
	if value == nil || value.Tag == "!!null" {
		return nil, nil
	}
	if value.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("harness 模型配置的 %s 必须是字符串列表", key)
	}
	out := make([]string, 0, len(value.Content))
	for _, item := range value.Content {
		if item.Kind != yaml.ScalarNode || item.Tag != "!!str" {
			return nil, fmt.Errorf("harness 模型配置的 %s 必须是字符串列表", key)
		}
		if s := strings.TrimSpace(item.Value); s != "" {
			out = append(out, s)
		}
	}
	return out, nil
}

// setStringList replaces a list field; an empty list deletes the key rather
// than leaving an empty sequence.
func setStringList(node *yaml.Node, key string, values []string) {
	clean := make([]string, 0, len(values))
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			clean = append(clean, v)
		}
	}
	if len(clean) == 0 {
		mapDelete(node, key)
		return
	}
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, v := range clean {
		seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v})
	}
	mapSet(node, key, seq)
}

func findEntry(providers *yaml.Node, id string) *yaml.Node {
	if providers == nil || providers.Kind != yaml.SequenceNode {
		return nil
	}
	for _, node := range providers.Content {
		if node.Kind == yaml.MappingNode && strings.TrimSpace(scalar(mapGet(node, "id"))) == id {
			return node
		}
	}
	return nil
}

func cloneNode(node *yaml.Node) *yaml.Node {
	if node == nil {
		return nil
	}
	out := *node
	out.Content = make([]*yaml.Node, len(node.Content))
	for i, child := range node.Content {
		out.Content[i] = cloneNode(child)
	}
	return &out
}
