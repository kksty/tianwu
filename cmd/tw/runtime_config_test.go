package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/settings"
)

func TestRuntimeHarnessArgsExplicitlyUseFilteredConfiguration(t *testing.T) {
	h, source := profileHub(t)
	if _, err := h.settings.SetProfilePaused("fast", true); err != nil {
		t.Fatal(err)
	}
	m := h.settings.Effective()
	m.ContextWindow, m.MaxTokens = 32000, 8192
	args, err := runtimeHarnessArgs(h.settings, t.TempDir(), m)
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 6 || args[0] != "--config" || !filepath.IsAbs(args[1]) || args[1] == source || strings.Join(args[2:], " ") != "--context-window 32000 --max-tokens 8192" {
		t.Fatalf("runtime arguments=%v", args)
	}
	data, err := os.ReadFile(args[1])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "paused:") {
		t.Fatal("boot/restart configuration still contains admission metadata")
	}
}

// This exercises the actual strict parser without starting a service or making
// a model request. The original operator file fails, while the generated file
// used by boot and settings restarts must validate with the same binary.
func TestRuntimeHarnessConfigStrictParser(t *testing.T) {
	bin := strings.TrimSpace(os.Getenv("TW_AISCAN_BIN"))
	if bin == "" {
		t.Skip("set TW_AISCAN_BIN to validate with the real harness parser")
	}
	h, source := profileHub(t)
	if _, err := h.settings.SetProfilePaused("fast", true); err != nil {
		t.Fatal(err)
	}
	if _, err := h.settings.SaveProfile(settings.Profile{ID: "other", Paused: true, Provider: "openai", BaseURL: "http://127.0.0.1:9", Model: "fixture-model", APIKey: "fixture-key"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.settings.SetProfilePaused("other", true); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	validate := func(args []string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, append(args, "config", "validate")...)
		cmd.Dir = dir
		return cmd.CombinedOutput()
	}
	if out, err := validate([]string{"--config", source}); err == nil || !strings.Contains(string(out), "paused") {
		t.Fatalf("fixture did not reproduce strict parser failure: err=%v output=%s", err, out)
	}
	args, err := runtimeHarnessArgs(h.settings, dir, h.settings.Effective())
	if err != nil {
		t.Fatal(err)
	}
	if out, err := validate(args); err != nil {
		t.Fatalf("filtered runtime configuration rejected: err=%v output=%s", err, out)
	}
}
