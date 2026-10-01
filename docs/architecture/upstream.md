# 项目来源

`codex-link-clawbot` 基于 [WeClaw](https://github.com/fastclaw-ai/weclaw) 独立开发，保留原始 Git 历史与根目录 [MIT 许可证](../../LICENSE)。许可证中的原始版权声明为 `Copyright (c) 2026 fastclaw-ai`，分发时应一并保留。

本项目不是 WeClaw 官方发行版，也不隶属于微信、腾讯或 OpenAI。名称和界面中的 Codex、微信用于说明集成对象，不表示授权或背书。

## 项目标识

| 对象 | 值 |
| --- | --- |
| 项目及程序 | `codex-link-clawbot` |
| Go module | `github.com/huixiangyang/codex-link-clawbot` |
| 默认状态根 | `~/.codex-link-clawbot` |
| 配置协议顶层键 | `codex-link-clawbot` |
| 环境变量前缀 | `CODEX_LINK_CLAWBOT_` |
| systemd 用户服务 | `codex-link-clawbot.service` |
| launchd 标识 | `com.huixiangyang.codex-link-clawbot` |

当前实现只集成 Codex App Server，不提供多模型路由或旧协议回退。运行时不读取其他项目名称下的配置和目录；本项目最后一代 JSON 状态仅可通过显式[离线迁移](../operations/migration.md)导入。

新增修改继续采用仓库的 MIT 许可证。项目定位与实际能力见[工作台指南](../guides/workbench.md)，技术边界见[架构总览](overview.md)。
