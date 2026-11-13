//go:build !windows

package host

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// listProcessesPlatform scans /proc for harness processes. Linux only: other
// Unixes have no stable process table this cheap, and the host feature is
// documented for Linux and Windows.
func listProcessesPlatform(ctx context.Context) ([]procInfo, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("读 /proc：%w", err)
	}
	var out []procInfo
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		cmdline, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil || len(cmdline) == 0 {
			continue
		}
		args := strings.Split(strings.TrimRight(string(cmdline), "\x00"), "\x00")
		if len(args) == 0 {
			continue
		}
		exe, _ := os.Readlink(filepath.Join("/proc", e.Name(), "exe"))
		info := procInfo{
			PID:            pid,
			ParentPID:      readParentPID(e.Name()),
			Name:           filepath.Base(args[0]),
			CommandLine:    strings.Join(args, " "),
			ExecutablePath: exe,
		}
		out = append(out, info)
	}
	return out, nil
}

// readParentPID reads PPid from /proc/<pid>/status.
func readParentPID(pid string) int {
	data, err := os.ReadFile(filepath.Join("/proc", pid, "status"))
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "PPid:") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "PPid:")))
		if err == nil {
			return n
		}
	}
	return 0
}

// terminateProcessPlatform sends SIGTERM first (so the host can reap its PTY
// children), then SIGKILL after a short grace period.
func terminateProcessPlatform(pid int, tryGraceful bool) error {
	if pid <= 0 {
		return fmt.Errorf("无效 PID %d", pid)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if tryGraceful {
		if err := proc.Signal(syscall.SIGTERM); err == nil {
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				if !processAlive(pid) {
					return nil
				}
				time.Sleep(100 * time.Millisecond)
			}
		}
	}
	if err := proc.Signal(syscall.SIGKILL); err != nil {
		return fmt.Errorf("kill %d：%w", pid, err)
	}
	return nil
}

// processAlive reports whether a PID still exists.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil
}
