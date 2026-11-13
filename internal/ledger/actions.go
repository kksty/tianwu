package ledger

import (
	"fmt"
	"strings"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// ValidateComplete is the one implementation of "a goal may only be closed with
// evidence and a reason" (D55, D96, D97). It returns the first evidence id among
// the cited facts so the caller can pin the goal with it. The decider's parse
// step and the control-plane API both call it, so the two contracts cannot drift.
//
// It deliberately has no "at least one verified" rule (revokes D66's gate):
// whether the goal is met is the commander's judgement against the goal's own
// statement + start, and the note is where that judgement is
// written. The engine only enforces the receipt: every cited fact must carry
// its own evidence, and the reason must exist. Verification remains a delivery
// gate (a report's confirmed finding must be verified, report/validate.go), so a
// premature close cannot silently become a premature delivery (D97).
func ValidateComplete(st *store.Store, goal string, from []string, note string) (string, error) {
	goal = strings.TrimSpace(goal)
	if goal != "" {
		goals := st.Goals()
		found := false
		for i := range goals {
			if goals[i].ID != goal {
				continue
			}
			if goals[i].Met {
				return "", fmt.Errorf("目标 %s 已经达成", goal)
			}
			found = true
			break
		}
		if !found {
			return "", fmt.Errorf("complete 引用了不存在的目标：%s", goal)
		}
	}
	if strings.TrimSpace(note) == "" {
		return "", fmt.Errorf("complete 必须写 note：说清目标口径怎么被证据满足")
	}
	if len(from) == 0 {
		return "", fmt.Errorf("complete 必须引用事实（from）")
	}
	evidence := ""
	for _, id := range from {
		id = strings.TrimSpace(id)
		f, ok := st.FactByID(id)
		if !ok {
			return "", fmt.Errorf("complete 引用了不存在的事实：%s", id)
		}
		if f.Status == "rejected" {
			return "", fmt.Errorf("complete 不能依赖已否定的事实：%s", id)
		}
		if len(f.EvidenceIDs) == 0 {
			return "", fmt.Errorf("complete 引用的每条事实都必须有自己的证据：%s 没有实测（收据上的每条依据都要能点开）", id)
		}
		if evidence == "" {
			evidence = f.EvidenceIDs[0]
		}
	}
	return evidence, nil
}

// CompleteGoal validates and pins. An empty goal means the first unmet one (the
// single-goal shape); the caller never invents a goal id.
func CompleteGoal(st *store.Store, goal string, from []string, note string, policy ...string) (model.Goal, error) {
	evidence, err := ValidateComplete(st, goal, from, note)
	if err != nil {
		return model.Goal{}, err
	}
	if strings.TrimSpace(goal) == "" {
		for _, g := range st.Goals() {
			if !g.Met {
				goal = g.ID
				break
			}
		}
		if goal == "" {
			return model.Goal{}, fmt.Errorf("complete 被忽略：没有未达成的目标可盖章")
		}
	}
	return st.PinGoal(goal, evidence, note, from, policy...)
}

// ValidatePlaybook normalises and checks a commander nomination without writing.
// It returns the unknown fact ids it dropped so the caller can record them.
func ValidatePlaybook(st *store.Store, pb model.Playbook) (model.Playbook, []string, error) {
	pb.Title = strings.TrimSpace(pb.Title)
	pb.When = strings.TrimSpace(pb.When)
	pb.Doc = strings.TrimSpace(pb.Doc)
	pb.Outcome = strings.ToLower(strings.TrimSpace(pb.Outcome))
	if pb.Title == "" || pb.When == "" {
		return pb, nil, fmt.Errorf("playbook 需要 title 与 when")
	}
	if len([]rune(pb.Title)) > 40 {
		return pb, nil, fmt.Errorf("playbook 的 title 超过 40 字：它是索引，不是摘要")
	}
	if len([]rune(pb.When)) > 300 {
		return pb, nil, fmt.Errorf("playbook 的 when 超过 300 字")
	}
	if len([]rune(pb.Doc)) > 1500 {
		return pb, nil, fmt.Errorf("playbook 的 doc 超过 1500 字")
	}
	if pb.Outcome == "" {
		pb.Outcome = model.PlaybookSuccess
	}
	if pb.Outcome != model.PlaybookSuccess && pb.Outcome != model.PlaybookDeadEnd {
		return pb, nil, fmt.Errorf("playbook 的 outcome 只能是 success 或 dead_end")
	}
	from := make([]string, 0, len(pb.FromFactIDs))
	var dropped []string
	seen := make(map[string]bool, len(pb.FromFactIDs))
	for _, id := range pb.FromFactIDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		if _, ok := st.FactByID(id); ok {
			seen[id] = true
			from = append(from, id)
		} else {
			dropped = append(dropped, id)
		}
	}
	if len(from) == 0 {
		return pb, dropped, fmt.Errorf("playbook 必须引用至少一条已有事实（from_fact_ids）：没有观测就不算知识")
	}
	pb.FromFactIDs = from
	pb.Tags = cleanList(pb.Tags)
	if len(pb.Tags) > 8 {
		pb.Tags = pb.Tags[:8]
	}
	pb.Origin = model.PlaybookFromCommander
	pb.Status = model.PlaybookCandidate
	return pb, dropped, nil
}

// NominatePlaybook validates and writes a candidate row. A confirmed entry is
// never overwritten by a nomination: the store decides that and reports whether
// it changed anything.
func NominatePlaybook(st *store.Store, pb model.Playbook) (model.Playbook, bool, error) {
	pb, _, err := ValidatePlaybook(st, pb)
	if err != nil {
		return model.Playbook{}, false, err
	}
	return st.UpsertPlaybook(pb)
}

// cleanList trims, drops empties and de-duplicates while keeping order.
func cleanList(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}
