// Pool runs one local harness host per selected model profile.
//
// The harness resolves a provider/model/endpoint/key once per process (and
// treats the other llm.providers entries as fallbacks), while a Tianwu instance
// must run two intents on two different APIs at the same time. A per-profile
// host is the smallest way to get that isolation without patching the harness:
// each child reads a generated configuration that contains exactly one
// provider, so it cannot borrow another profile's endpoint, key or model.
//
// Hosts start lazily on the first run that selects the profile and are reused
// afterwards. A profile whose binding fields change gets a new revision, and
// therefore a new host and a new session database; the old process is kept
// until shutdown so runs that are still using it are not killed mid-turn.
package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kksty/tianwu/internal/aopexec"
	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/settings"
)

// Runner is the part of a started host the pool needs. It exists so tests can
// exercise revision reuse and retirement without launching a real binary.
type Runner interface {
	Endpoint() string
	Token() string
	Agent() string
	// PID is the host process, or 0 for a test runner. The orphan sweep uses it
	// to spare hosts this process is still supervising.
	PID() int
	Stop()
}

// LaunchFunc starts one host. It defaults to Start.
type LaunchFunc func(ctx context.Context, cfg Config) (Runner, error)

// PoolConfig is the instance-level part of every profile host: the binary, the
// data directory (the agent's working directory must contain the case dirs),
// and the settings store that owns the profile list.
type PoolConfig struct {
	Bin      string
	Dir      string
	Settings *settings.Store
	// Timeout bounds one run's turn, as on the node router.
	Timeout time.Duration
	// ExcludeTools is passed to every profile host as CYBER_EXCLUDE_TOOLS.
	// Empty leaves the harness's tool surface untouched.
	ExcludeTools string
	Logf         func(format string, args ...any)
	// OnReady observes a newly published host; it must not launch another host.
	OnReady func(profile string, runner Runner)
	// Launch is a test seam. Nil uses Start.
	Launch LaunchFunc
}

// Pool owns the profile hosts of this process.
type Pool struct {
	cfg PoolConfig

	mu      sync.Mutex
	current map[string]*poolHost
	// retired hosts are kept for runs that started before a binding edit. A
	// message to such a run must still find its endpoint/token here.
	retired []*poolHost
	locks   map[string]*sync.Mutex
	closed  bool
}

type poolHost struct {
	profile  string
	runner   Runner
	revision string
}

// NewPool creates an empty pool. Hosts start on demand.
func NewPool(cfg PoolConfig) *Pool {
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	return &Pool{
		cfg:     cfg,
		current: map[string]*poolHost{},
		locks:   map[string]*sync.Mutex{},
	}
}

// Executor resolves a saved profile to an AOP executor backed by that profile's
// host, starting the host on first use.
func (p *Pool) Executor(profile string) (executor.Executor, error) {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		return nil, errors.New("模型配置 id 为空")
	}
	if p.cfg.Settings == nil {
		return nil, errors.New("这个实例没有模型配置目录")
	}
	lock := p.profileLock(profile)
	lock.Lock()
	defer lock.Unlock()
	return p.executorLocked(profile)
}

// executorLocked is Executor without the per-profile lock. Re-reading the
// profile here (instead of trusting the caller's copy) is what lets Restart
// pick up a rotated key: the revision does not change, so the caller would
// otherwise hand back the old host.
func (p *Pool) executorLocked(profile string) (executor.Executor, error) {
	saved, err := p.cfg.Settings.SavedProfile(profile)
	if err != nil {
		return nil, err
	}
	if saved.Paused {
		return nil, fmt.Errorf("模型配置 %s 已暂停", profile)
	}
	if !saved.Runnable() {
		return nil, fmt.Errorf("模型配置 %s 还不能执行：base_url、model、api_key 必须齐全", profile)
	}
	revision := saved.Revision()

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, errors.New("实例正在关闭：不能再启动模型宿主")
	}
	current := p.current[profile]
	p.mu.Unlock()
	if current != nil && current.revision == revision {
		return p.executorFor(current.runner), nil
	}

	dir := filepath.Join(p.cfg.Dir, "host", "profiles")
	stem := profileStem(profile) + "-" + revision
	configPath := filepath.Join(dir, stem+".yaml")
	dbPath := filepath.Join(dir, stem+".db")
	if err := p.cfg.Settings.WriteProfileConfig(profile, configPath); err != nil {
		return nil, fmt.Errorf("写模型配置 %s 的宿主配置：%w", profile, err)
	}
	args := []string{"--config", configPath}
	if saved.ContextWindow > 0 {
		args = append(args, "--context-window", fmt.Sprint(saved.ContextWindow))
	}
	if saved.MaxTokens > 0 {
		args = append(args, "--max-tokens", fmt.Sprint(saved.MaxTokens))
	}
	// The environment wins over the generated config file inside the harness
	// (CYBER_* beats llm.providers), so the key comes from ProfileEnv: it applies
	// the rotation cursor's selected key exactly like WriteProfileConfig does. A
	// rotated key would otherwise be silently overridden by the primary api_key.
	env, err := p.cfg.Settings.ProfileEnv(profile)
	if err != nil {
		return nil, fmt.Errorf("解析模型配置 %s 的环境变量：%w", profile, err)
	}
	runner, err := p.launch(context.Background(), Config{
		Bin:          p.cfg.Bin,
		Dir:          p.cfg.Dir,
		DB:           dbPath,
		Env:          env,
		Args:         args,
		ExcludeTools: p.cfg.ExcludeTools,
		// A profile host is a worker host: it never needs the operator's login
		// token, and every session in it inherits the process environment. Drop
		// a leaked one even though tianwu does not put it there (D109).
		DropEnvPrefixes: []string{"CYBER_", "TW_CONSOLE_TOKEN"},
		// A profile is only launched when it is runnable, so a profile host
		// with no bound provider is a failed start, not a usable host.
		RequireProvider: true,
		Logf:            p.cfg.Logf,
	})
	if err != nil {
		return nil, fmt.Errorf("启动模型配置 %s 的宿主：%w", profile, err)
	}
	p.mu.Lock()
	if p.closed {
		// Stop ran while this host was starting. Do not publish it: the caller
		// is shutting down and nothing would ever stop the new process.
		p.mu.Unlock()
		runner.Stop()
		return nil, errors.New("实例正在关闭：不能再启动模型宿主")
	}
	if old := p.current[profile]; old != nil {
		p.retired = append(p.retired, old)
	}
	p.current[profile] = &poolHost{profile: profile, runner: runner, revision: revision}
	p.mu.Unlock()
	p.cfg.Logf("host: 模型配置 %s 的宿主就绪 %s", profile, runner.Endpoint())
	if p.cfg.OnReady != nil {
		p.cfg.OnReady(profile, runner)
	}
	return p.executorFor(runner), nil
}

// Restart explicitly rebuilds one profile's host so it reads a changed
// credential. Key rotation does not change Revision on purpose (the binding is
// the same), so the revision cache above would keep the old host forever; this
// is the one entry point for "re-materialise this profile's configuration".
//
// The old runner moves to retired, not to the bin: a run that is already
// talking to it must finish (its endpoint/token stay resolvable through
// HostForURL), and only then is it stopped at pool shutdown. A profile that was
// never started is left alone — the next Executor starts it with the new key.
func (p *Pool) Restart(profile string) error {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		return errors.New("模型配置 id 为空")
	}
	if p.cfg.Settings == nil {
		return errors.New("这个实例没有模型配置目录")
	}
	lock := p.profileLock(profile)
	lock.Lock()
	defer lock.Unlock()

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return errors.New("实例正在关闭：不能重启模型宿主")
	}
	old := p.current[profile]
	if old == nil {
		p.mu.Unlock()
		p.cfg.Logf("host: 模型配置 %s 还没有宿主，轮换留到首次使用时生效", profile)
		return nil
	}
	delete(p.current, profile)
	p.retired = append(p.retired, old)
	p.mu.Unlock()

	if _, err := p.executorLocked(profile); err != nil {
		return fmt.Errorf("重启模型配置 %s 的宿主：%w", profile, err)
	}
	p.cfg.Logf("host: 模型配置 %s 的宿主已重建（凭据轮换）", profile)
	return nil
}

// PIDs is every host process this pool supervises (current and retired). The
// orphan sweep must never kill one of these.
func (p *Pool) PIDs() []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]int, 0, len(p.current)+len(p.retired))
	for _, h := range p.current {
		if pid := h.runner.PID(); pid > 0 {
			out = append(out, pid)
		}
	}
	for _, h := range p.retired {
		if pid := h.runner.PID(); pid > 0 {
			out = append(out, pid)
		}
	}
	return out
}

// Host returns the live endpoint/token/agent of a profile host, starting it if
// this is the first use. It is what the say/message path resolves so a message
// goes to the host that actually holds the run's session.
func (p *Pool) Host(profile string) (endpoint, token, agent string, err error) {
	ex, err := p.Executor(profile)
	if err != nil {
		return "", "", "", err
	}
	ae, ok := ex.(*aopexec.Executor)
	if !ok {
		return "", "", "", errors.New("模型配置宿主不是 AOP executor")
	}
	return ae.BaseURL, ae.Token, ae.Agent, nil
}

// HostForURL finds the host that owns an endpoint, including a retired
// revision that is still serving a run. A message addressed to that run must use
// the old host's token, not the current revision's.
func (p *Pool) HostForURL(profile, endpoint string) (base, token, agent string, ok bool) {
	profile = strings.TrimSpace(profile)
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if profile == "" || endpoint == "" {
		return "", "", "", false
	}
	match := func(h *poolHost) bool {
		return h != nil && h.profile == profile &&
			strings.EqualFold(strings.TrimRight(h.runner.Endpoint(), "/"), endpoint)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if match(p.current[profile]) {
		h := p.current[profile]
		return h.runner.Endpoint(), h.runner.Token(), h.runner.Agent(), true
	}
	for _, h := range p.retired {
		if match(h) {
			return h.runner.Endpoint(), h.runner.Token(), h.runner.Agent(), true
		}
	}
	return "", "", "", false
}

// Stop stops every host the pool started, newest first. It is safe to call more
// than once.
func (p *Pool) Stop() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	runners := make([]Runner, 0, len(p.current)+len(p.retired))
	for _, h := range p.current {
		runners = append(runners, h.runner)
	}
	for _, h := range p.retired {
		runners = append(runners, h.runner)
	}
	p.current = map[string]*poolHost{}
	p.retired = nil
	p.mu.Unlock()
	for i := len(runners) - 1; i >= 0; i-- {
		runners[i].Stop()
	}
}

func (p *Pool) executorFor(runner Runner) *aopexec.Executor {
	return &aopexec.Executor{
		BaseURL: runner.Endpoint(), Token: runner.Token(), Agent: runner.Agent(),
		Timeout: p.cfg.Timeout,
	}
}

func (p *Pool) launch(ctx context.Context, cfg Config) (Runner, error) {
	if p.cfg.Launch != nil {
		return p.cfg.Launch(ctx, cfg)
	}
	return Start(ctx, cfg)
}

func (p *Pool) profileLock(profile string) *sync.Mutex {
	p.mu.Lock()
	defer p.mu.Unlock()
	lock := p.locks[profile]
	if lock == nil {
		lock = &sync.Mutex{}
		p.locks[profile] = lock
	}
	return lock
}

// profileStem turns an arbitrary profile id into a portable file name. The id
// itself may contain Chinese characters or dots; the digest keeps the path safe
// and still recognisable in a log line.
func profileStem(profile string) string {
	sum := sha256.Sum256([]byte(profile))
	return hex.EncodeToString(sum[:])[:12]
}
