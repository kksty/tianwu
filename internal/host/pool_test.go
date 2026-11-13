package host

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/settings"
)

const poolProfiles = `llm:
    active_profile: fast
    providers:
        - id: fast
          provider: openai
          base_url: http://fast:8080
          api_key: fast-key
          model: fast-model
        - id: slow
          provider: anthropic
          base_url: http://slow:8080
          api_key: slow-key
          model: slow-model
`

type fakeRunner struct {
	endpoint string
	token    string
	stopped  *int32
	pid      int
}

func TestPoolNotifiesOnlyNewlyMaterializedHosts(t *testing.T) {
	f := newPoolFixture(t)
	calls := 0
	f.pool.cfg.OnReady = func(profile string, runner Runner) {
		calls++
		if _, _, _, ok := f.pool.HostForURL(profile, runner.Endpoint()); !ok {
			t.Error("ready callback preceded host publication")
		}
	}
	if _, err := f.pool.Executor("fast"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Executor("fast"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("ready notifications=%d", calls)
	}
}

func (f *fakeRunner) Endpoint() string { return f.endpoint }
func (f *fakeRunner) Token() string    { return f.token }
func (f *fakeRunner) Agent() string    { return "local" }
func (f *fakeRunner) PID() int         { return f.pid }
func (f *fakeRunner) Stop()            { atomic.AddInt32(f.stopped, 1) }

type poolFixture struct {
	pool    *Pool
	dir     string
	store   *settings.Store
	mu      sync.Mutex
	configs []Config
	launch  int32
	stopped int32
}

func newPoolFixture(t *testing.T) *poolFixture {
	t.Helper()
	dir := t.TempDir()
	harness := filepath.Join(dir, "cyber.yaml")
	if err := os.WriteFile(harness, []byte(poolProfiles), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := settings.LoadAt(filepath.Join(dir, "settings.json"), harness)
	if err != nil {
		t.Fatal(err)
	}
	f := &poolFixture{dir: dir, store: store}
	n := int32(0)
	f.pool = NewPool(PoolConfig{
		Bin:      "aiscan-full",
		Dir:      dir,
		Settings: store,
		Timeout:  time.Minute,
		Logf:     t.Logf,
		Launch: func(_ context.Context, cfg Config) (Runner, error) {
			atomic.AddInt32(&f.launch, 1)
			f.mu.Lock()
			f.configs = append(f.configs, cfg)
			id := n
			n++
			f.mu.Unlock()
			return &fakeRunner{endpoint: fmt.Sprintf("http://127.0.0.1:9%03d", id), token: "tok", stopped: &f.stopped, pid: 1000 + int(id)}, nil
		},
	})
	t.Cleanup(f.pool.Stop)
	return f
}

func (f *poolFixture) lastConfig(t *testing.T) Config {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.configs) == 0 {
		t.Fatal("no host launched")
	}
	return f.configs[len(f.configs)-1]
}

func TestPoolReusesHostPerProfile(t *testing.T) {
	f := newPoolFixture(t)
	first, err := f.pool.Executor("slow")
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.pool.Executor("slow")
	if err != nil {
		t.Fatal(err)
	}
	if first.Name() != second.Name() || atomic.LoadInt32(&f.launch) != 1 {
		t.Fatalf("launches = %d, executors %q / %q", f.launch, first.Name(), second.Name())
	}
	cfg := f.lastConfig(t)
	if cfg.DB == "" || !strings.Contains(cfg.DB, "-") || !strings.HasSuffix(cfg.DB, ".db") {
		t.Fatalf("db = %q", cfg.DB)
	}
	if strings.Join(cfg.Env, " ") == "" || cfg.Env[0] == "" {
		t.Fatalf("env = %v", cfg.Env)
	}
	if !containsPrefix(cfg.DropEnvPrefixes, "CYBER_") {
		t.Fatalf("inherited CYBER_* not dropped: %v", cfg.DropEnvPrefixes)
	}
	if !hasArg(cfg.Args, "--config") {
		t.Fatalf("args = %v", cfg.Args)
	}
}

func TestPausedProfileRejectsNewUseWithoutStoppingExistingHost(t *testing.T) {
	f := newPoolFixture(t)
	if _, err := f.pool.Executor("fast"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.SetProfilePaused("fast", true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Executor("fast"); err == nil || !strings.Contains(err.Error(), "已暂停") {
		t.Fatalf("paused host was reused: %v", err)
	}
	if atomic.LoadInt32(&f.stopped) != 0 || atomic.LoadInt32(&f.launch) != 1 {
		t.Fatal("pausing stopped or replaced an existing host")
	}
	if _, err := f.store.SetProfilePaused("fast", false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Executor("fast"); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&f.launch) != 1 {
		t.Fatal("resume replaced the unchanged host")
	}
}

func TestPoolStartsDifferentHostsForDifferentProfiles(t *testing.T) {
	f := newPoolFixture(t)
	fast, err := f.pool.Executor("fast")
	if err != nil {
		t.Fatal(err)
	}
	slow, err := f.pool.Executor("slow")
	if err != nil {
		t.Fatal(err)
	}
	if fast.Name() == slow.Name() || atomic.LoadInt32(&f.launch) != 2 {
		t.Fatalf("launches = %d, %q / %q", f.launch, fast.Name(), slow.Name())
	}
	f.mu.Lock()
	configs := append([]Config(nil), f.configs...)
	f.mu.Unlock()
	if configs[0].DB == configs[1].DB {
		t.Fatal("profile hosts share one session database")
	}
	if !strings.Contains(strings.Join(configs[0].Env, " "), "fast-model") ||
		!strings.Contains(strings.Join(configs[1].Env, " "), "slow-model") {
		t.Fatalf("env crossed profiles: %v / %v", configs[0].Env, configs[1].Env)
	}
}

// A binding edit creates a new revision, so the next run gets a new host and a
// new database; the old host stays alive until shutdown.
func TestPoolStartsNewRevisionAndRetiresOldHost(t *testing.T) {
	f := newPoolFixture(t)
	if _, err := f.pool.Executor("slow"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.SaveProfile(settings.Profile{
		ID: "slow", Provider: "anthropic", BaseURL: "http://slow:8080", Model: "slow-v2",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Executor("slow"); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&f.launch); got != 2 {
		t.Fatalf("launches = %d", got)
	}
	f.mu.Lock()
	configs := append([]Config(nil), f.configs...)
	f.mu.Unlock()
	if configs[0].DB == configs[1].DB {
		t.Fatal("new revision reused the old session database")
	}
	if atomic.LoadInt32(&f.stopped) != 0 {
		t.Fatal("old host stopped before shutdown")
	}
	f.pool.Stop()
	if atomic.LoadInt32(&f.stopped) != 2 {
		t.Fatalf("stopped = %d", f.stopped)
	}
}

// A run that started before a binding edit keeps its session on the old host;
// message delivery must resolve that host's token, not the current revision's.
func TestPoolHostForURLFindsRetiredRevision(t *testing.T) {
	f := newPoolFixture(t)
	first, err := f.pool.Executor("slow")
	if err != nil {
		t.Fatal(err)
	}
	firstURL := strings.TrimPrefix(first.Name(), "aop(")
	firstURL = strings.TrimSuffix(firstURL, ")")
	if _, err := f.store.SaveProfile(settings.Profile{
		ID: "slow", Provider: "anthropic", BaseURL: "http://slow:8080", Model: "slow-v2",
	}); err != nil {
		t.Fatal(err)
	}
	second, err := f.pool.Executor("slow")
	if err != nil {
		t.Fatal(err)
	}
	secondURL := strings.TrimPrefix(second.Name(), "aop(")
	secondURL = strings.TrimSuffix(secondURL, ")")
	if firstURL == secondURL {
		t.Fatal("revision reuse did not start a new host")
	}
	if base, _, _, ok := f.pool.HostForURL("slow", firstURL); !ok || base != firstURL {
		t.Fatalf("old endpoint = %q ok=%v", base, ok)
	}
	if base, _, _, ok := f.pool.HostForURL("slow", secondURL); !ok || base != secondURL {
		t.Fatalf("new endpoint = %q ok=%v", base, ok)
	}
	if _, _, _, ok := f.pool.HostForURL("fast", secondURL); ok {
		t.Fatal("endpoint resolved under the wrong profile")
	}
}

func TestPoolSerializesConcurrentStarts(t *testing.T) {
	f := newPoolFixture(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.pool.Executor("fast"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := atomic.LoadInt32(&f.launch); got != 1 {
		t.Fatalf("launches = %d", got)
	}
}

// Stop can land while a host is still becoming ready; the new process must not
// be published after the pool is closed or nothing would ever stop it.
func TestPoolStopDuringStartStopsNewHost(t *testing.T) {
	f := newPoolFixture(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var stopped int32
	f.pool.cfg.Launch = func(context.Context, Config) (Runner, error) {
		close(started)
		<-release
		return &fakeRunner{endpoint: "http://127.0.0.1:1", token: "t", stopped: &stopped}, nil
	}
	done := make(chan error, 1)
	go func() {
		_, err := f.pool.Executor("fast")
		done <- err
	}()
	<-started
	f.pool.Stop()
	close(release)
	if err := <-done; err == nil || !strings.Contains(err.Error(), "正在关闭") {
		t.Fatalf("err = %v", err)
	}
	if atomic.LoadInt32(&stopped) != 1 {
		t.Fatalf("new host stopped = %d, want 1", stopped)
	}
}

func TestPoolRefusesIncompleteProfile(t *testing.T) {
	f := newPoolFixture(t)
	harness := filepath.Join(f.dir, "cyber.yaml")
	body := strings.Replace(poolProfiles, "          api_key: slow-key\n", "", 1)
	if err := os.WriteFile(harness, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Executor("slow"); err == nil || !strings.Contains(err.Error(), "api_key") {
		t.Fatalf("err = %v", err)
	}
	if atomic.LoadInt32(&f.launch) != 0 {
		t.Fatal("launched an incomplete profile")
	}
}

func TestPoolRefusesUnknownProfile(t *testing.T) {
	f := newPoolFixture(t)
	if _, err := f.pool.Executor("missing"); err == nil {
		t.Fatal("want error")
	}
}

func containsPrefix(values []string, prefix string) bool {
	for _, v := range values {
		if v == prefix {
			return true
		}
	}
	return false
}

// Key rotation does not change a profile's Revision, so the revision cache
// would hand back the old host forever. Restart is the explicit "re-materialise
// this profile's configuration" path used by the watchdog.
func TestPoolRestartRebuildsHost(t *testing.T) {
	f := newPoolFixture(t)
	first, err := f.pool.Executor("slow")
	if err != nil {
		t.Fatal(err)
	}
	firstURL := first.Name()
	if err := f.pool.Restart("slow"); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if got := atomic.LoadInt32(&f.launch); got != 2 {
		t.Fatalf("launches = %d, want 2", got)
	}
	second, err := f.pool.Executor("slow")
	if err != nil {
		t.Fatal(err)
	}
	if second.Name() == firstURL {
		t.Fatal("restart reused the old host")
	}
	// The old host stays resolvable for runs that were already talking to it.
	if base, _, _, ok := f.pool.HostForURL("slow", strings.TrimSuffix(strings.TrimPrefix(firstURL, "aop("), ")")); !ok || base == "" {
		t.Fatalf("retired host no longer resolvable: %q", base)
	}
	// PIDs covers current + retired: the orphan sweep must spare both.
	if got := len(f.pool.PIDs()); got != 2 {
		t.Fatalf("PIDs = %v, want current+retired", f.pool.PIDs())
	}
	if atomic.LoadInt32(&f.stopped) != 0 {
		t.Fatal("restart stopped the old host early; a running run would lose its session")
	}
	// The new host's config was written again from the current settings: that is
	// what carries a rotated key to the harness.
	f.mu.Lock()
	configs := append([]Config(nil), f.configs...)
	f.mu.Unlock()
	if len(configs) != 2 || configs[1].DB != configs[0].DB {
		t.Fatalf("restart did not reuse the same session database: %+v", configs)
	}
}

// Restarting a profile that never started is a no-op: the next use starts it
// with the new key anyway.
func TestPoolRestartUnstartedProfileIsNoop(t *testing.T) {
	f := newPoolFixture(t)
	if err := f.pool.Restart("fast"); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if got := atomic.LoadInt32(&f.launch); got != 0 {
		t.Fatalf("launches = %d, want 0", got)
	}
}

func TestPoolRestartAfterCloseFails(t *testing.T) {
	f := newPoolFixture(t)
	if _, err := f.pool.Executor("slow"); err != nil {
		t.Fatal(err)
	}
	f.pool.Stop()
	if err := f.pool.Restart("slow"); err == nil {
		t.Fatal("Restart succeeded on a closed pool")
	}
}

func hasArg(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}
