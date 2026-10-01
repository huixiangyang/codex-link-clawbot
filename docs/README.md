# 文档

这里说明当前源码的使用方式、业务规则和维护边界，不作为线上部署状态或历史验收证明。先确认正在使用的程序版本，再按对应源码的文档操作。

## 使用

| 要做的事 | 入口 |
| --- | --- |
| 安装、扫码、配置第一个项目、启动 | [首次使用](guides/getting-started.md) |
| 在微信提交工作、使用菜单、取回结果 | [工作台指南](guides/workbench.md) |
| 修改配置、工作空间、阅读图和语音能力 | [配置指南](guides/configuration.md) |

## 开发与设计

| 要了解的事 | 入口 |
| --- | --- |
| 本地检查、代码修改位置、隔离预览 | [开发指南](guides/development.md) |
| 模块边界、执行流程、管理 API | [架构总览](architecture/overview.md) |
| 令牌、权限、可信目录与宿主访问 | [安全模型](architecture/management-security.md) |
| SQLite 表职责、目录、留存与文件校验 | [数据与产物](design/sqlite-storage.md) |
| 项目来源、命名和许可证 | [项目来源](architecture/upstream.md) |

## 运维

| 要做的事 | 入口 |
| --- | --- |
| 安装用户服务、升级版本、处理提交不确定 | [部署](operations/deployment.md) |
| 从最后一代 JSON 状态迁入 SQLite | [离线迁移](operations/migration.md) |
| 查看日志、定位连接与投递问题 | [日志与排障](operations/logging.md) |
| 验证自动检查、隔离流程与真实渠道 | [验收](operations/acceptance.md) |

## 维护规则

同一主题只在对应文档维护完整规则，其他页面使用链接，不复制阶段计划或已完成任务清单。行为、接口、配置、命令变化时同步修改对应文档，并运行 `make docs-check`。历史决策由 Git 追溯，不在现行指南混入旧截图、旧能力盘点或“本轮完成”记录。

本地测试通过、真实微信与 Codex 联调通过、提交推送和部署完成是不同结论，交付时必须分别说明。
