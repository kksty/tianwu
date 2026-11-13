package host

// The harness fork removes tools via CYBER_EXCLUDE_TOOLS (task-33). Tianwu pins
// that variable on every harness process it starts: the default excludes the
// native subagent tool, and --allow-native-subagent is the escape hatch.

import (
	"strings"
	"testing"
)

// A stale inherited value must not win: setEnv keeps exactly one definition.
func TestWithExcludeToolsReplacesInheritedValue(t *testing.T) {
	env := []string{"PATH=C:\\bin", "CYBER_EXCLUDE_TOOLS=ioa", "CYBER_MODEL=x"}
	got := withExcludeTools(env, "subagent")
	count := 0
	for _, kv := range got {
		if strings.HasPrefix(kv, "CYBER_EXCLUDE_TOOLS=") {
			count++
			if kv != "CYBER_EXCLUDE_TOOLS=subagent" {
				t.Fatalf("exclusion = %q", kv)
			}
		}
	}
	if count != 1 {
		t.Fatalf("CYBER_EXCLUDE_TOOLS appears %d times: %v", count, got)
	}
	if len(got) != 3 {
		t.Fatalf("env length = %d, want 3: %v", len(got), got)
	}
}

// Empty value means "leave the child environment alone": --allow-native-subagent
// must not inject an empty exclusion that a harness might read as a decision.
func TestWithExcludeToolsEmptyLeavesEnvAlone(t *testing.T) {
	env := []string{"PATH=C:\\bin"}
	got := withExcludeTools(env, "")
	if strings.Join(got, "|") != strings.Join(env, "|") {
		t.Fatalf("env changed: %v", got)
	}
	if got2 := withExcludeTools(got, "  "); strings.Join(got2, "|") != strings.Join(env, "|") {
		t.Fatalf("whitespace value changed env: %v", got2)
	}
}

// The profile host pool forwards the instance's exclusion to each launched host.
func TestPoolPassesExcludeToolsToEveryHost(t *testing.T) {
	f := newPoolFixture(t)
	f.pool.cfg.ExcludeTools = "subagent"
	if _, err := f.pool.Executor("fast"); err != nil {
		t.Fatal(err)
	}
	if cfg := f.lastConfig(t); cfg.ExcludeTools != "subagent" {
		t.Fatalf("host config exclusion = %q", cfg.ExcludeTools)
	}
}
