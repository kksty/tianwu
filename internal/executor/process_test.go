package executor

import (
	"errors"
	"strings"
	"testing"
)

// A cancellation closes our read end of the child's stdout. When the child
// happens to exit cleanly at that moment, Wait returns nil, and the old
// attribution called Error() on that nil waitErr — a panic that takes the whole
// control plane down. The stream was cut, so the run is a failure with a reason,
// never a success.
func TestTruncatedStreamIsNotSuccessAndDoesNotPanic(t *testing.T) {
	res := finishResult(Result{}, nil, "", true)
	if res.ExitCode != -1 {
		t.Fatalf("exit code = %d, want -1 for a truncated stream", res.ExitCode)
	}
	if !strings.Contains(res.Output, "读取 harness 事件流失败") {
		t.Fatalf("output does not explain the truncated stream: %q", res.Output)
	}
}

// The child's stderr explains why the stream died, but it must not erase the
// fact that the event stream itself was truncated.
func TestStderrExplainsATruncatedStream(t *testing.T) {
	res := finishResult(Result{}, nil, "harness: database is locked", true)
	if res.ExitCode != -1 {
		t.Fatalf("exit code = %d, want -1", res.ExitCode)
	}
	if !strings.Contains(res.Output, "读取 harness 事件流失败") ||
		!strings.Contains(res.Output, "database is locked") {
		t.Fatalf("output = %q, want the read failure and stderr", res.Output)
	}
}

// A clean exit keeps the child's stderr as the run's message (it is a warning,
// not a failure) and a failed exit with no stderr falls back to the exit status.
func TestFinishResultKeepsStderrAndExitStatus(t *testing.T) {
	clean := finishResult(Result{}, nil, "host warning: no working dir", false)
	if clean.ExitCode != 0 || clean.Output != "host warning: no working dir" {
		t.Fatalf("clean exit = %+v", clean)
	}

	failed := finishResult(Result{}, errors.New("exit status 3"), "", false)
	if failed.ExitCode != -1 || failed.Output != "exit status 3" {
		t.Fatalf("failed exit = %+v", failed)
	}
}
