// Command tw is the Tianwu control plane: projects, ledgers, dispatcher,
// deciders, HTTP API and the embedded console, in one process.
//
// Run from source:
//
//	go run ./cmd/tw --addr 127.0.0.1:8080 --data ./data
//
// One instance serves several engagements. Each project lives below
// <data>/projects/<id> and owns its ledger, decider memory and workspaces; the
// tooling below <data> (arsenal, pocs, skills) is shared (docs/02, D33).
//
// The decider is enabled when a model is configured, either by flags or by the
// same CYBER_* variables cyber-harness uses:
//
//	CYBER_BASE_URL=http://host:8080 CYBER_API_KEY=sk-... CYBER_MODEL=qwen3.8-27b
//
// Linux and Windows are both first-class; see docs/04-executor.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kksty/tianwu/internal/aop"
	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/host"
	"github.com/kksty/tianwu/internal/ioa"
	"github.com/kksty/tianwu/internal/kb"
	"github.com/kksty/tianwu/internal/modelclient"
	"github.com/kksty/tianwu/internal/node"
	"github.com/kksty/tianwu/internal/pocs"
	"github.com/kksty/tianwu/internal/project"
	"github.com/kksty/tianwu/internal/settings"
	"github.com/kksty/tianwu/internal/skills"
	"github.com/kksty/tianwu/internal/vulnlib"
)

// buildVersion is stamped by the release build (-ldflags "-X main.buildVersion=…").
// A probe and the console both report it: "which build is running" must be
// answerable without ssh.
var buildVersion = "dev"

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "HTTP listen address")
	dataDir := flag.String("data", "./data", "instance root: projects, tools, templates")
	projectID := flag.String("project", "", "打开哪个项目（默认第一个，建库时自动创建 default）")
	aiscan := flag.String("aiscan", "", "path to aiscan-full: the harness tw runs as the local node host (default: next to tw, then PATH)")
	hostAddr := flag.String("host-addr", "127.0.0.1:18766", "本机 aiscan web 宿主的监听地址（被占用时自动换一个空闲端口）")
	requireHostReady := flag.Bool("require-host-ready", false,
		"本机宿主没就绪就退出；默认降级启动控制台（API/设置可用，不派发），在「设置 → 模型」修好后宿主自动重启")
	allowNativeSubagent := flag.Bool("allow-native-subagent", false, "允许 harness 暴露原生 subagent 工具；默认排除（它创建的会话在账本外，不可取消/不计费，分工应走 spawn/children）")
	timeout := flag.Duration("timeout", 0, "per-run timeout, 0 means none")
	idleTimeout := flag.Duration("idle-timeout", 30*time.Minute, "cancel a run with no events for this long (0 disables)")
	maxEvidence := flag.Int("max-evidence", 200, "每次 run 最多自动登记的证据文件数（交接口声明的证据不受此限）")
	scope := flag.String("scope", "", "authorised scope for new projects, injected into every dispatch brief")

	noDecider := flag.Bool("no-decider", false, "ledger and dispatch only; do not start the deciders")
	modelName := flag.String("model", "", "model name (default CYBER_MODEL)")
	modelURL := flag.String("model-url", "", "OpenAI-compatible endpoint (default CYBER_BASE_URL)")
	modelKey := flag.String("model-key", "", "API key (default CYBER_API_KEY)")
	maxRuns := flag.Int("max-runs", 3, "default max concurrent intent runs per project (0 = uncapped); a project can override it in 设置 → 项目并发")
	maxWorkers := flag.Int("max-workers", 0, "实例级并发上限：所有项目同时在跑的意图 run 数（0=不限）；purpose run 不计")
	debounce := flag.Duration("debounce", 3*time.Second, "wake-up debounce window")
	heartbeat := flag.Duration("heartbeat", 5*time.Minute, "idle heartbeat interval")
	roundTimeout := flag.Duration("round-timeout", 15*time.Minute,
		"指挥官单轮墙钟上限：超时按 canceled 截断，下一次唤醒重新决策（0 = 不限）")
	extraSkills := flag.String("skills", "", "额外的 harness skills，逗号分隔（名字或 SKILL.md 路径）")
	verifyAuto := flag.Bool("verify-auto", false, "每次运行结束后自动复核 vuln/cred 类事实")
	consoleToken := flag.String("console-token", "", "控制台登录令牌（默认读 TW_CONSOLE_TOKEN；都不给就自动生成 <data>/console.token）")
	consoleTokenFile := flag.String("console-token-file", "", "从文件读控制台令牌（避免出现在命令行/进程列表里）")
	harnessConfig := flag.String("harness-config", "", "harness 用户配置（默认 ~/.cyber/cyber.yaml）；控制台「设置」直接读写它")
	showVersion := flag.Bool("version", false, "打印版本后退出")
	flag.Parse()
	if flag.NArg() > 0 {
		log.Fatalf("无法识别的参数：%v（--scope 等含空格的值要加引号，否则后面的 flag 会被静默忽略）", flag.Args())
	}

	if *showVersion {
		fmt.Println("tw", buildVersion)
		return
	}

	data := absPath(*dataDir)
	if err := os.MkdirAll(data, 0o755); err != nil {
		log.Fatalf("data dir: %v", err)
	}
	// Take the console socket before anything with side effects (orphan sweep,
	// host start). A second instance used to reach the sweep first, kill the
	// running instance's local host, then die on the bind; the survivor was left
	// talking to the orphan host, whose token it does not know (HTTP 401 on the
	// AOP handshake). Failing here keeps a mis-launched second instance from
	// damaging a running control plane.
	listener, err := listenConsole(*addr)
	if err != nil {
		log.Fatalf("控制台地址 %s 已被占用（可能已有一个 tw 实例在跑）：%v；先停掉旧实例再启动", *addr, err)
	}

	bin := *aiscan
	if bin != "" {
		// Windows accepts `--aiscan ./bin/aiscan-full` as well as the .exe name.
		bin = executor.NormalizePath(bin)
		// The host child runs with its working directory set to the data dir, so
		// a relative --aiscan would be resolved there and fail to start. Pin an
		// existing file to an absolute path now; a bare name stays for PATH
		// lookup.
		if abs, err := filepath.Abs(bin); err == nil {
			if st, statErr := os.Stat(abs); statErr == nil && !st.IsDir() {
				bin = abs
			}
		}
	} else {
		bin = executor.ResolveBin("aiscan-full")
	}
	if bin != "" {
		log.Printf("harness: %s", bin)
	}
	// Every harness process tianwu starts excludes the native subagent tool by
	// default: a child session it creates has no run, usage, parent or cancel
	// record, which breaks both observability and the "spawn is the only way to
	// make an individual" contract (docs/17 §3.2/§3.11, task-19/33). The flag is
	// the operator's explicit escape hatch; an old harness that does not know
	// CYBER_EXCLUDE_TOOLS ignores it, so this is safe before the fork ships.
	excludeTools := "subagent"
	if *allowNativeSubagent {
		excludeTools = ""
		log.Printf("harness: --allow-native-subagent 已启用：原生 subagent 工具可见（账本外子会话不会被记录）")
	} else {
		log.Printf("harness: 排除原生工具 CYBER_EXCLUDE_TOOLS=%s（--allow-native-subagent 可恢复）", excludeTools)
	}

	// Single-machine platform: the only execution host is the `aiscan web`
	// process this control plane starts itself. <data>/nodes.json may still list
	// local audit binaries (cyber-audit); remote AOP hosts are ignored.
	nodePath := filepath.Join(data, "nodes.json")
	var extra node.File
	var ignoredRemote []string
	if st, statErr := os.Stat(nodePath); statErr == nil && !st.IsDir() {
		f, err := node.Read(nodePath)
		if err != nil {
			log.Fatalf("节点清单: %v", err)
		}
		for _, spec := range f.Nodes {
			if spec.Audit {
				extra.Nodes = append(extra.Nodes, spec)
				continue
			}
			ignoredRemote = append(ignoredRemote, spec.ID)
		}
	} else if statErr != nil && !os.IsNotExist(statErr) {
		log.Fatalf("节点清单 %s: %v", nodePath, statErr)
	}
	if len(ignoredRemote) > 0 {
		log.Printf("nodes: 单机模式忽略远端节点 %s（只保留本机宿主）", strings.Join(ignoredRemote, ", "))
	}
	if bin == "" {
		log.Fatalf("找不到 aiscan-full：本机宿主需要它（--aiscan 指定，或放到 tw 同目录 / PATH）")
	}

	mgr, err := project.Open(filepath.Join(data, "projects"))
	if err != nil {
		log.Fatalf("open projects: %v", err)
	}
	if mgr.Len() == 0 {
		if _, err := mgr.EnsureDefault("默认项目", *scope); err != nil {
			log.Fatalf("create default project: %v", err)
		}
	}
	if *projectID != "" && mgr.Get(*projectID) == nil {
		log.Fatalf("没有这个项目：%s（现有：%s）", *projectID, projectIDs(mgr))
	}

	// Skills travel with the binary and are instance-level: house style is not
	// an engagement. The executor gets no built-in skill; the handoff contract
	// is part of the dispatch prompt, so it cannot fail to load (D32).
	skillDir := filepath.Join(data, "skills")
	if _, err := skills.Ensure(skillDir); err != nil {
		log.Printf("skills: %v", err)
	}
	// The harness host discovers skills under `.cyber/skills` relative to its
	// working directory (<data>). Putting the commander skill there is how a
	// session expands /skill:commander with the harness's own mechanism (D78).
	if p, err := skills.Install(skills.Commander, filepath.Join(data, ".cyber", "skills")); err != nil {
		log.Printf("commander skill: %v", err)
	} else {
		log.Printf("commander skill: %s", p)
	}
	// The executor discovers this one by name in its system prompt; it is not
	// injected into the dispatch brief, so runs that never collaborate do not
	// pay for its body.
	if p, err := skills.Install(skills.Collaborate, filepath.Join(data, ".cyber", "skills")); err != nil {
		log.Printf("collaborate skill: %v", err)
	} else {
		log.Printf("collaborate skill: %s", p)
	}
	resolved, err := skills.Resolve(strings.Split(*extraSkills, ","), skillDir)
	if err != nil {
		log.Fatalf("skills: %v", err)
	}
	reportSkills, err := skills.Resolve([]string{skills.Report}, skillDir)
	if err != nil {
		log.Fatalf("report skill: %v", err)
	}
	if len(resolved) > 0 {
		log.Printf("skills: %d 个 · %s", len(resolved), strings.Join(resolved, ", "))
	}

	// The offline KB travels with the binary and is materialised under the data
	// dir so an executor can grep it from the shell it already has. Built-in
	// entries live in kb/builtin; kb/entries is the operator's, never rewritten.
	kbDir := filepath.Join(data, "kb")
	builtin, err := kb.Ensure(kbDir)
	if err != nil {
		log.Printf("kb: %v", err)
	}
	kbLib, err := kb.Open(kbDir)
	if err != nil {
		log.Printf("kb: %v", err)
	} else if n := len(kbLib.Entries()); n > 0 {
		if local := n - builtin; local > 0 {
			log.Printf("kb: %d 条（内置 %d + 现场 %d）· %s", n, builtin, local, kbDir)
		} else {
			log.Printf("kb: %d 条 · %s", n, kbDir)
		}
	}

	// The POC library and the tool directory are shared: a template and an
	// installed binary are tools, not findings.
	lib := pocs.New(filepath.Join(data, "pocs"))

	// The vulnerability library is the one thing that outlives an engagement:
	// every vuln fact is mirrored here and deleting a project leaves it intact.
	vulns, err := vulnlib.Open(filepath.Join(data, "vulns"))
	if err != nil {
		log.Fatalf("open vuln library: %v", err)
	}

	// Model configuration is instance-level and edited from the console. It
	// lives in the harness's own file (llm.providers + llm.active_profile), so
	// the decider, the REPL and every local run read one source. settings.json
	// is the pre-harness fallback and is kept in sync.
	if *harnessConfig != "" {
		_ = os.Setenv("TW_HARNESS_CONFIG", absPath(*harnessConfig))
	}
	st, err := settings.Load(filepath.Join(data, "settings.json"))
	if err != nil {
		log.Fatalf("settings: %v", err)
	}
	if *modelURL != "" {
		_ = os.Setenv(modelclient.EnvBase, *modelURL)
	}
	if *modelKey != "" {
		_ = os.Setenv(modelclient.EnvKey, *modelKey)
	}
	if *modelName != "" {
		_ = os.Setenv(modelclient.EnvModel, *modelName)
	}
	eff := st.Effective()
	harnessArgs, err := runtimeHarnessArgs(st, data, eff)
	if err != nil {
		log.Fatalf("准备宿主运行配置：%v", err)
	}
	// The harness reads the same values from its own env/flags; the local host
	// gets them at start, and audit runs get them per run.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The console token is resolved before the host starts: the commander
	// session calls the control-plane API and reads the token from the host's
	// environment (D78), so it must exist by then.
	token, tokenPath, generated, err := ensureConsoleToken(data, *consoleToken, *consoleTokenFile)
	if err != nil {
		log.Fatalf("控制台令牌：%v", err)
	}
	if generated {
		log.Printf("已生成控制台令牌并写入 %s：%s", tokenPath, token)
	}

	// The local host is the first node (D64). It runs from the data directory:
	// an agent's file tools only resolve paths below their working dir, and the
	// case directories live under <data>/projects/<id>/cases.
	var localHost *host.Host
	specs := make([]node.Spec, 0, len(extra.Nodes)+1)
	// A previous process may have died without stopping its host: the child
	// outlives tw and keeps its port, session database and PTY shells, so the
	// next start loses the port or two hosts write one DB. Reaping is limited
	// to `aiscan*` processes started from *this* data directory and never
	// touches this process's family (docs/17 §3.6; host.SweepOrphans).
	lastHost, hadHost, recErr := host.ReadRecord(data)
	if recErr != nil {
		log.Printf("host: 上次宿主记录不可用：%v", recErr)
	}
	if hadHost {
		log.Printf("host: 上次宿主 endpoint=%s pid=%d started_at=%s restarts=%d（记录仍在，说明上次没有正常退出）",
			lastHost.Endpoint, lastHost.PID, lastHost.StartedAt.Format(time.RFC3339), lastHost.Restarts)
	}
	orphans, sweepErr := host.SweepOrphans(ctx, data, log.Printf)
	switch {
	case sweepErr != nil:
		log.Printf("host: 清理陈旧宿主失败（不影响启动）：%v", sweepErr)
	case orphans.DryRun:
		log.Printf("host: 陈旧宿主 dry-run：候选 %d，未回收（TW_HOST_CLEANUP=dry）", orphans.Candidates)
	case orphans.Candidates > 0:
		log.Printf("host: 陈旧宿主：候选 %d，回收 %d，失败 %d", orphans.Candidates, orphans.Reaped, orphans.Failed)
	}
	localHost, err = host.Start(ctx, host.Config{
		Bin:  bin,
		Dir:  data,
		Addr: *hostAddr,
		// The host's sessions (commander and executors) inherit this
		// environment; it gets the scoped agent token, never the operator's
		// login token (D109). An inherited TW_CONSOLE_TOKEN is dropped below
		// even when tw itself was started with it.
		Env:             hostEnvWithAgentToken(eff.Env(), token),
		DropEnvPrefixes: []string{"TW_CONSOLE_TOKEN"},
		Args:            harnessArgs,
		ExcludeTools:    excludeTools,
		// The local host is the one endpoint recorded on disk: its PID is how
		// the next start knows the previous process did not stop cleanly.
		Persist: true,
		// With a model configured every run needs a bound provider; a host whose
		// provider init timed out must be restarted, not adopted as ready
		// (docs/17 §3.6). If it never gets ready, keep the console up in
		// degraded mode so the operator can fix the provider from 设置 →
		// 模型 (D101); --require-host-ready restores the old hard exit.
		RequireProvider: eff.Configured(),
		AllowNotReady:   !*requireHostReady,
		Logf:            log.Printf,
	})
	if err != nil {
		if errors.Is(err, host.ErrNotReady) && localHost != nil {
			log.Printf("WARNING: %v", err)
			log.Printf("WARNING: 控制台以降级模式继续启动：可以查看账本、在「设置 → 模型」修好连接；保存后宿主会自动重启，就绪前不会派发新 run（--require-host-ready 可改回直接退出）")
		} else {
			log.Fatalf("本机宿主: %v", err)
		}
	}
	// Session pointers from a previous endpoint are unusable — the next retry
	// would resume a session the live host does not have ("会话属于 18767 不是
	// 49850"). Sweep once here, before projects are attached, and again from
	// the ready hook if a restart ever lands on a different endpoint.
	swept, sweepErr := host.SweepStaleSessions(ctx, data, localHost.Endpoint(), log.Printf)
	if sweepErr != nil {
		log.Printf("host: 清理陈旧会话指针失败（不影响启动）：%v", sweepErr)
	} else if swept.Cleaned > 0 || swept.Failed > 0 {
		log.Printf("host: 会话指针清理：改名 %d，保留 %d，失败 %d", swept.Cleaned, swept.Kept, swept.Failed)
	}
	sweptFor := strings.TrimRight(localHost.Endpoint(), "/")
	localHost.AddOnReady(func(aop.Agent) {
		ep := strings.TrimRight(localHost.Endpoint(), "/")
		if strings.EqualFold(ep, sweptFor) {
			return
		}
		// Endpoint changed across a restart: markers that named the old one
		// are stale by definition. Sweep once per new endpoint.
		sweptFor = ep
		s, err := host.SweepStaleSessions(ctx, data, ep, log.Printf)
		if err != nil {
			log.Printf("host: endpoint 变更，会话指针清理失败（不影响运行）：%v", err)
		} else if s.Cleaned > 0 {
			log.Printf("host: endpoint 变更，会话指针清理：改名 %d", s.Cleaned)
		}
	})
	// The host token is a shell on this machine: keep the socket on loopback
	// unless the operator really means to expose it (docs/14-nodes.md).
	if isPlaintextRemote(localHost.Endpoint()) {
		log.Printf("警告：本机宿主监听在 %s（非 loopback）：它的 token 等于这台机器的 shell，只应放在可信网段", localHost.Endpoint())
	}
	specs = append(specs, node.Spec{
		ID:    "local",
		AOP:   localHost.Endpoint(),
		Token: localHost.Token(),
		Agent: localHost.Agent(),
		Note:  "本机宿主（控制台启动的 aiscan web）",
	})
	specs = append(specs, extra.Nodes...)
	// The IOA observer is read-only: it tails the local host's space, parses v1
	// application records, and stores them for the console. It never sends or
	// acknowledges anything.
	var observer *ioa.Observer
	if localHost != nil {
		client := ioa.New(localHost.Endpoint(), localHost.Token(), localHost.Agent())
		if o, err := ioa.NewObserver(client, filepath.Join(data, "ioa")); err != nil {
			log.Printf("ioa observer: %v", err)
		} else {
			observer = o
		}
	}
	nodes, err := node.New(specs, "local")
	if err != nil {
		log.Fatalf("节点清单: %v", err)
	}
	if len(extra.Nodes) == 0 {
		log.Printf("nodes: 单机模式，只有本机宿主")
	} else {
		log.Printf("nodes: 单机模式，本机宿主 + %d 个本地审计节点", len(extra.Nodes))
	}
	for _, n := range nodes.Infos() {
		mark := ""
		if n.Default {
			mark = "（默认）"
		}
		note := ""
		if n.Note != "" {
			note = " · " + n.Note
		}
		if n.Transport == "aop" {
			note += " · AOP " + n.Endpoint
		}
		log.Printf("  node %s%s%s", n.ID, note, mark)
	}
	if missing := nodes.Missing(); len(missing) > 0 {
		log.Printf("WARNING: 审计节点找不到可执行文件：%s", strings.Join(missing, "；"))
	}
	warnExposure(*addr, nodes)
	// Audit runs are local processes: they take the instance's harness settings.
	// They are harness processes too, so the same tool exclusion applies.
	auditEnv := eff.Env()
	if excludeTools != "" {
		auditEnv = append(auditEnv, "CYBER_EXCLUDE_TOOLS="+excludeTools)
	}
	nodes.SetLLM(auditEnv, harnessArgs)
	// A named model profile runs on its own local host, started lazily with a
	// one-profile configuration. The default model keeps using the host above,
	// so an instance with one profile behaves exactly as before.
	var h *hub
	var profileHosts *host.Pool
	if localHost != nil {
		profileHosts = host.NewPool(host.PoolConfig{
			Bin: bin, Dir: data, Settings: st, Timeout: *timeout,
			ExcludeTools: excludeTools, Logf: log.Printf,
			OnReady: func(profile string, runner host.Runner) {
				h.observeNativeHost(profile, runner.Endpoint(), runner.Token(), runner.Agent())
			},
		})
		nodes.SetLocalProfiles("local", profileHosts)
	}

	cfg := hubConfig{
		DataDir:      data,
		Nodes:        nodes,
		LocalHost:    localHost,
		ProfileHosts: profileHosts,
		IOA:          observer,
		APIBase:      apiBase(*addr),
		ConsoleToken: token,
		Collab:       collab.NewRegistry(24 * time.Hour),
		Timeout:      *timeout,
		IdleTimeout:  *idleTimeout,
		MaxEvidence:  *maxEvidence,
		Scope:        *scope,
		Skills:       resolved,
		SkillsDir:    skillDir,
		KB:           kbLib,
		KBDir:        kbDir,
		ReportSkills: reportSkills,
		VerifyAuto:   *verifyAuto,
		MaxRuns:      *maxRuns,
		MaxWorkers:   *maxWorkers,
		// Optional token gate: observation is always on, the refusal is opt-in.
		Debounce:       *debounce,
		Heartbeat:      *heartbeat,
		RoundTimeout:   *roundTimeout,
		NoDecider:      *noDecider,
		DefaultProject: *projectID,
	}
	h = newHub(ctx, cfg, mgr, st, lib, vulns)
	h.start()
	if observer != nil {
		observer.OnRecord = h.onNativeIOA
		go observer.Run(ctx, 5*time.Second, func(err error) { log.Printf("ioa observer: %v", err) })
	}
	if localHost != nil {
		// Wakes that were deferred because the host was restarting land here,
		// once the host can actually run a round (D111). This runs on the
		// supervisor goroutine; wakeAll only posts to the deciders.
		localHost.AddOnReady(func(aop.Agent) { h.wakeAll("host_ready") })
	}

	for _, p := range mgr.All() {
		log.Printf("project: %s（%s）", p.ID, p.Name)
	}
	if eff.Configured() && !*noDecider {
		log.Printf("decision: model=%s", eff.Model)
	} else if !*noDecider {
		log.Printf("decision: 未配置模型（控制台「设置」里配，或设 CYBER_*）")
	}

	// A control plane that dispatches attacks is never unauthenticated: the
	// token was resolved before the host started, above.
	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           withSecurityHeaders(withConsoleAuth(h, token)),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
	}

	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		// Restore the default signal action now: a second Ctrl+C must work
		// even if a host or transport is stuck in its cleanup.
		stop()
		shutdownConsole(httpSrv, 10*time.Second, func() {
			// A run's context is detached from the process on purpose (a
			// client disconnect must not kill a scan), which also means stopping the
			// server does not stop the harness: without this, Ctrl-C leaves an
			// aiscan-full writing into a case directory nobody owns any more.
			var waits sync.WaitGroup
			for _, p := range mgr.All() {
				k := h.kit(p.ID)
				if k == nil {
					continue
				}
				if n := k.srv.BeginShutdown(); n > 0 {
					waits.Add(1)
					go func() { defer waits.Done(); k.srv.WaitIdle(5 * time.Second) }()
					log.Printf("shutdown: canceled %d run(s) in %s", n, p.ID)
				}
			}
			waits.Wait()
			// The local host is stopped last: runs above may still be talking to it.
			if localHost != nil {
				log.Printf("shutdown: 停止本机宿主")
				waits.Add(1)
				go func() { defer waits.Done(); localHost.Stop() }()
			}
			if profileHosts != nil {
				log.Printf("shutdown: 停止模型配置宿主")
				waits.Add(1)
				go func() { defer waits.Done(); profileHosts.Stop() }()
			}
			waits.Wait()
		})
	}()

	log.Printf("tianwu 天吴 listening on http://%s", *addr)
	log.Printf("  data=%s", data)
	log.Printf("  控制台 http://%s/ （%d 个项目）", *addr, mgr.Len())
	if err := httpSrv.Serve(listener); err != nil && err != http.ErrServerClosed {
		log.Fatalf("serve: %v", err)
	}
	<-shutdownDone
}

// listenConsole reserves the console address. Separated so the "second instance
// must fail before the orphan sweep" property is testable.
func listenConsole(addr string) (net.Listener, error) {
	return net.Listen("tcp", addr)
}

func projectIDs(m *project.Manager) string {
	all := m.All()
	out := make([]string, 0, len(all))
	for _, p := range all {
		out = append(out, p.ID)
	}
	return strings.Join(out, ", ")
}

// apiBase turns the listen address into the URL the commander session uses to
// call this control plane. Wildcard binds are reached over loopback: the local
// commander runs on this machine.
func apiBase(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

func absPath(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}
