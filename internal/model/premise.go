package model

import "fmt"

// Premise admission has exactly one mode: verified_only (证明优先). Legacy
// manifests are migrated to it when loaded; there is no standard/read-only
// compatibility path in the model anymore.
const PremiseVerifiedOnly = "verified_only"

func ValidPremisePolicy(policy string) bool {
	return policy == PremiseVerifiedOnly
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
// A necessary premise must be a reviewable kind, verified at its current claim
// version, and backed by at least one original evidence row. The policy
// parameter is retained only so callers that freeze the mode still compile; the
// mode itself is now always verified_only.
func PremiseIssue(f Fact, _ string) string {
	if f.Status == "rejected" {
		return "事实已被否定"
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

// LatestReviewForClaim returns the newest verdict bound to the fact's current
// assertion. A verdict for an older claim version is history, not the current
// review state.
//
// The slice order is deliberately not part of the contract: store.Reviews
// returns newest-first, the persisted ledger keeps append order, and both are
// valid inputs. "Newest" is decided by CreatedAt, with a same-instant tie going
// to the later slice element (the later append).
func LatestReviewForClaim(f Fact, reviews []Review) (Review, bool) {
	var latest Review
	found := false
	for _, r := range reviews {
		if r.FactID != f.ID || r.ClaimVersion != f.ClaimVersion || r.ClaimHash != f.ClaimHash {
			continue
		}
		// !Before covers the equal case, so a same-instant later element wins.
		if !found || !r.CreatedAt.Before(latest.CreatedAt) {
			latest, found = r, true
		}
	}
	return latest, found
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
