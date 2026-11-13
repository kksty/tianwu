package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/kksty/tianwu/internal/settings"
)

// Model profiles are the harness's llm.providers entries. This endpoint is the
// instance-level management face; choosing one for a direction happens under
// /p/<project>/api/model-profiles and on POST /p/<project>/api/intents.
//
// A saved profile is configuration, not a running binding: an edit here affects
// the *next* run that selects it. A host already serving that profile keeps its
// configuration and its sessions until the next run needs the new revision.

func (h *hub) serveModelProfiles(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		// Probed: the console should show "configured but the gateway does not
		// serve this model" at the point where a human can fix it (task-13).
		cat, err := h.settings.ProfilesProbed()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, cat)
	case http.MethodPost:
		h.saveModelProfile(w, r, "")
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "只支持 GET / POST"})
	}
}

func (h *hub) serveModelProfile(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/model-profiles/")
	if id, ok := strings.CutSuffix(rest, "/pause"); ok {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "只支持 POST"})
			return
		}
		var body struct {
			Paused *bool `json:"paused"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Paused == nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "paused 必须明确为 true 或 false"})
			return
		}
		// Serialize the toggle with worker admission through its durable run.
		h.dispatchMu.Lock()
		p, err := h.settings.SetProfilePaused(strings.TrimSpace(id), *body.Paused)
		h.dispatchMu.Unlock()
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, settings.ErrProfileNotFound) {
				status = http.StatusNotFound
			}
			writeJSON(w, status, map[string]any{"error": err.Error()})
			return
		}
		h.mu.RLock()
		for _, k := range h.kits {
			if k.dec != nil {
				if name, window := k.srv.CommanderModelInfo(); name != "" {
					k.dec.SetModelName(name)
					k.dec.SetContextWindow(window)
				}
				k.dec.Wake("model_profile_admission_changed")
			}
		}
		h.mu.RUnlock()
		// Admission metadata changes never restart or cancel a live host.
		writeJSON(w, http.StatusOK, map[string]any{"id": p.ID, "paused": p.Paused, "runnable": p.Runnable()})
		return
	}
	if id, ok := strings.CutSuffix(rest, "/activate"); ok {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "只支持 POST"})
			return
		}
		h.activateModelProfile(w, strings.TrimSpace(id))
		return
	}
	if id, ok := strings.CutSuffix(rest, "/probe"); ok {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "只支持 POST"})
			return
		}
		h.probeModelProfile(w, r, strings.TrimSpace(id))
		return
	}
	id := strings.TrimSpace(rest)
	if id == "" {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "模型配置 id 为空"})
		return
	}
	switch r.Method {
	case http.MethodPut:
		h.saveModelProfile(w, r, id)
	case http.MethodDelete:
		if err := h.settings.DeleteProfile(id); err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, settings.ErrProfileNotFound) {
				status = http.StatusNotFound
			}
			writeJSON(w, status, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
	default:
		w.Header().Set("Allow", "PUT, DELETE")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "只支持 PUT / DELETE"})
	}
}

// probeModelProfile makes one explicit inference request with saved credentials.
// It reports the model supplied by the response, never a configured fallback.
func (h *hub) probeModelProfile(w http.ResponseWriter, r *http.Request, id string) {
	if h.settings == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "模型配置不可用"})
		return
	}
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "模型配置 id 为空"})
		return
	}
	p, err := h.settings.SavedProfile(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	probe := h.settings.CheckProfile(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]any{
		"id":             p.ID,
		"model":          p.Model,
		"returned_model": probe.ReturnedModel,
		"probe_kind":     probe.Kind,
		"latency_ms":     probe.MS,
		"base_url":       p.BaseURL,
		"local":          p.LocalEndpoint(),
		"probed":         probe.Probed,
		"available":      probe.Available,
		"probe_error":    probe.Error,
		"at":             probe.At,
	})
}

// serveModelCatalogue is the settings page's "检测选取模型" action: it asks the
// draft's endpoint which models it serves, so the operator can pick an exact
// model id (or confirm the typed one) before saving. The API key is transient:
// it arrives in the body, is used for one GET and is never echoed or stored.
func (h *hub) serveModelCatalogue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "只支持 POST"})
		return
	}
	if h.settings == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "模型配置不可用"})
		return
	}
	var in struct {
		BaseURL string `json:"base_url"`
		Model   string `json:"model"`
		APIKey  string `json:"api_key"`
		ID      string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求体不是 JSON"})
		return
	}
	if strings.TrimSpace(in.BaseURL) == "" && strings.TrimSpace(in.ID) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "检测模型前先填 Base URL"})
		return
	}
	cat := h.settings.Catalogue(in.BaseURL, in.ID, in.APIKey)
	model := strings.TrimSpace(in.Model)
	available := false
	for _, id := range cat.Models {
		if model != "" && id == model {
			available = true
			break
		}
	}
	resp := map[string]any{
		"base_url": strings.TrimSpace(in.BaseURL),
		"endpoint": cat.Endpoint,
		"local":    cat.Local,
		"probed":   cat.Probed,
		"models":   cat.Models,
		"count":    len(cat.Models),
	}
	if model != "" {
		resp["model"] = model
		resp["available"] = available
	}
	if cat.Error != "" {
		resp["error"] = cat.Error
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *hub) saveModelProfile(w http.ResponseWriter, r *http.Request, id string) {
	var in settings.Profile
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求体不是 JSON"})
		return
	}
	if id != "" {
		in.ID = id
	}
	saved, err := h.settings.SaveProfile(in)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, settings.ErrProfileNotFound) {
			status = http.StatusNotFound
		}
		writeJSON(w, status, map[string]any{"error": err.Error()})
		return
	}
	// Editing the active entry is an instance settings change: the default host
	// read that profile at startup and must be restarted, exactly as the
	// settings form does. Editing another profile touches only its next run.
	if active := h.activeProfileID(); active == saved.ID {
		h.applyModel(h.settings.Effective())
	}
	view, ok := h.modelProfileView(saved.ID)
	if !ok {
		// The save succeeded; a read-back failure must not turn it into an error.
		writeJSON(w, http.StatusOK, map[string]any{"id": saved.ID})
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (h *hub) activateModelProfile(w http.ResponseWriter, id string) {
	if id == "" {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "模型配置 id 为空"})
		return
	}
	saved, err := h.settings.ActivateProfile(id)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, settings.ErrProfileNotFound) {
			status = http.StatusNotFound
		}
		writeJSON(w, status, map[string]any{"error": err.Error()})
		return
	}
	// The default host and the commander read the active profile at process
	// start; restart the host (its running runs fail and can be retried) so the
	// instance default really is the profile the console now shows.
	h.applyModel(h.settings.Effective())
	view, ok := h.modelProfileView(saved.ID)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"id": saved.ID, "active": true})
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (h *hub) activeProfileID() string {
	if h.settings == nil {
		return ""
	}
	cat, err := h.settings.Profiles()
	if err != nil {
		return ""
	}
	return cat.ActiveProfile
}

func (h *hub) modelProfileView(id string) (settings.ProfileView, bool) {
	cat, err := h.settings.ProfilesProbed()
	if err != nil {
		return settings.ProfileView{}, false
	}
	for _, p := range cat.Profiles {
		if p.ID == id {
			return p, true
		}
	}
	return settings.ProfileView{}, false
}
