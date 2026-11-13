package host

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kksty/tianwu/internal/aopexec"
)

// A case's AOP session pointer is `<case>/.tw/aop-session.json`; the commander's
// is `<project>/commander/.tw/aop-session.json` (api.Server.CommanderDir). The
// pointer is what "this case can continue its conversation" means, and a
// pointer to a host that is gone makes the next retry resume a session nobody
// holds — the real failure was "会话属于 18767 不是 49850", i.e. a marker from a
// previous process's endpoint.
const (
	staleSuffix        = ".stale"
	staleInvalidSuffix = ".stale.invalid"
	// maxSweepDetails bounds the log detail list; the counts stay complete.
	maxSweepDetails = 20
)

// Swept is the outcome of one marker sweep. Cleaned markers are renamed, never
// deleted: the file is evidence of which session the case used.
type Swept struct {
	Cleaned int
	Kept    int
	Failed  int
	Details []string
}

// SweepStaleSessions renames AOP session markers that point at a previous local
// host. It only touches loopback markers: a remote node's session belongs to
// that node and its own router (docs/14-nodes.md).
//
// This is the recovery path for a restart with a different endpoint, and it is
// deliberately not part of Server.Recover: Recover settles run rows, while this
// scans the case tree once at startup (and once per endpoint change). Doing it
// in Recover would rescan the whole tree on every project attach.
func SweepStaleSessions(ctx context.Context, dataDir, currentEndpoint string, logf func(string, ...any)) (Swept, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	var out Swept
	if strings.TrimSpace(dataDir) == "" {
		return out, nil
	}
	currentEndpoint = strings.TrimRight(strings.TrimSpace(currentEndpoint), "/")
	patterns := []string{
		filepath.Join(dataDir, "projects", "*", "cases", "*", ".tw", "aop-session.json"),
		filepath.Join(dataDir, "projects", "*", "commander", ".tw", "aop-session.json"),
	}
	for _, pattern := range patterns {
		paths, err := filepath.Glob(pattern)
		if err != nil {
			// A malformed pattern is a programming error, not a data problem.
			return out, fmt.Errorf("扫描会话指针：%w", err)
		}
		for _, path := range paths {
			if err := ctx.Err(); err != nil {
				return out, err
			}
			sweepMarker(path, currentEndpoint, &out, logf)
		}
	}
	return out, nil
}

func sweepMarker(path, currentEndpoint string, out *Swept, logf func(string, ...any)) {
	// The case directory is two levels up: <case>/.tw/aop-session.json.
	workDir := filepath.Dir(filepath.Dir(path))
	info, ok := aopexec.ReadSession(workDir)
	if !ok {
		// Unparseable or empty session: it cannot be resumed, so keeping it only
		// guarantees one failed resume later.
		renameStale(path, staleInvalidSuffix, "invalid", out, logf)
		return
	}
	if !IsLoopbackEndpoint(info.URL) {
		out.Kept++
		return
	}
	if sameEndpoint(info.URL, currentEndpoint) {
		// The live local host owns this URL. Only an agent mismatch is stale
		// (e.g. the host was rebuilt with a different agent id).
		if info.Agent == "" || info.Agent == LocalAgent {
			out.Kept++
			return
		}
		renameStale(path, staleSuffix, "agent "+info.Agent+" != "+LocalAgent, out, logf)
		return
	}
	renameStale(path, staleSuffix, "endpoint "+info.URL+" != "+currentEndpoint, out, logf)
}

// sameEndpoint compares two host URLs, ignoring a trailing slash and path case
// on Windows-style loopback hosts.
func sameEndpoint(a, b string) bool {
	a = strings.TrimRight(strings.TrimSpace(a), "/")
	b = strings.TrimRight(strings.TrimSpace(b), "/")
	if a == "" || b == "" {
		return false
	}
	return strings.EqualFold(a, b)
}

func renameStale(path, suffix, why string, out *Swept, logf func(string, ...any)) {
	target := path + suffix
	// Windows rename refuses to replace; a previous sweep's leftover is not
	// worth keeping over the newer evidence.
	_ = os.Remove(target)
	if err := os.Rename(path, target); err != nil {
		out.Failed++
		logf("host_event=session_marker_stale_failed path=%s err=%q", path, err.Error())
		return
	}
	out.Cleaned++
	if len(out.Details) < maxSweepDetails {
		out.Details = append(out.Details, fmt.Sprintf("%s -> %s（%s）", path, filepath.Base(target), why))
	}
	logf("host_event=session_marker_stale path=%s reason=%q", path, why)
}
