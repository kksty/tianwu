package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The binding is a human decision written to project.json: bound nodes are an
// allowlist, the default must be one of them, and an empty list is legal only
// with an empty default (nothing can run yet).
func TestSetNodeBindingValidates(t *testing.T) {
	p := &Project{Dir: t.TempDir()}
	if p.BindingConfigured() {
		t.Fatal("a fresh project must not look configured")
	}

	if err := p.SetNodeBinding([]string{"lab"}, ""); err == nil {
		t.Fatal("binding a node without a default must fail")
	}
	if err := p.SetNodeBinding([]string{"lab"}, "other"); err == nil {
		t.Fatal("a default outside the allowlist must fail")
	}
	if err := p.SetNodeBinding(nil, "lab"); err == nil {
		t.Fatal("a default without nodes must fail")
	}
	if err := p.SetNodeBinding([]string{"lab", "lab"}, "lab"); err != nil {
		t.Fatalf("dedupe failed: %v", err)
	}
	if !p.BindingConfigured() {
		t.Fatal("a written binding must be marked configured")
	}
	nodes, def := p.NodeBinding()
	if len(nodes) != 1 || nodes[0] != "lab" || def != "lab" {
		t.Fatalf("binding = %v/%q", nodes, def)
	}

	raw, err := os.ReadFile(filepath.Join(p.Dir, ManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"default_node": "lab"`) {
		t.Fatalf("manifest missing default_node:\n%s", raw)
	}

	if err := p.SetNodeBinding(nil, ""); err != nil {
		t.Fatalf("clearing failed: %v", err)
	}
	if nodes, def := p.NodeBinding(); len(nodes) != 0 || def != "" {
		t.Fatalf("after clear = %v/%q", nodes, def)
	}
}
