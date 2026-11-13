package api

import (
	"strings"
	"testing"
)

// A vuln's `doc` is the only material a verifier can re-run and the report can
// turn into repro steps, so the handoff contract must require the reproducer,
// the observed packets and the criterion — not just "write what you did" (D105).
//
// This lives in its own file because prompt_test.go carries unrelated
// in-progress edits.
func TestHandoffRequiresVulnReproMaterial(t *testing.T) {
	var b strings.Builder
	writeHandoff(&b)
	p := b.String()
	for _, want := range []string{"最小复现命令", "请求报文", "响应报文", "判据"} {
		if !strings.Contains(p, want) {
			t.Errorf("handoff prompt missing %q:\n%s", want, p)
		}
	}
	// The requirement is about content, not a new tool/step recipe: it must not
	// grow into a per-kind checklist.
	if n := strings.Count(p, "`vuln` 的 `doc`"); n != 1 {
		t.Errorf("expected exactly one vuln doc bullet, got %d:\n%s", n, p)
	}
}
