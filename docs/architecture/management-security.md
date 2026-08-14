# 本机管理面安全

## 唯一管理边界

codex-link-clawbot 不创建管理 TCP 监听，也不提供通用主动发送 API。健康、排空、恢复和部署提交固定通过当前用户私有的 `~/.codex-link-clawbot/control.sock`；外部系统不能借 codex-link-clawbot 向微信任意发送文字或媒体。

历史上的共享 TCP `api_addr`、`CODEX_LINK_CLAWBOT_API_ADDR`、`start --api-addr`、`codex-link-clawbot.send_api`、`codex-link-clawbot send`、`codex-link-clawbot send-token` 和主动发送回执均已删除。配置 v6 不接受这些字段，运行时没有旧接口兼容、双写或降级分支。

## Unix socket 约束

服务启动后创建 `~/.codex-link-clawbot/control.sock`：

- 状态根目录必须是当前用户拥有的真实 `0700` 目录。
- socket 必须是当前用户拥有的真实 Unix socket，权限固定为 `0600`。
- CLI 在创建客户端和每次拨号前都重新校验路径类型、所有者和权限。
- 已存活 socket 不会被覆盖；只有当前用户拥有且无法连接的旧 socket 才能作为残留项删除。
- `GET /health`、`POST /admin/drain`、`POST /admin/resume` 与类型化部署通知只注册在该 socket。
- 不存在 TCP `/health`、`/admin/*` 或 `/api/send`。

部署通知只接受 `from_version`、`to_version` 和 `service` 三项短元数据。运行中服务生成固定正文，为全部绑定者写入待阅通知并返回 `deferred`；部署器不能提交自定义正文、图片、文件、目标绑定者或外部 URL。

## 待阅通知

没有新微信消息时不发送任何内容。以下两条确定性路径只能写入严格 v1 `~/.codex-link-clawbot/pending-notices.json`：

1. 部署事务在新版本完成切换后，经 Unix socket 提交类型化完成事件。
2. 长任务结果或失败说明无法确定交付时，协调器写入只含安全摘要的恢复提醒。

待阅状态不保存 `context_token`。绑定者下一次发来携带有效上下文的消息时，最多合并四条补送；明确失败继续保留，响应不确定则按可能可见处理并删除，避免重复。两条路径都不能接受远程自由文本、自由命令、自由文件路径或自由收件人。

## 验收

自动化覆盖 Unix socket 类型、所有者、权限、残留 socket、管理方法、排空与恢复、类型化通知字段、待阅通知持久化以及 TCP 监听缺失。运维验证使用：

```bash
codex-link-clawbot status
go test ./...
go test -race ./internal/management ./internal/config ./internal/cli ./internal/bridge
go vet ./...
```

不要使用 TCP `curl /health` 判断运行状态；该路由不存在。
