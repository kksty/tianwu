package project

import (
	"fmt"
	"strings"
)

// Premise admission modes. Standard is the default when the manifest has no
// policy: a premise must exist and must not be rejected. VerifiedOnly (证明优先)
// is the operator's opt-in for high-assurance projects: every necessary premise
// must be a reviewable kind, verified at its current claim version, with a
// receipt bound to the current claim and intact original evidence.
//
// The middle "strict" mode was removed: standard and verified_only are the only
// modes now. legacyStrictPremisePolicy survives only as the read-time migration
// of older manifests (see effectivePremisePolicy) and to name the value a fresh
// write is rejected with.
const (
	PremisePolicyStandard     = "standard"
	PremisePolicyVerifiedOnly = "verified_only"

	// legacyStrictPremisePolicy is the removed middle mode. It is never returned
	// by a setter: a manifest that still stores it reads as verified_only.
	legacyStrictPremisePolicy = "strict"
)

// PremisePolicyView returns the effective premise policy: an absent manifest
// policy is reported as standard rather than as an empty mode, so callers can
// show what actually applies.
func (p *Project) PremisePolicyView() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return effectivePremisePolicy(p.PremisePolicy)
}

// SetPremisePolicy validates and persists the operator's premise admission mode.
// A typo is an error, never a silent downgrade to standard: the operator asked
// for a stricter ledger, and quietly ignoring the request would hide it.
func (p *Project) SetPremisePolicy(mode string) (string, error) {
	normalized, err := NormalizePremisePolicy(mode)
	if err != nil {
		return "", err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	previous := p.PremisePolicy
	p.PremisePolicy = normalized
	if err := p.write(); err != nil {
		p.PremisePolicy = previous
		return "", err
	}
	return normalized, nil
}

// Manager.SetPremisePolicy is the instance-level project settings path for the
// same stored mode. It validates before taking the manager lock, so a bad value
// never changes the manifest, and it restores the old value if the write fails.
func (m *Manager) SetPremisePolicy(id, mode string) (string, error) {
	normalized, err := NormalizePremisePolicy(mode)
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.byID[id]
	if !ok {
		return "", fmt.Errorf("没有这个项目：%s", id)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	previous := p.PremisePolicy
	p.PremisePolicy = normalized
	if err := p.write(); err != nil {
		p.PremisePolicy = previous
		return "", err
	}
	return normalized, nil
}

func effectivePremisePolicy(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case PremisePolicyVerifiedOnly:
		return PremisePolicyVerifiedOnly
	case legacyStrictPremisePolicy:
		// The strict mode was removed from the product. Read an existing
		// manifest that still stores it as verified_only, never as standard:
		// verified_only is a superset of strict's guarantee (all necessary
		// reviewable premises must be current-verified), so treating the legacy
		// value as standard would silently weaken a constraint the operator
		// chose. The read is effective immediately without rewriting the
		// manifest; the next explicit settings save persists verified_only, and
		// NormalizePremisePolicy rejects a fresh "strict" write.
		return PremisePolicyVerifiedOnly
	default:
		return PremisePolicyStandard
	}
}

// NormalizePremisePolicy validates an operator-supplied mode before any setter
// writes it. It is exported for callers that edit several project fields in one
// request and must reject the whole request before persisting an earlier field.
func NormalizePremisePolicy(mode string) (string, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case PremisePolicyVerifiedOnly:
		return PremisePolicyVerifiedOnly, nil
	case PremisePolicyStandard:
		return PremisePolicyStandard, nil
	case legacyStrictPremisePolicy:
		return "", fmt.Errorf("premise_policy 的 strict 模式已移除，旧清单里的 strict 会按 verified_only 读取且本次未改写清单；请改为 standard 或 verified_only（收到 %q）", mode)
	default:
		return "", fmt.Errorf("premise_policy 只能是 standard 或 verified_only（收到 %q）", mode)
	}
}
