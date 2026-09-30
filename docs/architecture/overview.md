# 架构总览

## 两个入口，一套内核

```text
WeChat owner ── iLink ── bridge.Handler ── request.Store
                                              │
                                    execution.Coordinator
                                              │ Executor 接口
                                    bridge.taskExecutor ── Codex App Server
                                              │
                                    冻结结果 / 微信交付 / 回执

Browser ── HTTPS proxy/Tunnel ── loopback console ── target/thread/request/preference/runtime stores

Local CLI ── owner-only Unix socket ── lifecycle controller
```

微信入口对普通内容先做会话准入，空闲则立即执行，忙碌则拒绝并展示会话菜单，将数字输入交给独立的菜单控制器。菜单按展示时的 ID 调用相同目标、线程、请求与偏好服务，正常只发送一张菜单图，失败时回退文字。Web 管理台调用显式 JSON API 修改本机状态。部署、停止和健康检查仍使用私有 Unix socket，避免把机器生命周期协议混入公网管理 API。

## 关键包

| 包 | 责任 |
| --- | --- |
| `internal/bridge` | 所有者校验、数字菜单与分页、媒体接收、即时准入、结果呈现 |
| `internal/execution` | 与传输协议无关的会话准入、互斥、共享状态查询、取消仲裁与阶段报告 |
| `internal/management` | 嵌入式管理页面、令牌认证、管理 API、Unix 生命周期端点 |
| `internal/request` | 输入、冻结结果、执行状态、投递回执、恢复与保留 |
| `internal/target` | 持久化会话意图与原子目标选择 |
| `internal/businessmigration` | 备份、暂存验证和可恢复的离线业务迁移 |
| `internal/thread` | Codex 全局线程目录、可信归属和显式建会话/续接 |
| `internal/workspace` | 受信任目录白名单；旧选择仅为迁移来源 |
| `internal/preference` | 回答模式与视觉风格 |
| `internal/delivery` | 待阅通知；旧文件库已删除，旧记录类型仅在 request 离线迁移代码中 |
| `internal/runtimecontrol` | 健康、排空、恢复和运行指标 |
| `internal/codex/appserver` | 唯一 Codex 协议适配器 |
| `internal/app` | 进程组合根和生命周期 |

## 不变量

1. 启动只启用一个明确选定的绑定，只有该所有者的完成态私聊消息能提交执行。
2. 先登记可取消的准备阶段并即时启动执行器，回执、附件下载与校验不阻塞消息入口；原生执行必须等附件完整校验后开始，来源键保证微信重投不会重复执行。
3. 工作空间、会话意图和回复偏好在接收时固定；之后的管理台切换只影响新请求。
4. 每个会话同时只有一次活动执行，不同会话可独立执行；没有等待任务或自动领取逻辑。
5. 管理 TCP 服务只能绑定回环地址，所有 `/api/*` 请求都必须携带管理令牌。
6. 新数字菜单每页固定编号到业务 ID；十分钟后或重启时会话失效。菜单收件记录在副作用前持久化，防止微信整批重投重复操作；旧多位编号解析器和控制状态文件不参与运行时。
7. `execution` 不依赖 `bridge`、`ilink`、`management` 或具体 App Server 适配器；架构检查阻止反向依赖。
8. 排空检查与会话准入共用临界区；排空返回后不再接收新工作。
9. 执行器在冻结结果或提交终态前调用 `finalize`。以指定轮次的实际终态确认停止，无法确认则保留未知状态；即使已请求取消，上游实际完成也保留成果。进入收尾后拒绝取消。

## 装配与生命周期

`app.Run` 校验启动输入，显式传递 `Options.StateRoot` 下的领域存储路径，再装配微信与管理入口。`bridge.NewRuntime` 一次性连接 Handler、执行适配器、客户端注册表和协调器，不暴露依赖 setter，也不保留旧 `bridge.Coordinator`。

`serviceGroup` 管理两个 HTTP 服务、请求协调器、微信监听器、每分钟保留清理和 Codex 退出观察器。启动失败、后台服务异常退出和父 context 取消都经过统一清理：取消子 context，等待所有循环退出，再停止 Codex 子进程。HTTP 服务最多等待在途请求 5 秒，超时关闭连接。

CLI 的配置、凭据发现仍由原有本机目录约定负责；`Options.StateRoot` 是应用内部装配参数，不是新增 CLI 配置项。请求索引为 v4、输入清单为 v2、结果格式为 v2，并新增 targets.json；已有实例必须执行[离线迁移](../operations/migration.md)。

## 线程语义

Codex 是真实线程的所有者。工作空间只是本机允许微信进入的路径集合。目标线程只决定下一条微信请求送往哪里；如果没有线程，接收首先保存稳定意图，协调器创建线程后解析该意图。准备输入与执行期间都拒绝该会话的第二条指令；迟到的线程解析不会覆盖用户新选择。忙碌拒绝回执在回复前持久化，微信和恢复操作重投不能补执行；各会话的结果回执显示请求编号与工作空间。

本轮分析与取舍见[架构调整记录](refactoring.md)，开发命令见[开发指南](../guides/development.md)。

## 结果与恢复

`tasks/<id>/result.json` 和 `outbox` 是生产结果的唯一来源。冻结成功即记录不可逆的执行完成事实；微信发送回执单独记录。成功、发送失败和发送不确定的回答都按 7 天留存，输入在终态后 24 小时清理，元数据保留 30 天。

取消校验所有者与精确请求编号。重跑创建带 `retry_of` 的新请求；重投先记录操作编号，再发送已有结果。进程重启不会自动重跑或重投。恢复需要近期真实微信消息上下文，下载不依赖微信连接。

API 以 `/api/requests`、`/api/conversations`、`/api/target` 为业务入口，详情与下载共享令牌认证和所有者隔离。旧队列动作、线程入口和交付列表 API 已删除。完整接口与存储设计见[业务设计](../../specs/business-reset/design.md)。

本轮收尾：完成事实与回答检查点先于产物归档持久化；归档失败只恢复保存，不重跑工作。请求详情可清理内容和记录名额，轻量来源回执保持去重。目录短缓存不参与执行准入；网页目录复用状态摘要，避免逐条 RPC。详见[收尾设计](../../specs/realtime-completion/design.md)。
