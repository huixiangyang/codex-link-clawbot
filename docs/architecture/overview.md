# 架构总览

项目是一个 Go 模块化单体：单个运行实例拥有一个微信绑定、一个 Codex App Server 子进程、一套 SQLite 状态和本机文件。微信与网页共享业务服务，不是两套执行架构。

## 模块边界

```text
cmd/codex-link-clawbot
  -> cli -> app
             -> adapters/wechat      微信协议、菜单、媒体和输出
             -> adapters/management  HTTP、Unix socket 与嵌入页面
             -> adapters/appserver   Codex 子进程与 JSON-RPC
                    -> core          会话、执行、请求、目标与业务规则
                         -> platform SQLite、私有文件、运行锁与日志
```

`internal/` 只保留 `app`、`cli`、`adapters`、`core`、`platform` 五个一级目录。允许使用下层能力，不强制逐层转发；核心可直接使用 SQLite 基础设施，不另建通用 repository 层。适配器之间不互相导入，由 `app` 装配。架构测试也检查测试代码的依赖方向。

| 模块 | 职责 |
| --- | --- |
| `app` / `app/config` | 配置、依赖装配、后台服务启动与退出 |
| `app/migration` | CLI 专用离线导入，运行服务不能调用 |
| `core/conversation` | 创建、选择、改名、归档、可信归属与精确打断 |
| `core/execution` | 准入互斥、生命周期、取消仲裁、结果恢复与进度 |
| `core/request` | 请求、输入、完成检查点、结果、回执、产物及留存 |
| `core/target` | 当前目标与尚未解析的会话意图 |
| `core/thread` / `workspace` | 线程目录、可信路径、工作空间归属 |
| `core/preference` / `presentation` | 回复偏好、共享模式与文本规则 |
| `core/access` / `runtimecontrol` / `delivery` | 远程锁、健康与排空、异常待办通知 |
| `core/codex` | Codex 能力契约，不含进程或 RPC 实现 |
| `platform/storage` / `statefile` / `logging` | 事务与布局、文件保护与租约、独立诊断日志 |

源码装配入口是 [app.Run](../../internal/app/app.go)，开发定位见[开发指南](../guides/development.md)。

## 一次请求

1. 微信适配器校验绑定者、消息类型和来源。控制输入交给菜单或快捷操作；附件先记录草稿，显式提交才进入准入。
2. 协调器将排空检查、来源去重和会话占用检查置于同一准入边界。忙碌则持久化拒绝回执，不创建等待工作。
3. 请求固定目标、输入和回复偏好并落盘，准备阶段已经可见且可取消。执行器下载和校验附件，不阻塞消息入口。
4. 核心 `execution.Runner` 解析或创建真实线程，提交 Codex 轮次，持久化真实阶段，并确认指定轮次的最终状态。等待提示从准备阶段计时，微信最多发送一次延迟提示，不发送即时回执或逐阶段消息。
5. 执行完成事实和回答检查点先持久化，再收集、校验并冻结产物。归档失败保留成功事实，只允许恢复保存。
6. `wechat.executionChannel` 提供渠道 I/O。首次投递回执和请求终态原子提交；失败不改变 Codex 已完成的事实。

协调器决定是否准入、是否占用与何时可取消；Runner 决定执行生命周期；渠道负责下载、通知和发送，不能自行改写执行终态。线程和轮次由 Codex 拥有，桥接器不复制其完整对话数据库。

## 一致性约束

- 只有一个明确选定的绑定可以提交工作；请求、偏好、结果和目标按所有者隔离。
- 同一会话最多一个活动请求，其他会话可独立执行；没有队列、暂停队列或自动重试。
- 工作目标在接收时固定。远端校验不持有目标写锁，提交按快照修订校验，切走再切回也不能接受过期操作。
- 取消只作用于观察到的请求或原生轮次，只有上游确认停止才标记取消；未知状态不允许直接重跑，实际完成仍保留成果。
- 进入结果收尾后拒绝取消。执行、归档、渠道送达是不同事实，恢复操作不能混用。
- 进程重启不自动重跑或重投。来源回执在业务副作用前持久化，重复消息和操作编号不产生第二次工作。
- 每个状态根只装配一个运行期请求存储和目标存储，事务成功才推进内存基线。运行锁不允许第二个服务或离线写入者并行操作。

线程目录缓存最多三秒，失效后旧的在途查询不能回填；执行准入和详情读取新鲜状态，缓存不决定执行权。菜单编号绑定展示时 ID，十分钟或重启后失效。

## 数据读取

关系表保存元数据，文件保存输入和输出内容。`ListSummaries` 在请求状态读锁内批量读取回执，不逐条打开数据库；`CataloguePage` 在同一 SQLite 快照计数并查询当页。

`InspectRequest` / `InspectResult` 验证所有者、有效期和元数据，用于原文与清单展示。执行和重试使用完整输入校验，实际投递使用完整结果校验；`OpenArtifact` 只校验所选文件，并返回回卷后的同一文件句柄。元数据可读不表示附件一定完好。目录、事务和留存见[数据与产物](../design/sqlite-storage.md)。

## 进程生命周期

CLI 取得运行租约后初始化日志和配置，选择绑定；`app` 装配各领域存储、Codex、微信监听、网页管理和 Unix socket。Codex 或启用的图片能力初始化失败时启动失败，不回退到其他模型协议。

后台服务组统一管理 HTTP、协调器、微信监听、每分钟保留清理和 Codex 退出观察。启动中途失败、后台错误或 context 取消走同一清理路径：取消后台循环、等待退出、回收 Codex。HTTP 关闭最多等待在途请求五秒。

`status`、排空和恢复走属主私有 Unix socket；网页令牌不能直接调用机器升级协议。不再提供部署通知接口，部署结果写入运维回执。部署提交点与失败恢复见[部署](../operations/deployment.md)。

## 管理 API

以下是 [HTTP 路由](../../internal/adapters/management/console.go)的完整清单。全部要求 `X-Codex-Link-Token`，返回 `Cache-Control: no-store`；请求与产物限制到当前绑定，会话操作限制到可信工作空间。

| 方法与路径 | 参数与用途 |
| --- | --- |
| GET `/api/snapshot` | 当前目标、连接、执行状态、能力、偏好、锁定、容量及留存 |
| GET `/api/requests` | `view` 为 active、attention、history、all；`q` 搜索；`page`、`page_size` 分页 |
| GET `/api/artifacts` | 有效产物按生成时间倒序，使用 `page`、`page_size` |
| GET `/api/requests/{id}` | 输入文字、附件名、执行与投递状态、回答和产物清单 |
| GET `/api/requests/{id}/artifacts/{index}` | `index` 从 0 开始，校验后下载 |
| POST `/api/requests/{id}/actions` | `action` 为 cancel、retry、redeliver、restore、release；retry、redeliver 需 UUID `operation_id` |
| GET `/api/conversations` | `q` 搜索，`page`、`page_size` 分页，返回可信目录中未归档的会话 |
| POST `/api/conversations` | `workspace_id` 和可选 `name`，创建并选择，成功返回 201 |
| GET `/api/conversations/{id}` | 本地请求及原生轮次的实时活动状态 |
| POST `/api/conversations/{id}/actions` | rename 携带 `name`，archive 归档，interrupt 携带对应 `task_id` 或 `turn_id` |
| PUT `/api/target` | `workspace_id` 和可选 `thread_id`，原子切换目标 |
| PUT `/api/preferences` | `response_mode` 与 `style`，整体验证和保存 |
| POST `/api/runtime` | `action` 为 drain 或 resume |
| POST `/api/lock` | `locked` 布尔值，解锁携带 `code` |

分页从 1 开始，`page_size` 为 1–50，缺省或无效值取 12。请求和产物列表超页返回末页；会话列表当前超页返回 503，调用方应根据 `pages` 限制翻页。列表返回 `items`、`total`、`page`、`pages`，请求列表另有不随搜索和分类缩小的 `counts`。

JSON 写入最大 64 KiB，拒绝未知字段和多个 JSON 值。错误对象包含 `error`；无效令牌返回 401，不可访问的请求或文件返回 404，状态冲突返回 409。请求恢复遇到会话占用时附 `code: session_busy` 和 `session`。重跑成功返回 201 和新请求 `id`，其余请求动作成功返回 `status: ok`。

`restore` 只恢复已完成成果，`release` 只清理已结束记录。重投操作编号重放返回原状态，不重复发送，也不把失败或未确认变成成功。没有新工作提交、队列或旧接口兼容别名。认证和宿主权限见[安全模型](management-security.md)。
