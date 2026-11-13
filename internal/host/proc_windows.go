//go:build windows

package host

import (
	"context"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

// setProcAttr is a no-op on Windows: the host tree is terminated by
// taskkill /T, which walks parent-child links rather than a process group.
func setProcAttr(cmd *exec.Cmd) {}

// killGroup terminates the host *and the shells/agents it started*. Killing the
// host alone would leave its PTY children (cmd.exe, tools) running.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pid := strconv.Itoa(cmd.Process.Pid)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	killer := exec.CommandContext(ctx, "taskkill", "/T", "/F", "/PID", pid)
	killer.WaitDelay = time.Second
	killer.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = killer.Run()
	_ = cmd.Process.Kill()
}
