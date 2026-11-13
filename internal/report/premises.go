package report

import (
	"fmt"
	"sort"

	"github.com/kksty/tianwu/internal/evidence"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// A confirmed finding and the inputs its producing run used are different
// proofs. A stale input makes this report incomplete, without rewriting either
// the historical finding or an old completion receipt.
func (g *Generator) validatePremises(db store.DB, findings []model.Fact) []Check {
	facts := map[string]model.Fact{}
	runs := map[string]model.Run{}
	ev := map[string]model.Evidence{}
	for _, f := range db.Facts {
		facts[f.ID] = f
	}
	for _, r := range db.Runs {
		runs[r.ID] = r
	}
	for _, e := range db.Evidence {
		ev[e.ID] = e
	}
	checks := []Check{}
	for _, f := range findings {
		if f.Status != "verified" {
			continue
		}
		if _, ok := model.CurrentProof(f, db.Reviews); !ok {
			checks = append(checks, Check{Rule: "current_proof:" + f.ID, OK: false, Detail: "缺少当前断言的独立证明"})
		}
		r := runs[f.SourceRunID]
		ids := map[string]bool{}
		for id := range r.PremiseClaimVersions {
			ids[id] = true
		}
		for id := range r.PremiseVersions {
			ids[id] = true
		}
		// A later edit to the intent cannot erase inputs a producing run used,
		// or make newly added inputs appear in its history. Legacy runs without
		// a frozen policy retain current refs only to report missing versions.
		if r.PremisePolicy == "" {
			for _, in := range db.Intents {
				if in.ID == f.IntentID {
					for _, id := range in.FromFactIDs {
						ids[id] = true
					}
				}
			}
		}
		ordered := make([]string, 0, len(ids))
		for id := range ids {
			ordered = append(ordered, id)
		}
		sort.Strings(ordered)
		for _, id := range ordered {
			p, exists := facts[id]
			why := ""
			if !exists {
				why = "前提不存在"
			} else if version, ok := r.PremiseClaimVersions[id]; !ok || version != p.ClaimVersion {
				why = "来源运行的前提版本缺失或已变化"
			} else if issue := model.PremiseIssue(p, model.PremiseVerifiedOnly); issue != "" {
				why = issue
			} else if _, ok := model.CurrentProof(p, db.Reviews); !ok {
				why = "前提缺少当前证明"
			}
			if why == "" {
				for _, eid := range p.EvidenceIDs {
					e, ok := ev[eid]
					if !ok || e.Digest == "" {
						why = "前提原件缺失"
						break
					}
					if err := evidence.Verify(g.CaseRoot, e); err != nil {
						why = err.Error()
						break
					}
				}
			}
			checks = append(checks, Check{Rule: "verified_premise:" + f.ID + ":" + id, OK: why == "", Detail: fmt.Sprintf("%s 断言 v%d：%s", id, p.ClaimVersion, why)})
		}
	}
	return checks
}
