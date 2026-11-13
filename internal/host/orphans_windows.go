//go:build windows

package host

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"syscall"
)

// listProcessesPlatform enumerates candidate host processes through WMI. The
// filter runs in PowerShell rather than Go so the query is cheap: the machine
// may have thousands of processes and only the harness ones matter.
func listProcessesPlatform(ctx context.Context) ([]procInfo, error) {
	const script = `Get-CimInstance Win32_Process -Filter "Name='aiscan.exe' OR Name='aiscan-full.exe'" ` +
		`| Select-Object ProcessId,ParentProcessId,Name,CommandLine,ExecutablePath | ConvertTo-Json -Compress`
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("powershell Get-CimInstance 失败：%w", err)
	}
	procs, err := parseProcessJSON(out)
	if err != nil {
		return nil, fmt.Errorf("解析进程列表：%w", err)
	}
	return procs, nil
}

// terminateProcessPlatform stops one PID. With tryGraceful it first asks
// taskkill without /F (a polite WM_CLOSE equivalent for console apps) and only
// falls back to a forced tree kill; the child's PTY shells belong to the tree.
func terminateProcessPlatform(pid int, tryGraceful bool) error {
	if pid <= 0 {
		return fmt.Errorf("无效 PID %d", pid)
	}
	num := strconv.Itoa(pid)
	if tryGraceful {
		polite := exec.Command("taskkill", "/PID", num)
		polite.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		if err := polite.Run(); err == nil {
			return nil
		}
	}
	forced := exec.Command("taskkill", "/T", "/F", "/PID", num)
	forced.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := forced.Run(); err != nil {
		return fmt.Errorf("taskkill %d：%w", pid, err)
	}
	return nil
}
