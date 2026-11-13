package project

import (
	"fmt"
	"strings"
)

// Premise admission modes. Standard is the default when the manifest has no
// policy: a premise must exist and must not be rejected. Strict is the
// operator's opt-in for high-assurance projects: a vuln/cred premise must also
// be verified at its current claim version.
const (
	PremisePolicyStandard     = "standard"
	PremisePolicyStrict       = "strict"
	PremisePolicyVerifiedOnly = "verified_only"
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
	normalized, err := normalizePremisePolicy(mode)
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

func effectivePremisePolicy(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case PremisePolicyVerifiedOnly:
		return PremisePolicyVerifiedOnly
	case PremisePolicyStrict:
		return PremisePolicyStrict
	default:
		return PremisePolicyStandard
	}
}

func normalizePremisePolicy(mode string) (string, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case PremisePolicyVerifiedOnly:
		return PremisePolicyVerifiedOnly, nil
	case PremisePolicyStandard:
		return PremisePolicyStandard, nil
	case PremisePolicyStrict:
		return PremisePolicyStrict, nil
	default:
		return "", fmt.Errorf("premise_policy 只能是 standard、strict 或 verified_only（收到 %q）", mode)
	}
}
