//go:build windows

package executor

import (
	"os/exec"
	"strconv"
	"syscall"
)

// setProcAttr is a no-op on Windows: the subprocess tree is terminated by
// taskkill /T, which walks parent-child links rather than a process group.
func setProcAttr(cmd *exec.Cmd) {}

// killGroup terminates the harness *and everything it started*. Process.Kill
// alone only kills the harness binary; the agent's tools (nmap, browsers,
// loaders) are children and would keep running after a cancel. taskkill /T
// walks the tree, /F forces it, and /PID pins it to this run.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pid := strconv.Itoa(cmd.Process.Pid)
	killer := exec.Command("taskkill", "/T", "/F", "/PID", pid)
	killer.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = killer.Run()
	// Fallback for hosts where taskkill is unavailable (or the tree is already
	// gone): make sure at least the harness itself is dead.
	_ = cmd.Process.Kill()
}
