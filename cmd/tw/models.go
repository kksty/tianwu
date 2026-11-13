package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/kksty/tianwu/internal/modelclient"
	"github.com/kksty/tianwu/internal/settings"
)

// serveModels asks the configured endpoint which models it exposes, so the
// settings page can offer a list instead of a free-text model id. The request is
// made by the controller, not the browser: the API key never leaves this host.
func (h *hub) serveModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "只支持 GET"})
		return
	}
	var m settings.Model
	profile := strings.TrimSpace(r.URL.Query().Get("profile"))
	if r.URL.Query().Has("profile") {
		if profile == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "profile 不能为空"})
			return
		}
		var err error
		m, err = h.settings.ProfileModel(profile)
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, settings.ErrProfileNotFound) {
				status = http.StatusNotFound
			}
			writeJSON(w, status, map[string]any{"error": err.Error()})
			return
		}
	} else {
		m = h.settings.Effective()
	}
	if strings.TrimSpace(m.BaseURL) == "" || strings.TrimSpace(m.APIKey) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "先保存 API 地址与 Key，再检测模型"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	models, limits, err := listModels(ctx, m)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	// Remember the per-model output caps: a later "save settings" clamps
	// max_tokens to them so the value sent to the decider and the local harness
	// can never exceed what the model accepts.
	// The existing output-cap cache belongs to the default endpoint. A second
	// gateway may serve the same model id with a different cap.
	if profile == "" {
		h.rememberModelLimits(limits)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"models":   models,
		"limits":   limits,
		"base_url": m.BaseURL,
		"provider": m.Provider,
		"profile":  profile,
	})
}

// modelLimitDoc is one entry of a provider's /models catalogue. OpenAI-compatible
// gateways publish max_output_tokens; Anthropic's list does not, in which case
// the field is 0 and no clamp is applied.
type modelLimitDoc struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	MaxOutputTokens int    `json:"max_output_tokens"`
}

func listModels(ctx context.Context, m settings.Model) ([]string, map[string]int, error) {
	u, headers := modelsURL(m)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("模型列表请求：%w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept", "application/json")
	res, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("连不上 %s：%w", u, err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, nil, fmt.Errorf("%s 返回 %d：%s", u, res.StatusCode, strings.TrimSpace(string(body)))
	}
	var payload struct {
		Data   []modelLimitDoc `json:"data"`
		Models []modelLimitDoc `json:"models"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, nil, fmt.Errorf("%s 不是模型列表：%w", u, err)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(payload.Data)+len(payload.Models))
	limits := map[string]int{}
	add := func(id string, maxOutput int) {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] || len(out) >= 300 {
			return
		}
		seen[id] = true
		out = append(out, id)
		if maxOutput > 0 {
			limits[id] = maxOutput
		}
	}
	for _, d := range append(payload.Data, payload.Models...) {
		add(d.ID, d.MaxOutputTokens)
		if d.ID == "" {
			add(d.Name, d.MaxOutputTokens)
		}
	}
	sort.Strings(out)
	return out, limits, nil
}

// modelsURL builds the provider-specific endpoint. It reuses the chat client's
// base-URL normalisation (modelclient.Endpoint): the configured base is usually
// "http://host:8080" and chat goes to /v1/chat/completions, so the model list
// must go to /v1/models rather than /models.
func modelsURL(m settings.Model) (string, map[string]string) {
	base, provider := modelclient.Endpoint(m.BaseURL, m.Provider)
	key := strings.TrimSpace(m.APIKey)
	if provider == modelclient.ProviderAnthropic {
		return base + "/models?limit=100", map[string]string{
			"x-api-key":         key,
			"anthropic-version": "2023-06-01",
		}
	}
	if strings.HasSuffix(base, "/models") {
		return base, map[string]string{"Authorization": "Bearer " + key}
	}
	return base + "/models", map[string]string{"Authorization": "Bearer " + key}
}
