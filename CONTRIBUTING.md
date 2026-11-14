# 贡献指南

欢迎提交问题、文档修正与代码贡献。参与前请阅读 [安全策略](SECURITY.md)、[核心架构](docs/01-architecture.md)与[实现约定](agent.md)。

## 授权与内容来源

只提交虚构目标、假凭据和脱敏材料。不要提交客户信息、真实目标、证据、API key、登录令牌或本地配置。第三方内容需说明来源并保留许可证。贡献本项目即表示你有权提交这些内容，并同意按 [AGPL-3.0](LICENSE) 发布；第三方资产遵循其原有许可，见 [NOTICE](NOTICE)。

## 开发环境与检查

使用 Go 1.26+、Node.js 24+ 和 pnpm。Windows 执行宿主还需要 Git for Windows 的 Bash 与 POSIX 工具。先初始化 `cyber-ui` 子模块：

```bash
git submodule update --init --recursive
pnpm install
pnpm --dir web typecheck
pnpm --dir web test
pnpm --dir web build
go build ./...
go test ./...
go vet ./...
node scripts/check-docs.mjs
```

前端测试包含纯函数和真实 React 组件的状态回归，不需要运行控制面或调用模型。`internal/web/static` 是入库的嵌入产物，前端源码修改后必须重新构建并一并提交。项目不依赖常规 CI 才能提交，提交者需要在本地完成检查；资源不足时可用 `go test -p 1 ./...`。并发敏感改动（复核结算、调度/唤醒、store 事务、decider）还应在有 C 工具链的环境跑 race 检测（Go race detector，需要 `CGO_ENABLED=1` 与 gcc）：Windows 用 `powershell -ExecutionPolicy Bypass -File scripts/race.ps1`，git-bash/WSL 用 `make race`；两者与 `.github/workflows/race.yml` 跑同一组包（该工作流已改为手动触发，因为当前账号的 Actions 计费不可用）。Windows 上没有 gcc 时，可先用 winget 安装 MinGW-w64（`winget install BrechtSanders.WinLibs.POSIX.UCRT`，装完重开终端；`gcc --version` 应输出版本）。装好后也可以直接跑 `CGO_ENABLED=1 go test -race -count=1 -timeout 30m ./internal/api ./internal/lifecycle ./internal/verifier ./internal/store ./internal/decider ./internal/board ./internal/collab`。也可以改用 WSL/Docker。

harness 独立维护在 `kksty/cyber-harness` 的 `tianwu/fixes` 分支，构建方式见 [README](README.md)。不要把 harness 源码或二进制复制进本仓库。需要宿主集成测试时，显式设置 `TW_AISCAN_BIN` 指向该二进制；未设置时相关测试跳过。隔离验证及浏览器脚本见[验收说明](scripts/acceptance/README.md)。

## 变更要求

- 为行为修复添加覆盖真实失败模式的回归测试；协议、授权与数据模型改动需说明兼容性和失败模式。
- 当前架构集中维护在 `docs/01-architecture.md`。修改行为时同步更新该文档或 README，不再新增阶段评审、路线图或交接文档。
- 一个 PR 聚焦一个问题，描述触发条件、前后行为与验证结果。
- 不提交 `data/`、`.build/`、`bin/`、`logs/`、`platform.yaml` 或数据快照。发布前用仓库的 `.gitleaks.toml` 扫描待发布内容。
- 不重启正在使用的控制面或宿主，不修改运行中的项目数据。验证使用临时目录与本地假模型。

## 反馈问题

Bug 请附版本或提交号、复现步骤、期望与实际行为及脱敏日志。功能建议请说明场景与边界。天吴自身安全问题通过 [安全策略](SECURITY.md)规定的私密渠道报告，不在公开 issue 中披露利用细节。
