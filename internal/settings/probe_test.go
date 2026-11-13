package settings

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The endpoint probe is the difference between "the fields are filled in" and
// "this gateway serves this model" (task-13). These tests use httptest servers;
// no real credential or network is involved.

func probeConfig(baseURL, model, key string) string {
	return fmt.Sprintf(`llm:
    active_profile: fast
    providers:
        - id: fast
          provider: openai
          base_url: %s
          api_key: %s
          model: %s
`, baseURL, key, model)
}

// withProbeClient swaps the probe client (and its short timeout) for one test.
func withProbeClient(t *testing.T, timeout time.Duration) {
	t.Helper()
	old := probeClient
	probeClient = &http.Client{Timeout: timeout}
	t.Cleanup(func() { probeClient = old })
}

func modelCatalogue(ids ...string) string {
	items := make([]string, 0, len(ids))
	for _, id := range ids {
		items = append(items, fmt.Sprintf(`{"id":%q,"object":"model"}`, id))
	}
	return `{"object":"list","data":[` + strings.Join(items, ",") + `]}`
}

const probeKey = "sk-probe-0123456789abcdef" // gitleaks:allow -- fake key used only by local httptest servers

func TestProbeProfileFindsModel(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if got := r.Header.Get("Authorization"); got != "Bearer "+probeKey {
			t.Errorf("probe did not use the profile key: %q", got)
		}
		if r.URL.Path != "/v1/models" {
			t.Errorf("probe path = %q, want /v1/models", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(modelCatalogue("fast-model", "other-model")))
	}))
	defer srv.Close()

	s, _ := loadProfiles(t, probeConfig(srv.URL, "fast-model", probeKey))
	probe := s.ProbeProfile("fast")
	if !probe.Probed || !probe.Available || probe.Error != "" {
		t.Fatalf("probe = %+v", probe)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("hits = %d", hits)
	}
	// Second lookup is served from the 5-minute cache.
	if again := s.ProbeProfile("fast"); !again.Available || !again.Probed {
		t.Fatalf("cached probe = %+v", again)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("probe was repeated despite the cache: hits=%d", hits)
	}
	// The catalogue view carries the verdict without any credential.
	cat, err := s.ProfilesProbed()
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Profiles) != 1 || !cat.Profiles[0].Probed || !cat.Profiles[0].Available {
		t.Fatalf("catalog = %+v", cat.Profiles)
	}
	if strings.Contains(fmt.Sprint(cat), probeKey) {
		t.Fatal("catalogue leaked the API key")
	}
}

// A base_url that already carries /v1 must not become /v1/v1/models.
func TestProbeHandlesVersionedBaseURL(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(modelCatalogue("fast-model")))
	}))
	defer srv.Close()

	s, _ := loadProfiles(t, probeConfig(srv.URL+"/v1", "fast-model", probeKey))
	if probe := s.ProbeProfile("fast"); !probe.Available {
		t.Fatalf("probe = %+v", probe)
	}
	if path != "/v1/models" {
		t.Fatalf("path = %q, want /v1/models", path)
	}
}

func TestProbeProfileModelMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(modelCatalogue("some-other-model")))
	}))
	defer srv.Close()

	s, _ := loadProfiles(t, probeConfig(srv.URL, "fast-model", probeKey))
	probe := s.ProbeProfile("fast")
	if !probe.Probed || probe.Available {
		t.Fatalf("probe = %+v, want probed and unavailable", probe)
	}
	if !strings.Contains(probe.Error, "fast-model") || !strings.Contains(probe.Error, "/v1/models") {
		t.Fatalf("error does not name the model and endpoint: %q", probe.Error)
	}
	if strings.Contains(probe.Error, probeKey) {
		t.Fatalf("probe error leaked the key: %q", probe.Error)
	}
}

// A gateway without a catalogue endpoint (404/405) cannot be preflighted: that
// is unknown, not broken, so it must not block dispatch forever.
func TestProbeProfileNoCatalogueEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	s, _ := loadProfiles(t, probeConfig(srv.URL, "fast-model", probeKey))
	probe := s.ProbeProfile("fast")
	if probe.Probed || probe.Available {
		t.Fatalf("probe = %+v, want unknown verdict", probe)
	}
	if !strings.Contains(probe.Error, "HTTP 404") || !strings.Contains(probe.Error, "无法预检") {
		t.Fatalf("error = %q", probe.Error)
	}
	if strings.Contains(probe.Error, probeKey) {
		t.Fatalf("probe error leaked the key: %q", probe.Error)
	}
}

// A rejected key or a failing gateway is a definitive "do not dispatch now".
func TestProbeProfileServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad gateway", http.StatusBadGateway)
	}))
	defer srv.Close()

	s, _ := loadProfiles(t, probeConfig(srv.URL, "fast-model", probeKey))
	probe := s.ProbeProfile("fast")
	if !probe.Probed || probe.Available {
		t.Fatalf("probe = %+v", probe)
	}
	if !strings.Contains(probe.Error, "HTTP 502") {
		t.Fatalf("error = %q", probe.Error)
	}
	if strings.Contains(probe.Error, probeKey) {
		t.Fatalf("probe error leaked the key: %q", probe.Error)
	}
}

func TestProbeProfileTimeout(t *testing.T) {
	withProbeClient(t, 40*time.Millisecond)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte(modelCatalogue("fast-model")))
	}))
	defer srv.Close()

	s, _ := loadProfiles(t, probeConfig(srv.URL, "fast-model", probeKey))
	start := time.Now()
	probe := s.ProbeProfile("fast")
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("timeout probe took %s, want a short bound", elapsed)
	}
	if !probe.Probed || probe.Available {
		t.Fatalf("probe = %+v", probe)
	}
	if !strings.Contains(probe.Error, "探测") {
		t.Fatalf("error = %q", probe.Error)
	}
	if strings.Contains(probe.Error, probeKey) {
		t.Fatalf("probe error leaked the key: %q", probe.Error)
	}
}

// The cache key includes the revision: editing the profile re-probes instead of
// serving a verdict for the old model.
func TestProbeCacheInvalidatedByRevision(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(modelCatalogue("fast-model")))
	}))
	defer srv.Close()

	s, _ := loadProfiles(t, probeConfig(srv.URL, "fast-model", probeKey))
	if probe := s.ProbeProfile("fast"); !probe.Available {
		t.Fatalf("probe = %+v", probe)
	}
	if _, err := s.SaveProfile(Profile{
		ID: "fast", Provider: "openai", BaseURL: srv.URL, Model: "fast-model-v2",
	}); err != nil {
		t.Fatal(err)
	}
	probe := s.ProbeProfile("fast")
	if probe.Available {
		t.Fatalf("stale verdict survived a revision change: %+v", probe)
	}
	if atomic.LoadInt32(&hits) != 2 {
		t.Fatalf("hits = %d, want a second probe after the edit", hits)
	}
}

func TestProbeIncompleteProfileIsNotProbed(t *testing.T) {
	body := `llm:
    providers:
        - id: half
          provider: openai
          base_url: http://127.0.0.1:1
          model: m
`
	s, _ := loadProfiles(t, body)
	probe := s.ProbeProfile("half")
	if probe.Probed || probe.Available {
		t.Fatalf("incomplete profile was probed: %+v", probe)
	}
	cat, err := s.ProfilesProbed()
	if err != nil {
		t.Fatal(err)
	}
	if cat.Profiles[0].Probed {
		t.Fatalf("incomplete view has a verdict: %+v", cat.Profiles[0])
	}
}

func TestProbeUnknownProfile(t *testing.T) {
	s, _ := loadProfiles(t, twoProfiles)
	if probe := s.ProbeProfile("nope"); probe.Probed || probe.Available || probe.Error == "" {
		t.Fatalf("probe = %+v", probe)
	}
}

func TestModelListURL(t *testing.T) {
	cases := []struct{ base, want string }{
		{"http://gw:8080", "http://gw:8080/v1/models"},
		{"http://gw:8080/", "http://gw:8080/v1/models"},
		{"http://gw:8080/v1", "http://gw:8080/v1/models"},
		{"http://gw:8080/v1/", "http://gw:8080/v1/models"},
		{"http://gw:8080/v1/models", "http://gw:8080/v1/models"},
		{"http://gw/openai/v1", "http://gw/openai/v1/models"},
		{"", ""},
	}
	for _, c := range cases {
		if got := modelListURL(c.base); got != c.want {
			t.Fatalf("modelListURL(%q) = %q, want %q", c.base, got, c.want)
		}
	}
}

func TestParseModelIDsShapes(t *testing.T) {
	openai, err := parseModelIDs([]byte(`{"data":[{"id":"a"},{"id":"b"}]}`))
	if err != nil || len(openai) != 2 || openai[1] != "b" {
		t.Fatalf("openai shape = %v err=%v", openai, err)
	}
	flat, err := parseModelIDs([]byte(`{"models":["x","y"]}`))
	if err != nil || len(flat) != 2 || flat[0] != "x" {
		t.Fatalf("flat shape = %v err=%v", flat, err)
	}
	objs, err := parseModelIDs([]byte(`{"models":[{"id":"m1"}]}`))
	if err != nil || len(objs) != 1 || objs[0] != "m1" {
		t.Fatalf("object models shape = %v err=%v", objs, err)
	}
	if _, err := parseModelIDs([]byte(`not json`)); err == nil {
		t.Fatal("invalid JSON accepted")
	}
	if _, err := parseModelIDs([]byte(`{"unexpected":true}`)); err == nil {
		t.Fatal("catalogue without data/models accepted")
	}
}

// SecretValues feeds journal redaction: every profile key and pool spare, no
// short values, no duplicates.
func TestSecretValues(t *testing.T) {
	body := `llm:
    active_profile: fast
    providers:
        - id: fast
          provider: openai
          base_url: http://gw:8080
          api_key: primary-key-0123456789
          api_keys:
              - primary-key-0123456789
              - spare-key-0123456789
          model: fast-model
        - id: slow
          provider: anthropic
          base_url: http://gw2:8080
          api_key: short
          model: slow-model
`
	s, _ := loadProfiles(t, body)
	values := s.SecretValues()
	if len(values) != 2 {
		t.Fatalf("values = %v", values)
	}
	joined := strings.Join(values, "|")
	for _, want := range []string{"primary-key-0123456789", "spare-key-0123456789"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %v", want, values)
		}
	}
	if strings.Contains(joined, "short") {
		t.Fatalf("a short non-credential value was included: %v", values)
	}
}

// The console's "检测选取模型" action reads the draft endpoint's catalogue
// before the profile is saved. It must list what the gateway serves, reuse the
// saved credential when the editor leaves the key blank, and never leak the key.
func TestCatalogueForDraftBinding(t *testing.T) {
	var gotKey, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey, gotPath = r.Header.Get("Authorization"), r.URL.Path
		_, _ = w.Write([]byte(modelCatalogue("fast-model", "slow-model")))
	}))
	defer srv.Close()

	s, _ := loadProfiles(t, probeConfig(srv.URL, "fast-model", probeKey))

	// The editor sent only the URL + key it is about to save: an unsaved draft.
	cat := s.Catalogue(srv.URL+"/v1", "", probeKey)
	if !cat.Probed || cat.Error != "" || cat.Endpoint != srv.URL+"/v1/models" {
		t.Fatalf("catalogue = %+v", cat)
	}
	if len(cat.Models) != 2 || cat.Models[0] != "fast-model" || cat.Models[1] != "slow-model" {
		t.Fatalf("models = %v", cat.Models)
	}
	if gotKey != "Bearer "+probeKey || gotPath != "/v1/models" {
		t.Fatalf("request used key=%q path=%q", gotKey, gotPath)
	}

	// Editing an existing profile: the blank key reuses the saved credential,
	// and a blank base_url inherits the saved one.
	cat = s.Catalogue("", "fast", "")
	if len(cat.Models) != 2 || cat.Error != "" {
		t.Fatalf("saved-profile catalogue = %+v", cat)
	}
	if !cat.Local {
		t.Fatalf("loopback endpoint should be classified local: %+v", cat)
	}
	if strings.Contains(fmt.Sprint(cat), probeKey) {
		t.Fatal("catalogue leaked the API key")
	}

	// A gateway without a /models endpoint reports "unknown" plus a reason.
	notFound := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer notFound.Close()
	cat = s.Catalogue(notFound.URL, "", "")
	if cat.Probed || len(cat.Models) != 0 || !strings.Contains(cat.Error, "HTTP 404") {
		t.Fatalf("no-catalogue verdict = %+v", cat)
	}
	if cat.Models == nil {
		t.Fatal("models must serialise as [] not null")
	}
}
