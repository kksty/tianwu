package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/kksty/tianwu/internal/model"
)

// listModelProfiles is the executor/commander view of the instance's saved
// model configurations: enough to choose one (id, model, whether it can run),
// never a credential.
func (s *Server) listModelProfiles(w http.ResponseWriter, r *http.Request) {
	if s.Profiles == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"profiles": []ProfileInfo{}, "active": "", "local_node": s.LocalNode,
		})
		return
	}
	profiles, active, err := s.Profiles()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if profiles == nil {
		profiles = []ProfileInfo{}
	}
	// Running is live occupancy per profile: the console can show "2/4" next to
	// a profile, and an agent choosing one can see when it would have to wait.
	// It is derived per request from the ledger; it is not stored capacity.
	counts := s.profileRunningCounts()
	for i := range profiles {
		profiles[i].Running = counts[profiles[i].ID]
		if s.GlobalProfileRunning != nil {
			profiles[i].Running = s.GlobalProfileRunning(profiles[i].ID)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"profiles": profiles, "active": active, "local_node": s.LocalNode,
	})
}

// profileConcurrencyLimit returns one saved profile's max_concurrency cap.
// ok=false means "this instance has no profile catalogue or the profile is
// gone": legacy/flat deployments keep the old behaviour and are not gated.
func (s *Server) profileConcurrencyLimit(profileID string) (int, bool) {
	profileID = strings.TrimSpace(profileID)
	if profileID == "" || s.Profiles == nil {
		return 0, false
	}
	profiles, _, err := s.Profiles()
	if err != nil {
		return 0, false
	}
	for _, p := range profiles {
		if p.ID == profileID {
			return p.MaxConcurrency, true
		}
	}
	return 0, false
}

// runningProfileCount counts live intent runs bound to one resolved profile.
// Purpose runs (commander/verifier/report) are not workers and do not consume
// the profile's worker capacity, matching the scope of max_runs/--max-workers.
func (s *Server) runningProfileCount(profileID string) int {
	if profileID == "" {
		return 0
	}
	n := 0
	for _, r := range s.Store.Runs() {
		if r.Status == model.RunRunning && r.Purpose == "" && r.ResolvedProfile == profileID {
			n++
		}
	}
	return n
}

// profileRunningCounts is runningProfileCount for every profile in one pass.
func (s *Server) profileRunningCounts() map[string]int {
	out := map[string]int{}
	for _, r := range s.Store.Runs() {
		if r.Status == model.RunRunning && r.Purpose == "" && r.ResolvedProfile != "" {
			out[r.ResolvedProfile]++
		}
	}
	return out
}

// checkProfileCapacity gates every new run under the instance admission lock.
//
// The target is the run's resolved profile, which is the same binding chain the
// dispatch used (intent.Profile → project worker_profile → instance active):
// gating on anything re-derived here would be a second resolution that could
// disagree with the run row.
func (s *Server) checkProfileCapacity(run model.Run) error {
	limit, ok := s.profileConcurrencyLimit(run.ResolvedProfile)
	if !ok || limit <= 0 {
		return nil
	}
	running := s.runningProfileCount(run.ResolvedProfile)
	if s.GlobalProfileRunning != nil {
		running = s.GlobalProfileRunning(run.ResolvedProfile)
	}
	if running >= limit {
		return fmt.Errorf("模型配置 %s 并发已到上限 %d（max_concurrency）：等一个 run 结束再派发",
			run.ResolvedProfile, limit)
	}
	return nil
}

// profileBinding resolves one saved profile and refuses anything that cannot
// actually start a host, or whose gateway does not serve its model. An explicit
// selection never falls through to the default model: a typo must be a visible
// error, not a silent switch.
func (s *Server) profileBinding(id string) (ProfileInfo, error) {
	id = strings.TrimSpace(id)
	if s.Profiles == nil {
		return ProfileInfo{}, errors.New("这个实例没有模型配置目录")
	}
	profiles, active, err := s.Profiles()
	if err != nil {
		return ProfileInfo{}, fmt.Errorf("读模型配置：%w", err)
	}
	for _, p := range profiles {
		if p.ID != id {
			continue
		}
		if p.Paused {
			return ProfileInfo{}, fmt.Errorf("模型配置 %s 已暂停：先在设置中恢复使用", id)
		}
		if !p.Runnable {
			return ProfileInfo{}, fmt.Errorf("模型配置 %s 还不能执行：base_url、model、api_key 必须齐全", id)
		}
		// Runnable only says the fields are filled in. The probe says whether the
		// gateway actually lists the model; a definitive no must stop dispatch
		// here, before a run row or a host exists (task-13, docs/17 §1.4.3).
		// Probed=false means "no verdict yet" and is deliberately allowed: an
		// instance without a settings store (or a test server) must keep working.
		if p.Probed && !p.Available {
			reason := strings.TrimSpace(p.ProbeError)
			if reason == "" {
				reason = "端点的模型清单里没有这个模型"
			}
			return ProfileInfo{}, fmt.Errorf("模型配置 %s 的模型 %s 不可用：%s", id, p.Model, reason)
		}
		p.Active = p.ID == active
		return p, nil
	}
	known := make([]string, 0, len(profiles))
	for _, p := range profiles {
		known = append(known, p.ID)
	}
	if len(known) == 0 {
		return ProfileInfo{}, fmt.Errorf("没有模型配置 %s（这个实例还没有保存任何配置）", id)
	}
	return ProfileInfo{}, fmt.Errorf("没有模型配置 %s（现有：%s）", id, strings.Join(known, ", "))
}

// activeProfile is the saved instance default, or "" when the deployment uses
// flat/env configuration only.
func (s *Server) activeProfile() string {
	if s.Profiles == nil {
		return ""
	}
	_, active, err := s.Profiles()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(active)
}
