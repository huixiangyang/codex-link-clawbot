# 本机管理面与主动发送安全

## 已完成边界

当前实现已经删除共享 TCP 服务中的无认证发送、健康和管理路由。默认配置不会创建任何 TCP 监听；健康、排空、恢复和部署通知固定通过 `~/.weclaw/control.sock`。主动发送只有在配置显式启用后才启动独立 TCP 服务。

旧 `api_addr`、`WECLAW_API_ADDR`、`start --api-addr` 以及直接读取微信账号凭据发送的旧 `weclaw send` 均已删除。离线迁移会删除配置中的 `api_addr`、写入关闭状态的 `send_api`，并从 systemd `ExecStart` 中移除旧 `--api-addr` 参数；运行时没有旧接口兼容、双写或降级分支。

## 本机管理面

服务启动后创建 `~/.weclaw/control.sock`：

- 状态根目录必须是当前用户拥有的真实 `0700` 目录。
- socket 必须是当前用户拥有的真实 Unix socket，权限固定为 `0600`。
- CLI 在创建客户端和每次拨号前都重新校验路径类型、所有者和权限。
- 已存活 socket 不会被覆盖；只有当前用户拥有且无法连接的旧 socket 才能作为残留项删除。
- `GET /health`、`POST /admin/drain`、`POST /admin/resume` 与类型化部署通知只注册在该 socket。
- TCP 上的 `/health` 和所有 `/admin/*` 均为 404。

部署通知只接受 `from_version`、`to_version` 和 `service` 三项短元数据。运行中服务生成固定正文并通知全部绑定者；部署器不能借管理面发送任意文字、图片或文件。

## 主动发送配置

默认值是：

```json
"send_api": {"enabled": false}
```

生成 token：

```bash
weclaw send-token --caller local-cli
```

命令在离线状态生成 32 个随机字节，输出一次明文 token 和一个只含 SHA-256 哈希的配置项。明文应立即进入调用方的 secret 管理；不能写入 `config.json`、systemd 单元、命令参数或回执。启用回环发送面的完整结构为：

```json
"send_api": {
  "enabled": true,
  "listen_addr": "127.0.0.1:18011",
  "tokens": [
    {
      "caller_id": "local-cli",
      "token_sha256": "64位小写十六进制SHA-256",
      "scopes": ["send:text", "send:media"]
    }
  ]
}
```

caller 必须稳定且唯一；token 哈希也必须唯一。每个 token 最多声明 `send:text` 和 `send:media` 两项权限。请求同时带文字与媒体时必须同时拥有两项权限。

回环模式只接受直接对端为回环 IP 的请求。非回环后端必须显式设置：

```json
"proxy_mode": true,
"trusted_proxy_cidrs": ["10.20.0.0/16"]
```

此模式只适用于用户自己管理的 TLS 反向代理到 WeClaw 后端。服务按 TCP 直接对端校验规范化 CIDR，不读取 `X-Forwarded-For` 来放宽来源；不在 WeClaw 中提供面向公网的明文 HTTP 或自动证书逻辑。

## 请求协议

```http
POST /api/send
Authorization: Bearer <plaintext-token>
Idempotency-Key: release-2026-08-05
Content-Type: application/json

{
  "caller_id": "local-cli",
  "target_owner": "bound-owner-id",
  "text": "构建完成",
  "media_url": "https://example.com/result.png"
}
```

安全约束：

- `caller_id` 必须与 token 所属 caller 完全一致。
- `target_owner` 必须是当前凭据中已经绑定的微信所有者；不能向任意 iLink ID 发送。
- `Idempotency-Key` 为 16–128 个受限 ASCII 字符；相同 caller 与键只能对应同一请求指纹。
- JSON 总大小不超过 32 KiB，拒绝未知字段和尾随数据；文字不超过 8,000 个 Unicode 字符。
- 媒体只接受一个无凭据、无 fragment 的公网 HTTPS URL，下载上限 25 MiB。
- 公网媒体下载不继承环境代理；每次 DNS 解析后固定拨号已验证的公网地址，拒绝回环、私网、链路本地、未指定地址、DNS 重绑定和超过三次跳转。
- 文字不再隐式提取 Markdown 图片；媒体必须显式声明并经过 `send:media` 权限。

`weclaw send` 现在只是该协议的安全客户端。它从 `WECLAW_SEND_TOKEN` 读取明文，不继承环境 HTTP 代理，自动生成并在结果或错误中返回幂等键，并在代理模式下要求显式 HTTPS `--endpoint`；调用结果不确定时只能带原键重试。它不再读取账号凭据或直接调用微信发送接口。

## 幂等回执

首次请求在任何外部副作用之前把 reservation 原子写入严格 v1 `~/.weclaw/send-api-state.json`。相同键和相同请求返回已有结果，不再次发送；相同键但请求不同返回 HTTP 409。明确失败或响应歧义也保持最多一次，不会因为客户端重试重复显示消息。

回执保存 24 小时，最多 4,096 条，只包含：

- caller ID；
- caller 与幂等键派生的 SHA-256 ID；
- 请求规范化后的 SHA-256 指纹；
- `reserved`、`succeeded` 或 `failed`；
- 创建和更新时间。

回执不保存正文、目标绑定者、URL、原始幂等键、token、附件名或微信会话令牌。文件继续复用统一状态内核的严格 JSON、`0600`、符号链接拒绝、大小限制、原子替换和目录同步。

## 验收

自动化覆盖：默认关闭、监听边界、无认证、错误 token、caller 不匹配、错误 scope、未绑定目标、请求超限、非可信代理、幂等冲突、并发重投、失败重投、进程重启、严格回执 schema、TCP 管理路由缺失、Unix socket 权限以及私网媒体阻断。

运维验证使用：

```bash
weclaw status
go test ./...
go test -race ./internal/api ./internal/config ./internal/cli ./internal/messaging
go vet ./...
```

不要再用 TCP `curl /health` 判断运行状态；该路由已删除。
