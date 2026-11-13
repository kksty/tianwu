//go:build !windows

package settings

import (
	"os"
	"syscall"
)

// preserveOwner keeps the harness config's owner when the control plane runs as
// a different account than the harness (for example root with
// TW_HARNESS_CONFIG pointing at /home/lab/.cyber/cyber.yaml). The staged file is
// created by this process, so without the chown the rename would leave a
// root-owned 0600 file the harness user can no longer read.
func preserveOwner(path string, info os.FileInfo) {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		_ = os.Chown(path, int(st.Uid), int(st.Gid))
	}
}
