package model

import "fmt"

// Premise admission policies. The product has exactly two: standard (default)
// and verified_only (证明优先). The legacy middle mode "strict" was removed; a
// stored strict value is migrated to verified_only by the project layer before
// any policy reaches these helpers, so "strict" is deliberately not valid here.
const (
	PremiseStandard     = "standard"
	PremiseVerifiedOnly = "verified_only"
)

func ValidPremisePolicy(policy string) bool {
	return policy == PremiseStandard || policy == PremiseVerifiedOnly
}

// ReviewableKind describes assertions an independent read-only proof can
// establish. Notes remain reference material; preserving bytes never verifies
// a host, service, credential or business claim by itself.
func ReviewableKind(kind string) bool {
	switch kind {
	case "host", "service", "vuln", "cred", "loot", "artifact":
		return true
	}
	return false
}

// PremiseIssue is the shared logical admission rule. Filesystem integrity is
// checked separately against the frozen evidence rows before model dispatch.
// A rejected fact is unusable in every mode. Under verified_only a necessary
// premise must also be a reviewable kind, verified at its current claim version,
// and backed by at least one original evidence row.
func PremiseIssue(f Fact, policy string) string {
	if f.Status == "rejected" {
		return "事实已被否定"
	}
	if policy != PremiseVerifiedOnly {
		return ""
	}
	if !ReviewableKind(f.Kind) {
		return "此类记录仅作参考，不能作为必要前提"
	}
	if f.Status != "verified" {
		return fmt.Sprintf("断言 v%d 尚未通过独立核验", f.ClaimVersion)
	}
	if len(f.EvidenceIDs) == 0 {
		return "已核验断言缺少可打开的原始证据"
	}
	return ""
}

func CurrentProof(f Fact, reviews []Review) (Review, bool) {
	if f.Status != "verified" {
		return Review{}, false
	}
	for i := len(reviews) - 1; i >= 0; i-- {
		r := reviews[i]
		if r.FactID == f.ID && r.ClaimVersion == f.ClaimVersion && r.ClaimHash == f.ClaimHash && r.Verdict == "pass" && r.VerifierRun != "" {
			return r, true
		}
	}
	return Review{}, false
}
