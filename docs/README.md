# WeClaw 文档

这里描述当前 Codex-only WeClaw 的使用、架构和运维边界。根目录 README 只负责产品说明和快速开始，细节统一以本目录为准。

## 阅读路径

第一次使用按以下顺序阅读：

1. [安装与启动](guides/getting-started.md)
2. [配置参考](guides/configuration.md)
3. [微信任务与进度](guides/tasks.md)
4. [会话管理](guides/sessions.md)
5. [视觉回复](guides/visual-replies.md)

## 用户指南

| 文档 | 解决的问题 |
| --- | --- |
| [安装与启动](guides/getting-started.md) | 构建、登录、启动和本机服务 |
| [配置参考](guides/configuration.md) | 项目、Codex、视觉、语音、自动化和主动发送 |
| [微信任务与进度](guides/tasks.md) | 排队、取消、恢复、进度和无回复诊断 |
| [会话管理](guides/sessions.md) | 新建、搜索、切换、归档和项目隔离 |
| [文件与交付](guides/media-and-deliveries.md) | 图片、附件、交付物、素材和再次发送 |
| [视觉回复](guides/visual-replies.md) | 五套模板、阅读卡、图片批次和文字版 |

## 架构

| 文档 | 主题 |
| --- | --- |
| [架构总览](architecture/overview.md) | 代码目录、依赖层次、输入与交付主链路 |
| [持久任务队列](architecture/task-queue.md) | FIFO、冻结结果、投递事务和重启恢复 |
| [可靠控制面](architecture/control-plane.md) | 控制领域、Presenter 和状态边界 |
| [类型化控制路由](architecture/control-routing.md) | Intent Registry、ActionResult 和副作用入口 |
| [持久交互](architecture/persistent-interactions.md) | revision、待输入状态和幂等回执 |
| [统一状态内核](architecture/state-kernel.md) | 原子写入、严格 schema、租约和备份 |
| [本机管理安全](architecture/management-security.md) | Unix socket、主动发送和可信代理 |
| [确定性诊断](architecture/diagnostics.md) | “为什么没回复”的证据与隐私边界 |
| [持久工作流](architecture/workflows.md) | 快捷任务、参数槽、复用和项目隔离 |

## 运维

| 文档 | 使用时机 |
| --- | --- |
| [日常部署](operations/deployment.md) | 构建候选、事务升级、状态验证和回滚 |
| [旧生产线迁移](operations/migration.md) | 从旧状态和旧管理面一次性迁入当前主线 |
| [首次生产切换](operations/production-cutover.md) | 维护窗口、备份、真机矩阵和稳定观察 |
| [完整验收清单](operations/acceptance.md) | 发布前自动化、故障注入和微信回归 |
| [产品路线](roadmap.md) | 当前基线、后续阶段和明确不做的能力 |

## 仓库结构

```text
cmd/weclaw/        唯一二进制入口
internal/          不对模块外公开的全部实现
docs/guides/       用户可见行为与配置
docs/architecture/ 代码边界、状态和事务设计
docs/operations/   部署、迁移与验收
scripts/           明确用途的辅助安装脚本
service/           systemd 与 launchd 服务定义
```

## 文档约束

- 当前行为写入 `guides/`，内部不变量写入 `architecture/`，人工操作写入 `operations/`。
- 文档文件名按主题命名，不再用版本号充当目录结构。
- 破坏性变更必须同时修改根 README、对应专题文档、构建命令和验收项。
- 同一个配置示例只在 [配置参考](guides/configuration.md) 维护，其他文档只链接，不复制整份 JSON。
- 与代码冲突时，以严格配置校验、状态 schema 和自动化测试为事实来源，并立即修正文档。
