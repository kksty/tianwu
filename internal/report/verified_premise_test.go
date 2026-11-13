package report

import (
	"testing"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

func TestVerifiedReportDoesNotHideChangedProducingPremise(t *testing.T) {
	base := model.Fact{ID: "base", ClaimVersion: 2, ClaimHash: "changed", Status: "verified", Kind: "host", EvidenceIDs: []string{"e"}}
	finding := model.Fact{ID: "finding", IntentID: "task", SourceRunID: "run", ClaimVersion: 1, ClaimHash: "claim", Status: "verified", Kind: "vuln"}
	db := store.DB{Facts: []model.Fact{base, finding}, Intents: []model.Intent{{ID: "task", FromFactIDs: []string{"base"}}}, Runs: []model.Run{{ID: "run", PremiseClaimVersions: map[string]uint64{"base": 1}}}, Reviews: []model.Review{{FactID: "finding", ClaimVersion: 1, ClaimHash: "claim", Verdict: "pass", VerifierRun: "review"}}}
	checks := (&Generator{}).validatePremises(db, []model.Fact{finding})
	if len(checks) != 1 || checks[0].OK {
		t.Fatalf("changed provenance was hidden: %+v", checks)
	}
}

func TestVerifiedReportUsesFrozenDependenciesAfterIntentEdit(t *testing.T) {
	finding := model.Fact{ID: "finding", IntentID: "task", SourceRunID: "run", ClaimVersion: 1, ClaimHash: "claim", Status: "verified", Kind: "vuln"}
	base := model.Fact{ID: "base", ClaimVersion: 2, Status: "verified", Kind: "host"}
	db := store.DB{
		Facts:   []model.Fact{base, finding},
		Intents: []model.Intent{{ID: "task"}}, // dependency removed after execution
		Runs:    []model.Run{{ID: "run", PremisePolicy: model.PremiseVerifiedOnly, PremiseClaimVersions: map[string]uint64{"base": 1}}},
		Reviews: []model.Review{{FactID: "finding", ClaimVersion: 1, ClaimHash: "claim", Verdict: "pass", VerifierRun: "review"}},
	}
	checks := (&Generator{}).validatePremises(db, []model.Fact{finding})
	if len(checks) != 1 || checks[0].OK || checks[0].Rule != "verified_premise:finding:base" {
		t.Fatalf("removed dependency erased producing provenance: %+v", checks)
	}
	// A dependency added after an independent run is not a historical input.
	db.Runs[0].PremiseClaimVersions = nil
	db.Intents[0].FromFactIDs = []string{"base"}
	if checks = (&Generator{}).validatePremises(db, []model.Fact{finding}); len(checks) != 0 {
		t.Fatalf("new dependency was retroactively attributed: %+v", checks)
	}
}
