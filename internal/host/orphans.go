package host

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// An orphan host is an `aiscan web` this instance started in an earlier life
// and never reaped: the control plane crashed, its child kept the PTY, the
// SQLite file and often the port. The next start then loses the port or, worse,
// two hosts write the same session database.
//
// Reaping is deliberately paranoid, because several tw instances run on one
// machine (the acceptance setup alone runs four, each with its own data
// directory). A candidate is killed only when all of these hold:
//
//  1. its executable name is aiscan / aiscan-full;
//  2. its command line contains this instance's data directory as a *path*
//     segment (not a substring: `<data>/data-ab` is a different instance);
//  3. its PID is neither this process nor any process in this process's
//     family (ancestors and descendants), nor the PID recorded in
//     `local.json` for the live host.
//
// Anything else is left alone, even if it is clearly an aiscan. Setting
// TW_HOST_CLEANUP=dry only logs candidates and kills nothing, which is the
// intended first run on a machine with other instances.

// procInfo is one entry of the platform process table.
type procInfo struct {
	PID            int    `json:"ProcessId"`
	ParentPID      int    `json:"ParentProcessId"`
	Name           string `json:"Name"`
	CommandLine    string `json:"CommandLine"`
	ExecutablePath string `json:"ExecutablePath"`
}

// OrphanSweep reports what one pass saw and did.
type OrphanSweep struct {
	Candidates int
	Reaped     int
	Failed     int
	DryRun     bool
	Details    []string
}

// listProcesses is the enumaration seam: the platform file provides the real
// one, tests inject a fake table.
var listProcesses = listProcessesPlatform

// terminateProcess is the kill seam (pid, tryGraceful). Tests replace it so no
// process is ever killed by a unit test.
var terminateProcess = terminateProcessPlatform

// SweepOrphans finds and terminates leftover hosts started from dataDir.
//
// It runs before this process starts its own host, so a stale listener is gone
// before the port is chosen.
func SweepOrphans(ctx context.Context, dataDir string, logf func(string, ...any)) (OrphanSweep, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	out := OrphanSweep{DryRun: cleanupDryRun()}
	if strings.TrimSpace(dataDir) == "" {
		return out, nil
	}
	procs, err := listProcesses(ctx)
	if err != nil {
		return out, fmt.Errorf("枚举宿主进程：%w", err)
	}
	rec, _, _ := ReadRecord(dataDir)
	targets := orphanCandidates(procs, dataDir, os.Getpid())
	out.Candidates = len(targets)
	for _, p := range targets {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		detail := fmt.Sprintf("pid=%d cmd=%s", p.PID, clipCommand(p.CommandLine, 200))
		if out.DryRun {
			logf("host_event=orphan_candidate pid=%d cmd=%q", p.PID, clipCommand(p.CommandLine, 300))
			out.Details = append(out.Details, "candidate "+detail)
			continue
		}
		// The PID in local.json is the previous process's host. It is a genuine
		// orphan — this process has not started its host yet — but it is the one
		// we know was supervised, so it gets the polite kill first.
		gentle := rec.PID > 0 && p.PID == rec.PID
		// Log before killing: if the kill hangs or the process exits on its own
		// we still know which one we acted on.
		logf("host_event=orphan_reaped pid=%d cmd=%q", p.PID, clipCommand(p.CommandLine, 300))
		if err := terminateProcess(p.PID, gentle); err != nil {
			out.Failed++
			logf("host_event=orphan_kill_failed pid=%d err=%q", p.PID, err.Error())
			continue
		}
		out.Reaped++
		out.Details = append(out.Details, "reaped "+detail)
	}
	return out, nil
}

// cleanupDryRun reports the TW_HOST_CLEANUP=dry switch.
func cleanupDryRun() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("TW_HOST_CLEANUP")), "dry")
}

// orphanCandidates filters a process table down to this instance's leftovers.
func orphanCandidates(procs []procInfo, dataDir string, selfPID int) []procInfo {
	protected := familyPIDs(procs, selfPID)
	var out []procInfo
	for _, p := range procs {
		if p.PID <= 0 || protected[p.PID] {
			continue
		}
		if !isHarnessName(p.Name) && !isHarnessName(filepath.Base(p.ExecutablePath)) {
			continue
		}
		if !commandBelongsTo(p.CommandLine, dataDir) {
			continue
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out
}

// familyPIDs returns this process plus every ancestor and descendant reachable
// through ParentPID. The whole family is protected: killing a host that a
// sibling instance launched from the same data directory would be caught by the
// data-directory test, but protecting the family also covers a host this
// process is currently starting.
func familyPIDs(procs []procInfo, selfPID int) map[int]bool {
	if selfPID <= 0 {
		return map[int]bool{}
	}
	parent := make(map[int]int, len(procs))
	children := make(map[int][]int, len(procs))
	for _, p := range procs {
		parent[p.PID] = p.ParentPID
		if p.ParentPID != 0 {
			children[p.ParentPID] = append(children[p.ParentPID], p.PID)
		}
	}
	protected := map[int]bool{selfPID: true}
	// Ancestors: bounded walk; a cycle in a corrupt table must not hang us.
	for pid, depth := selfPID, 0; depth < 64; depth++ {
		pp := parent[pid]
		if pp <= 0 || protected[pp] {
			break
		}
		protected[pp] = true
		pid = pp
	}
	// Descendants.
	queue := []int{selfPID}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		for _, child := range children[pid] {
			if protected[child] {
				continue
			}
			protected[child] = true
			queue = append(queue, child)
		}
	}
	return protected
}

// isHarnessName reports whether an executable base name is a harness host.
func isHarnessName(name string) bool {
	base := strings.ToLower(strings.TrimSpace(filepath.Base(strings.ReplaceAll(name, `\`, `/`))))
	base = strings.TrimSuffix(base, ".exe")
	switch base {
	case "aiscan", "aiscan-full":
		return true
	}
	return false
}

// commandBelongsTo reports whether the command line names this instance's data
// directory as a complete path segment.
func commandBelongsTo(commandLine, dataDir string) bool {
	needle := normalizeProcPath(dataDir)
	if needle == "" || needle == "." {
		return false
	}
	hay := normalizeProcPath(commandLine)
	offset := 0
	for {
		i := strings.Index(hay[offset:], needle)
		if i < 0 {
			return false
		}
		start := offset + i
		end := start + len(needle)
		if (start == 0 || isPathBoundary(hay[start-1])) &&
			(end == len(hay) || isPathBoundary(hay[end])) {
			return true
		}
		offset = start + 1
	}
}

// normalizeProcPath folds separators and case so a Windows command line and a
// filepath.Abs value compare equal. It does not resolve symlinks: both sides
// come from the same machine's conventions.
func normalizeProcPath(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = filepath.Clean(s)
	s = strings.ReplaceAll(s, "/", string(filepath.Separator))
	return strings.ToLower(s)
}

// isPathBoundary lists the characters that end a path inside a command line:
// separators, quotes, flags and whitespace.
func isPathBoundary(b byte) bool {
	switch b {
	case '\\', '/', '"', '\'', '=', ' ', '\t', '\r', '\n', ';', ',':
		return true
	}
	return false
}

// clipCommand keeps one log line bounded.
func clipCommand(s string, n int) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

// parseProcessJSON decodes the JSON produced by the Windows enumeration (a
// single object or an array, depending on how many processes matched).
func parseProcessJSON(data []byte) ([]procInfo, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	if strings.HasPrefix(trimmed, "{") {
		var one procInfo
		if err := json.Unmarshal([]byte(trimmed), &one); err != nil {
			return nil, err
		}
		return []procInfo{one}, nil
	}
	var many []procInfo
	if err := json.Unmarshal([]byte(trimmed), &many); err != nil {
		return nil, err
	}
	return many, nil
}
