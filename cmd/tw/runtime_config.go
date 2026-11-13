package main

import (
	"path/filepath"

	"github.com/kksty/tianwu/internal/settings"
)

// runtimeHarnessArgs is shared by boot and settings-driven restarts. Passing an
// explicit, filtered document prevents the harness from discovering and parsing
// the operator's file, where Tianwu keeps metadata such as profile pause state.
func runtimeHarnessArgs(st *settings.Store, dataDir string, m settings.Model) ([]string, error) {
	path, err := st.RuntimeConfig(filepath.Join(dataDir, "host", "config"))
	if err != nil {
		return nil, err
	}
	return append([]string{"--config", path}, m.Args()...), nil
}
