# 微信持久任务与进度桥接

## 目标

WeClaw 把微信输入与 Codex 执行彻底分离：消息先可靠落盘并获得稳定任务编号，再由进程内唯一 Coordinator 串行消费。长任务继续提供克制的阶段进度，最终答案只进入一次受控投递事务。

## 入站与幂等

1. iLink 长轮询取回一批消息，但在整批可靠消费前不推进同步游标。
2. 控制意图同步处理；普通文字、图片和文件进入任务 staging。
3. 附件下载、内容校验、私有写盘和 `request.json` 同步完成后，staging 原子改名并更新严格任务索引。
4. 只有持久入队成功才向微信确认；磁盘或状态错误保留旧游标供重投。
5. 来源键使用账号标识与 `message_id`，缺失时使用 `seq`。重投命中已有任务，不重复下载附件或创建 Codex turn。

单批前项已成功、后项失败时，`accounts/*.sync.json` 会保存 `pending_cursor` 与已消费消息回执。微信重投整批时跳过前项，直到全部成功才原子提交新游标。

## 附件输入

- 图片与文件都可以在忙碌时排队，不要求任务结束后重发。
- 单条最多 4 张图片，单张最大 20 MiB，只接受内容识别出的 JPEG、PNG、GIF 和 WebP。
- 文件最大 50 MiB，单任务负载最大 100 MiB，全队列附件最大 500 MiB。
- 入站附件保存在 `~/.weclaw/tasks/<task-id>/inbox`，目录 `0700`、文件 `0600`；执行时图片作为 `localImage`，文件作为不可信本机引用。
- 索引只记录数量、总字节和脱敏摘要，不记录附件名、正文、令牌或绝对路径。

## 全局执行协调器

全部项目共用一个 Coordinator，同时最多一个任务处于 `running` 或 `delivering`。它按全局 FIFO 领取未暂停、未锁定且拥有在线客户端的绑定者任务，并严格使用入队时固定的项目、会话、回答方式和视觉风格。

执行流程：

```text
queued
  → running：解析固定项目与 thread，独占 Codex
  → result.json：冻结正文、图片 URL、交付物与哈希
  → delivering：按冻结计划发送
  → succeeded / failed / interrupted
```

执行期间的 `turn/plan/updated` 被压缩为完成步骤数和当前步骤；命令执行与文件修改只产生固定活动标签。每 8 秒刷新“正在输入”，15 秒后检查首条文字进度，之后每 45 秒只推送新阶段。命令文本、输出、diff、环境变量和回答预览不进入进度或日志。

## 任务中心

空闲主菜单为“项目、会话、任务中心、更多功能”，执行中为“当前任务、任务中心、当前会话、更多功能”。也可以直接说“任务中心”“任务队列”“暂停队列”“继续队列”或“清空队列”。

任务中心每页 6 项，展示当前状态、等待数量、暂停状态和最近结果。详情按状态提供：

| 状态 | 操作 |
| --- | --- |
| 等待 | 移到最前、删除 |
| 执行 | 刷新、二次确认取消 |
| 发送 | 刷新 |
| 失败或中断 | 重试、删除；发送中断可取回冻结文字 |
| 完成 | 查看安全摘要 |

“清空队列”二次确认后只删除当前绑定者的等待项。暂停一个绑定者不影响其他绑定者；远程锁定会先持久化锁，再取消当前任务并暂停队列，解锁后不会自动继续。

## 重启与投递恢复

`queued` 在重启后保持等待。启动时发现 `running` 会转为执行中断，发现 `delivering` 会验证冻结结果后转为发送中断，两者都不自动重试。

投递回执区分四种结果：

- 完整成功：进入 `succeeded` 并立即清理私密负载。
- 明确失败且没有可见内容：进入 `failed/delivery_failed`。
- 响应丢失或可能部分可见：进入 `interrupted/delivery_ambiguous`，禁止盲目重发。
- 进程在发送中退出：进入 `interrupted/restart_delivery`，用户可以只取回冻结文字。

成功与取消立即删除请求、`context_token`、inbox 和 result；失败与中断负载最多保留 24 小时，过期后仍保留无正文元数据但不再提供恢复。

## 健康、排空与部署

本机管理面的 `GET /health` 返回 `starting`、`ready`、`draining`、`stopping` 或 `degraded`，以及实际版本、Codex 就绪、微信监控、任务数量和 `drain_complete`。健康、排空、恢复及部署通知只通过当前用户拥有且权限为 `0600` 的 `~/.weclaw/control.sock` 提供；TCP 不再暴露健康或 `/admin/*` 路由。

排空期间继续可靠接收微信消息，但 Coordinator 不领取新任务。`drain_complete` 要求没有运行/发送任务、没有 staging，并且没有待提交同步批次。

生产进程始终由 `weclaw.service` 托管，`start` 只以前台方式运行。普通重启与停止会先排空：

```bash
weclaw status
weclaw restart
weclaw stop
```

事务部署：

```bash
weclaw deploy v2.5.0
weclaw deploy --binary /absolute/path/to/weclaw --expect-version v2.5.0-local.1
```

候选先验证版本、平台和 SHA-256。旧进程排空并停止后才快照和离线迁移；新进程以隐藏的排空启动模式完成版本、Codex、微信和游标验收，再恢复正常 systemd 单元并放行队列。失败同时恢复旧二进制、单元、配置和任务状态。成功后，部署器通过本机管理 socket 提交类型化版本元数据，由运行中服务生成固定纯文字通知并发送给绑定者；随后写入无正文部署收据，并立即销毁包含任务正文、附件或令牌的状态副本。

## 验证

```bash
go test ./...
go test -race ./statefile ./taskqueue ./messaging ./ilink ./api ./cmd
go vet ./...
systemctl --user status weclaw.service
weclaw status
```

真机应连续验证文字、图片和 PDF 排队，项目/会话快照，暂停/继续，取消，执行与发送阶段重启，以及部署通知。任务卡只显示短编号、脱敏摘要、项目和必要状态。
