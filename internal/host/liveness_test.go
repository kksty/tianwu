package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/aop"
)

// The liveness tests drive the supervisor through the two package seams:
// launchProcess (no real binary) and dialHost (no real HTTP). Everything else —
// Start's retry loop, watch's crash/restart path, the structured log lines — is
// the production code.

type testChild struct {
	pid  int
	done chan struct{}
	err  error
	once sync.Once
}

func (c *testChild) exit(err error) {
	c.once.Do(func() {
		c.err = err
		close(c.done)
	})
}

func (c *testChild) process() *process {
	return &process{
		pid:  c.pid,
		wait: func() error { <-c.done; return c.err },
		kill: func() { c.exit(errors.New("killed")) },
	}
}

type testLauncher struct {
	mu       sync.Mutex
	launches int
	children []*testChild
	failNext bool
}

func (l *testLauncher) launch() (*process, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.launches++
	if l.failNext {
		return nil, errors.New("启动失败")
	}
	c := &testChild{pid: 9000 + l.launches, done: make(chan struct{})}
	l.children = append(l.children, c)
	return c.process(), nil
}

func (l *testLauncher) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.launches
}

func (l *testLauncher) childAt(i int) *testChild {
	l.mu.Lock()
	defer l.mu.Unlock()
	if i < 0 || i >= len(l.children) {
		return nil
	}
	return l.children[i]
}

type testAgents struct {
	mu     sync.Mutex
	calls  int
	decide func(call int) ([]aop.Agent, error)
}

func (d *testAgents) Agents(context.Context) ([]aop.Agent, error) {
	d.mu.Lock()
	d.calls++
	n := d.calls
	decide := d.decide
	d.mu.Unlock()
	if decide == nil {
		return nil, nil
	}
	return decide(n)
}

func (d *testAgents) callCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

type testLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *testLog) Logf(format string, args ...any) {
	l.mu.Lock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
	l.mu.Unlock()
}

func (l *testLog) has(substr string) bool { return l.countOf(substr) > 0 }

func (l *testLog) countOf(substr string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, line := range l.lines {
		if strings.Contains(line, substr) {
			n++
		}
	}
	return n
}

func (l *testLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func readyAgents(dir string) []aop.Agent {
	return []aop.Agent{{ID: LocalAgent, Name: "local", WorkingDir: dir}}
}

// installSeams swaps the package seams for one test.
func installSeams(t *testing.T, launcher *testLauncher, agents *testAgents) {
	t.Helper()
	oldLaunch, oldDial := launchProcess, dialHost
	launchProcess = func(*Host) (*process, error) { return launcher.launch() }
	dialHost = func(string, string) agentLister { return agents }
	t.Cleanup(func() {
		launchProcess, dialHost = oldLaunch, oldDial
	})
}

func hostBin(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return exe
}

func testHostConfig(t *testing.T, dir string, logs *testLog) Config {
	t.Helper()
	return Config{
		Bin:              hostBin(t),
		Dir:              dir,
		ReadyTimeout:     40 * time.Millisecond,
		RetryBackoff:     time.Millisecond,
		MaxReadyFailures: 3,
		Logf:             logs.Logf,
	}
}

func TestReadyRequiresLocalAgentWithWorkingDir(t *testing.T) {
	// Three agents, none called "local" (another instance's host on our port).
	if _, ok := readyAgent([]aop.Agent{
		{ID: "worker-a", WorkingDir: `C:\data`},
		{ID: "worker-b", WorkingDir: `C:\data`},
		{ID: "worker-c", WorkingDir: `C:\data`},
	}, false); ok {
		t.Fatal("ready accepted a host without the local agent")
	}
	// The local agent exists but cannot say where it resolves paths: unusable.
	if _, ok := readyAgent([]aop.Agent{{ID: LocalAgent}}, false); ok {
		t.Fatal("ready accepted a local agent without WorkingDir")
	}
	a, ok := readyAgent(readyAgents(`C:\data`), false)
	if !ok || a.ID != LocalAgent {
		t.Fatalf("ready rejected the local agent: %+v ok=%v", a, ok)
	}
}

func TestReadyRequiresProviderWhenConfigured(t *testing.T) {
	if _, ok := readyAgent(readyAgents(`C:\data`), true); ok {
		t.Fatal("ready accepted an agent with no provider while a model is configured")
	}
	withProvider := []aop.Agent{{ID: LocalAgent, WorkingDir: `C:\data`, Provider: "openai", Model: "m"}}
	if _, ok := readyAgent(withProvider, true); !ok {
		t.Fatal("ready rejected an agent with a bound provider")
	}
	withProvider[0].ConfigError = "API error (402): Insufficient Balance"
	if _, ok := readyAgent(withProvider, true); ok {
		t.Fatal("configured provider with native ConfigError must not be ready")
	}
}

func TestModelFailureKeepsHostOnlineAndCanRetryUnchangedSettings(t *testing.T) {
	dir := t.TempDir()
	var launcher testLauncher
	var fixed atomic.Bool
	agents := &testAgents{decide: func(int) ([]aop.Agent, error) {
		a := aop.Agent{ID: LocalAgent, WorkingDir: dir, Provider: "openai", Model: "fixture"}
		if !fixed.Load() {
			a.ConfigError = "API error (402): Insufficient Balance secret-fixture"
		}
		return []aop.Agent{a}, nil
	}}
	installSeams(t, &launcher, agents)
	cfg := testHostConfig(t, dir, &testLog{})
	cfg.AllowNotReady, cfg.RequireProvider = true, true
	cfg.Env = []string{"CYBER_API_KEY=secret-fixture"}
	h, err := Start(context.Background(), cfg)
	if h == nil || !errors.Is(err, ErrNotReady) {
		t.Fatalf("Start = %v, host=%v", err, h)
	}
	defer h.Stop()
	if st := h.Status(); st.Ready || !st.Online || st.PID == 0 || st.Unhealthy || strings.Contains(st.LastError, "secret-fixture") {
		t.Fatalf("model failure status: %+v", st)
	}
	select {
	case <-launcher.childAt(0).done:
		t.Fatal("model failure killed the host")
	default:
	}
	fixed.Store(true)
	if !h.Update(cfg.Env, cfg.Args) {
		t.Fatal("unchanged save must retry a failed model")
	}
	deadline := time.Now().Add(3 * time.Second)
	for !h.Ready() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if st := h.Status(); !st.Ready || !st.Online || st.LastError != "" {
		t.Fatalf("retry failed: %+v", st)
	}
}

func TestCrashedHostIsNotReadyDuringRestartBackoff(t *testing.T) {
	dir := t.TempDir()
	var launcher testLauncher
	agents := &testAgents{decide: func(int) ([]aop.Agent, error) { return readyAgents(dir), nil }}
	installSeams(t, &launcher, agents)
	cfg := testHostConfig(t, dir, &testLog{})
	cfg.RetryBackoff = 80 * time.Millisecond
	h, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Stop()
	launcher.childAt(0).exit(errors.New("fixture crash"))
	deadline := time.Now().Add(time.Second)
	for h.Status().Restarts == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if st := h.Status(); st.Ready || st.Online || st.PID != 0 || !strings.Contains(st.LastError, "fixture crash") {
		t.Fatalf("crashed host appears ready: %+v", st)
	}
}

// A host that became "ready" but whose agent has a nil provider/model used to
// answer ListAgents and then fail every dispatch in 0.05s. The readiness check
// now verifies the agent is ours and lives in this instance's data directory.
func TestReadyRejectsMismatchedWorkingDir(t *testing.T) {
	dir := t.TempDir()
	var launcher testLauncher
	agents := &testAgents{decide: func(int) ([]aop.Agent, error) {
		return []aop.Agent{{ID: LocalAgent, WorkingDir: t.TempDir()}}, nil
	}}
	installSeams(t, &launcher, agents)
	logs := &testLog{}
	_, err := Start(context.Background(), testHostConfig(t, dir, logs))
	if err == nil {
		t.Fatal("Start accepted a host whose agent works in another directory")
	}
	if !strings.Contains(err.Error(), "工作目录") {
		t.Fatalf("error does not explain the working directory mismatch: %v", err)
	}
	if !logs.has("host_event=not_ready") {
		t.Fatalf("no not_ready log:\n%s", logs)
	}
}

// A model endpoint that never comes up must not take the whole control plane
// down: with AllowNotReady, Start returns the host plus ErrNotReady so the
// console can still start and the operator can fix the provider (D101).
func TestStartReturnsDegradedHostWhenAllowed(t *testing.T) {
	dir := t.TempDir()
	var launcher testLauncher
	agents := &testAgents{decide: func(int) ([]aop.Agent, error) { return nil, nil }}
	installSeams(t, &launcher, agents)
	logs := &testLog{}
	cfg := testHostConfig(t, dir, logs)
	cfg.AllowNotReady = true

	h, err := Start(context.Background(), cfg)
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("Start = %v, want ErrNotReady", err)
	}
	if h == nil {
		t.Fatal("a degraded start must return the host object (Stop/Update rely on it)")
	}
	if h.Ready() {
		t.Fatal("a degraded host must not report ready")
	}
	if !logs.has("host_event=degraded") {
		t.Fatalf("degraded event missing:\n%s", logs)
	}
	// No supervisor ever started; Stop must not wait for one forever.
	done := make(chan struct{})
	go func() { h.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop blocked on a supervisor that never started")
	}
}

// The degraded host is not a dead end: saving a fixed model revives it through
// Update, and the console can keep serving in the meantime (D101).
func TestDegradedHostRevivesAfterASettingsChange(t *testing.T) {
	dir := t.TempDir()
	var launcher testLauncher
	var ready atomic.Bool
	agents := &testAgents{decide: func(int) ([]aop.Agent, error) {
		if ready.Load() {
			return readyAgents(dir), nil
		}
		return nil, nil
	}}
	installSeams(t, &launcher, agents)
	logs := &testLog{}
	cfg := testHostConfig(t, dir, logs)
	cfg.AllowNotReady = true
	h, err := Start(context.Background(), cfg)
	if !errors.Is(err, ErrNotReady) || h == nil {
		t.Fatalf("Start = %v, host nil=%v; want ErrNotReady with a host", err, h == nil)
	}

	ready.Store(true)
	h.Update([]string{"CYBER_MODEL=fixed"}, []string{"--model", "fixed"})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !h.Ready() {
		time.Sleep(10 * time.Millisecond)
	}
	if !h.Ready() {
		t.Fatalf("host did not revive after the model change:\n%s", logs)
	}
	h.Stop()
}

// A settings save reports whether it forced a restart, and every ready observer
// runs on the readiness that ends it. The hub uses both to defer data-plane
// wakes until the restarted host can serve (D111).
func TestUpdateReportsRestartAndEveryReadyObserverRuns(t *testing.T) {
	dir := t.TempDir()
	var launcher testLauncher
	var ready atomic.Bool
	agents := &testAgents{decide: func(int) ([]aop.Agent, error) {
		if ready.Load() {
			return readyAgents(dir), nil
		}
		return nil, nil
	}}
	installSeams(t, &launcher, agents)
	cfg := testHostConfig(t, dir, &testLog{})
	cfg.AllowNotReady = true
	h, err := Start(context.Background(), cfg)
	if !errors.Is(err, ErrNotReady) || h == nil {
		t.Fatalf("Start = %v, host nil=%v; want ErrNotReady with a host", err, h == nil)
	}
	defer h.Stop()

	var first, second atomic.Int32
	h.AddOnReady(func(aop.Agent) { first.Add(1) })
	h.AddOnReady(func(aop.Agent) { second.Add(1) })
	ready.Store(true)
	if restarted := h.Update([]string{"CYBER_MODEL=fixed"}, []string{"--model", "fixed"}); !restarted {
		t.Fatal("Update reported no restart for a changed configuration")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && (first.Load() == 0 || second.Load() == 0) {
		time.Sleep(10 * time.Millisecond)
	}
	if first.Load() != 1 || second.Load() != 1 {
		t.Fatalf("ready observers = %d/%d, want 1/1", first.Load(), second.Load())
	}
	if h.Update([]string{"CYBER_MODEL=fixed"}, []string{"--model", "fixed"}) {
		t.Fatal("Update reported a restart for an unchanged configuration")
	}
}

func TestHostStopsAfterThreeConsecutiveNotReady(t *testing.T) {
	dir := t.TempDir()
	var launcher testLauncher
	agents := &testAgents{decide: func(int) ([]aop.Agent, error) { return nil, nil }}
	installSeams(t, &launcher, agents)
	logs := &testLog{}
	_, err := Start(context.Background(), testHostConfig(t, dir, logs))
	if err == nil {
		t.Fatal("Start reported success without a ready agent")
	}
	if got := launcher.count(); got != 3 {
		t.Fatalf("launch attempts = %d, want 3", got)
	}
	if !logs.has("host_event=unhealthy") {
		t.Fatalf("unhealthy event missing:\n%s", logs)
	}
	if !strings.Contains(logs.String(), "attempts=3") {
		t.Fatalf("unhealthy log has no attempt count:\n%s", logs)
	}
	// Nothing may keep retrying after the verdict.
	time.Sleep(60 * time.Millisecond)
	if got := launcher.count(); got != 3 {
		t.Fatalf("supervisor kept restarting after unhealthy: %d launches", got)
	}
}

func TestHostResetsFailureCountAfterReady(t *testing.T) {
	dir := t.TempDir()
	var launcher testLauncher
	gate := make(chan struct{})
	var gateOnce sync.Once
	openGate := func() { gateOnce.Do(func() { close(gate) }) }
	defer openGate()

	agents := &testAgents{decide: func(call int) ([]aop.Agent, error) {
		switch {
		case call <= 2:
			return nil, nil
		case call == 3:
			return readyAgents(dir), nil
		default:
			select {
			case <-gate:
				return readyAgents(dir), nil
			default:
				return nil, nil
			}
		}
	}}
	installSeams(t, &launcher, agents)
	logs := &testLog{}
	h, err := Start(context.Background(), testHostConfig(t, dir, logs))
	if err != nil {
		t.Fatalf("Start: %v\n%s", err, logs)
	}
	defer h.Stop()
	if st := h.Status(); st.FailedReady != 0 || !st.Ready {
		t.Fatalf("after ready: %+v", st)
	}
	// Kill the live host: the supervisor restarts it, and this restart is not
	// ready on the first attempt.
	crash := launcher.childAt(2)
	if crash == nil {
		t.Fatal("no child to crash")
	}
	crash.exit(errors.New("exit status 1"))
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if st := h.Status(); st.FailedReady > 0 {
			if st.FailedReady != 1 {
				t.Fatalf("failure streak after a ready host = %d, want 1", st.FailedReady)
			}
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if st := h.Status(); st.FailedReady == 0 {
		t.Fatalf("restart failure was never counted: %+v", st)
	}
	openGate()
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		st := h.Status()
		if st.FailedReady == 0 && st.Ready {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if st := h.Status(); st.FailedReady != 0 || !st.Ready {
		t.Fatalf("failure streak did not reset after a ready restart: %+v", st)
	}
	if st := h.Status(); st.Restarts == 0 {
		t.Fatalf("restart was not counted: %+v", st)
	}
}

// An unhealthy host is a visible stop, and a settings edit is the operator's
// way back: the edit resets the failure budget and supervision resumes with the
// new environment (the console's "fix the key" path).
func TestHostUpdateRevivesUnhealthySupervisor(t *testing.T) {
	dir := t.TempDir()
	var launcher testLauncher
	var revived atomic.Bool
	agents := &testAgents{decide: func(call int) ([]aop.Agent, error) {
		if call == 1 || revived.Load() {
			return readyAgents(dir), nil
		}
		return nil, nil
	}}
	installSeams(t, &launcher, agents)
	cfg := testHostConfig(t, dir, &testLog{})
	cfg.MaxReadyFailures = 2
	h, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Stop()
	// Crash the ready host; the restarts fail until the budget is spent.
	if c := launcher.childAt(0); c != nil {
		c.exit(errors.New("exit status 1"))
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !h.Unhealthy() {
		time.Sleep(2 * time.Millisecond)
	}
	if !h.Unhealthy() {
		t.Fatalf("host never became unhealthy: %+v", h.Status())
	}
	launchesAtStop := launcher.count()
	revived.Store(true)
	h.Update([]string{"TW_TEST=1"}, nil)
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		st := h.Status()
		if st.Ready && !st.Unhealthy {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("settings edit did not revive the host (launches %d -> %d): %+v",
		launchesAtStop, launcher.count(), h.Status())
}

func TestHostLogsStructuredReadyAndUnhealthy(t *testing.T) {
	dir := t.TempDir()
	// 1) Never ready: start + not_ready + unhealthy lines.
	var launcher testLauncher
	agents := &testAgents{decide: func(int) ([]aop.Agent, error) { return nil, nil }}
	installSeams(t, &launcher, agents)
	badLogs := &testLog{}
	if _, err := Start(context.Background(), testHostConfig(t, dir, badLogs)); err == nil {
		t.Fatal("Start succeeded without an agent")
	}
	text := badLogs.String()
	for _, want := range []string{"host_event=start", "host_event=not_ready", "host_event=unhealthy", "last_error="} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}

	// 2) Ready: the line carries endpoint, agent and working directory.
	var launcher2 testLauncher
	agents2 := &testAgents{decide: func(int) ([]aop.Agent, error) { return readyAgents(dir), nil }}
	oldLaunch := launchProcess
	oldDial := dialHost
	launchProcess = func(*Host) (*process, error) { return launcher2.launch() }
	dialHost = func(string, string) agentLister { return agents2 }
	defer func() { launchProcess, dialHost = oldLaunch, oldDial }()
	goodLogs := &testLog{}
	h, err := Start(context.Background(), testHostConfig(t, dir, goodLogs))
	if err != nil {
		t.Fatalf("Start: %v\n%s", err, goodLogs)
	}
	defer h.Stop()
	readyLine := ""
	for _, line := range goodLogs.lines {
		if strings.Contains(line, "host_event=ready") {
			readyLine = line
			break
		}
	}
	for _, want := range []string{"host_event=ready", "endpoint=http://", "agent=local", "working_dir=", "ms="} {
		if !strings.Contains(readyLine, want) {
			t.Fatalf("ready line %q missing %q", readyLine, want)
		}
	}
}
