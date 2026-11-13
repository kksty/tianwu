package project

import "testing"

func TestPremisePolicyDefaultsToStandardAndPersistsStrict(t *testing.T) {
	root := t.TempDir()
	m, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	p, err := m.Create("strict project", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := p.PremisePolicyView(); got != PremisePolicyStandard {
		t.Fatalf("default policy = %q, want standard", got)
	}
	if _, err := p.SetPremisePolicy("sometimes"); err == nil {
		t.Fatal("invalid policy accepted")
	}
	if got := p.PremisePolicyView(); got != PremisePolicyStandard {
		t.Fatalf("invalid set changed the policy: %q", got)
	}
	if _, err := p.SetPremisePolicy(PremisePolicyStrict); err != nil {
		t.Fatal(err)
	}
	// Re-open from disk: the operator's mode survives a restart.
	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Get(p.ID).PremisePolicyView(); got != PremisePolicyStrict {
		t.Fatalf("persisted policy = %q, want strict", got)
	}
}
