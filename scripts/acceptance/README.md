# 隔离验收与维护脚本

本目录提供协议、界面与演示靶场的本地维护工具。基础检查见 [贡献指南](../../CONTRIBUTING.md)，验证边界见 [核心架构](../../docs/01-architecture.md)。所有验证使用临时目录、虚构目标和假凭据，不读取或改写正在使用的项目。

## 日常回归

```bash
pnpm --dir web typecheck
pnpm --dir web test
pnpm --dir web build
go test ./...
node scripts/check-docs.mjs
```

前端测试包含真实 React 组件的草稿归属、版本冲突、模型暂停和连通性检测等状态回归，不需要 Chrome、运行中的控制面或外部模型。

## 浏览器界面检查

浏览器脚本需要本机 Chrome，具体依赖与环境变量见各脚本开头。先构建前端，再从仓库根目录运行，例如：

```bash
node scripts/acceptance/projects-ui.cjs
node scripts/acceptance/instructions-ui.cjs
node scripts/acceptance/focus-context-ui.cjs
node scripts/acceptance/attention-ui.cjs
```

这些脚本使用合成 HTTP 响应，检查项目删除、持久指令、图谱与上下文编辑、事件待办及窄屏布局；截图和结果写入 `.build/ui-qa/`。部分其他脚本依赖本地未入库 fixture，公开 checkout 未必能直接运行全部脚本。不要把真实项目抓取结果作为公开 fixture 提交。离线 UI 检查不证明服务端行为或模型效果。

## 真实 harness 适配验证

从独立 fork 构建 `aiscan-full` 后，可显式启用集成测试：

```powershell
$env:TW_AISCAN_BIN = (Resolve-Path ./bin/aiscan-full.exe).Path
go test ./internal/api ./internal/host -run 'Harness|ProfileHosts' -count=1 -v
```

测试使用临时项目、本地假模型网关和受控 fixture，启动测试宿主并在退出时停止它。它们验证 Skill 展开、协议和宿主适配，不证明实际漏洞发现能力。未设置变量时相关测试跳过。

## 演示靶场

`target.py` 是含预置缺陷的虚构站点，仅供本机隔离验收。在专用终端中运行：

```powershell
$env:TW_TARGET_HOST = '127.0.0.1'
python scripts/acceptance/target.py
```

默认端口为 18099。真实模型实验必须使用独立控制面端口、全新数据目录及专用模型配置，明确授权范围后由操作员启动。固定样本的结果不能推广为真实业务覆盖率；比较协作策略时应保持模型、初始数据与资源条件一致，并记录独立复现、漏测、耗时、token 和人工介入。
