package project

import (
	"fmt"
	"strings"
)

// Premise admission has exactly one mode: verified_only (证明优先). Every
// necessary premise must be a reviewable kind, verified at its current claim
// version, with a receipt bound to the current claim and intact original
// evidence. Legacy standard/strict manifests are migrated to this value when the
// project is loaded; they are not served as a read-only compatibility mode.
const PremisePolicyVerifiedOnly = "verified_only"

// PremisePolicyView is the effective policy. There is no second mode.
func (p *Project) PremisePolicyView() string {
	return PremisePolicyVerifiedOnly
}

// SetPremisePolicy validates and persists the only supported mode. It is
// idempotent so a settings card can include the field without a special case.
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

// NormalizePremisePolicy validates an operator-supplied mode before any setter
// writes it. It is exported for callers that edit several project fields in one
// request and must reject the whole request before persisting an earlier field.
func NormalizePremisePolicy(mode string) (string, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == PremisePolicyVerifiedOnly {
		return PremisePolicyVerifiedOnly, nil
	}
	switch mode {
	case "standard", "strict":
		return "", fmt.Errorf("premise_policy 的 %s 模式已移除：唯一模式是 verified_only；旧项目会在加载时迁移", mode)
	default:
		return "", fmt.Errorf("premise_policy 只能是 verified_only（收到 %q）", mode)
	}
}
