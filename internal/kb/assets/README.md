# 事实知识卡

本目录收录事实特征：适用版本、默认端点、端口、指纹、协议行为和限制，不收攻击步骤或工具教程。执行流程由模型及 Skill 负责，角色边界见 [核心架构](../../../docs/01-architecture.md)。

内置条目位于 [entries](entries/README.md)。部分内容改编自 [sapper-v2](https://github.com/N1nEmAn/sapper-v2)，其余由维护者整理；各卡的 `source` 字段记录具体来源。sapper-v2 原有 MIT 许可及版权声明保留在 [NOTICE](../../../NOTICE) 中。

## 维护方式

- 公共条目放入 `entries/`，文件名与 `id` 一致，使用 JSON；构建时由 `go:embed` 带入二进制。
- 字段、格式与收录判据见 [条目说明](entries/README.md)。命令仅用于验证事实，不构成固定测试流程。
- 实例现场条目放入 `<data>/kb/entries/`，在操作员重启后加载，不进版本库。
- 修改后运行 `go test ./internal/kb`，保留来源与适用条件，不提交客户材料或私有笔记路径。
