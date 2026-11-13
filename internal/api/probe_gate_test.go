package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/model"
)

// The endpoint probe is what turns "the profile has all its fields" into "the
// gateway serves this model" (task-13). These tests pin the dispatch-side half:
// a definitive unavailable verdict rejects before any run row exists, while an
// unknown verdict (Probed=false) keeps the old behaviour.

const probeReason = `端点 http://gw.example/v1/models 的模型清单里没有 "ghost-model"（清单共 2 个模型）`

func ghostProfile() ProfileInfo {
	return ProfileInfo{
		ID: "fast", Provider: "openai", Model: "ghost-model", Revision: "rev-fast",
		Runnable: true, Active: true,
		Probed: true, Available: false, ProbeError: probeReason,
	}
}

func probeGateServer(t *testing.T, active string, profiles ...ProfileInfo) *Server {
	t.Helper()
	s, p := newBindingServer(t)
	s.LocalNode = "local"
	if err := p.SetNodeBinding([]string{"local"}, "local"); err != nil {
		t.Fatal(err)
	}
	s.Profiles = func() ([]ProfileInfo, string, error) {
		return append([]ProfileInfo(nil), profiles...), active, nil
	}
	return s
}

func TestCreateIntentRejectsUnavailableModel(t *testing.T) {
	s := probeGateServer(t, "fast", ghostProfile())
	w := call(t, s, http.MethodPost, "/api/intents", map[string]string{"title": "走不存在的模型", "profile": "fast"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("create = %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"不可用", "fast", "ghost-model", "模型清单"} {
		if !strings.Contains(body, want) {
			t.Fatalf("error %q does not mention %q", body, want)
		}
	}
	if intents := s.Store.ListIntents(); len(intents) != 0 {
		t.Fatalf("a rejected profile still created an intent: %+v", intents)
	}
}

// A default profile (instance active / project worker) is a binding too: if it
// is definitively unavailable the dispatch fails visibly instead of silently
// running the flat/env model.
func TestDefaultProfileUnavailableBlocksDispatch(t *testing.T) {
	s := probeGateServer(t, "fast", ghostProfile())
	w := call(t, s, http.MethodPost, "/api/intents", map[string]string{"title": "落到默认档位"})
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", w.Code, w.Body.String())
	}
	var in model.Intent
	if err := json.Unmarshal(w.Body.Bytes(), &in); err != nil {
		t.Fatal(err)
	}
	w = call(t, s, http.MethodPost, "/api/intents/"+in.ID+"/run", nil)
	if w.Code < 400 {
		t.Fatalf("run started on an unavailable default profile: %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"默认模型配置不可用", "ghost-model"} {
		if !strings.Contains(body, want) {
			t.Fatalf("error %q does not mention %q", body, want)
		}
	}
	if runs := s.Store.Runs(); len(runs) != 0 {
		t.Fatalf("a run was created for an unavailable default profile: %+v", runs)
	}
}

// No verdict yet (no settings store, older config, probe not run) must not
// block: unknown is not broken.
func TestUnknownProbeVerdictDoesNotBlock(t *testing.T) {
	unknown := ProfileInfo{
		ID: "fast", Provider: "openai", Model: "fast-model", Revision: "rev-fast",
		Runnable: true, Active: true,
	}
	s := probeGateServer(t, "fast", unknown)
	if _, err := s.profileBinding("fast"); err != nil {
		t.Fatalf("unprobed profile was refused: %v", err)
	}
	w := call(t, s, http.MethodPost, "/api/intents", map[string]string{"title": "未知探测", "profile": "fast"})
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", w.Code, w.Body.String())
	}
}

// runnable=false still has its own error and is not mislabelled as a probe
// failure.
func TestNotRunnableProfileKeepsFieldError(t *testing.T) {
	incomplete := ProfileInfo{ID: "half", Provider: "openai", Model: "m", Revision: "rev-half"}
	s := probeGateServer(t, "", incomplete)
	w := call(t, s, http.MethodPost, "/api/intents", map[string]string{"title": "x", "profile": "half"})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "还不能执行") {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
}

// The console/agent catalogue carries the verdict so a caller can avoid a
// profile before trying to run it.
func TestListModelProfilesExposesProbe(t *testing.T) {
	s := probeGateServer(t, "fast", ghostProfile())
	w := call(t, s, http.MethodGet, "/api/model-profiles", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list = %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Profiles []ProfileInfo `json:"profiles"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Profiles) != 1 {
		t.Fatalf("profiles = %+v", body.Profiles)
	}
	p := body.Profiles[0]
	if !p.Probed || p.Available || !strings.Contains(p.ProbeError, "ghost-model") {
		t.Fatalf("probe fields = %+v", p)
	}
	if !strings.Contains(w.Body.String(), `"probed":true`) || !strings.Contains(w.Body.String(), `"available":false`) {
		t.Fatalf("JSON shape = %s", w.Body.String())
	}
}
