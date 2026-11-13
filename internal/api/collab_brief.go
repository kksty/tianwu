package api

// Collaboration capability inventory for a dispatched executor.
//
// Alignment with sapper matters here: its workers learn that they can message
// the commander and share a fact graph from the mission text, not by guessing.
// Tianwu already has the mechanisms (participants, child intents, model
// profiles, per-run identity) but a worker that never reads the collaborate
// skill cannot know they exist. This section lists only *what is available* —
// no commands, no workflow, no role names — so a model that decides it needs a
// peer or another model tier can find the contract on its own (docs/17 §3.2/§3.3).

import (
	"fmt"
	"strings"

	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/model"
)

const (
	collabBriefMaxPeers  = 6
	collabBriefMaxModels = 6
)

// collaborationSection returns "" when there is nothing the executor could use
// (one direction, one model): a solo run should not pay for this section.
func (s *Server) collaborationSection(self model.Intent) string {
	latest := s.latestRuns()
	var peers []string
	for _, in := range s.Store.ListIntents() {
		if in.ID == self.ID {
			continue
		}
		switch in.Status {
		case model.StatusDone, model.StatusFailed, model.StatusParked:
			continue // no live holder: a message would be queued forever
		}
		// The peer's tier is the one frozen on its current run. It is optional
		// information: a direction that has not run yet has no run to read it
		// from, and its intent only carries the profile id.
		tier := ""
		if run, ok := latest[in.ID]; ok && run.Tier != "" {
			tier = "·" + run.Tier
		}
		peers = append(peers, fmt.Sprintf("%s（%s，%s%s）",
			clipRunes(in.Title, 40), collab.ParticipantID(in.ID), in.Status, tier))
		if len(peers) >= collabBriefMaxPeers {
			break
		}
	}
	var models []string
	if s.Profiles != nil {
		if profiles, _, err := s.Profiles(); err == nil {
			for _, p := range profiles {
				if !p.Runnable {
					continue
				}
				tier := strings.TrimSpace(p.Tier)
				if tier == "" {
					tier = "未分级"
				}
				models = append(models, fmt.Sprintf("%s（%s，%s）", p.ID, tier, p.Model))
				if len(models) >= collabBriefMaxModels {
					break
				}
			}
		}
	}
	if len(peers) == 0 && len(models) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n## 自主协作（不必经过指挥官）\n")
	b.WriteString("协作是手段不是流程：先在事实与同伴里找已有结论，能独立完成就直接做；需要并行、独立复核或共享现场时，直接给同伴发消息或在当前方向下派子任务，不必先请示。不要为凑层级拆任务，也不要重复别人的方向；共享现场/账号冲突先协调。是否协作、何时委派及选哪档模型由你判断。\n")
	if len(peers) > 0 {
		b.WriteString("- 同伴：" + strings.Join(peers, "；") + "\n")
	}
	if len(models) > 0 {
		b.WriteString("- 可用模型配置：" + strings.Join(models, "；") + "\n")
	}
	b.WriteString("- 可用动作：给同伴/指挥官发消息；在当前方向下派子任务（可选带强档 profile）；申请换模型接班。\n")
	b.WriteString("- 现场交接：可直接使用意图/插话提供的账号登录；扫码、验证码或人工确认时，可随人工请求关联自己的可见浏览器 session，宿主自动跟随本轮；人工完成后继续。多站点按 URL 选择页面，同站点多账号使用独立浏览器上下文。原生结果可按需查询。\n")
	b.WriteString("- 契约与身份：harness skill `collaborate`（`cyber://skills/collaborate/SKILL.md`），")
	b.WriteString("本轮身份在 `.tw/identity.json`。\n")
	return b.String()
}
