# 配置指南

配置文件默认位于 `~/.codex-link-clawbot/config.json`，权限必须为 `0600`。当前只接受 schema 7 和已声明字段。

```json
{
  "schema_version": 7,
  "codex": {
    "command": "codex",
    "model": "",
    "env": {}
  },
  "codex-link-clawbot": {
    "project_entries": [
      {"id": "workspace", "name": "主工作区", "root": "/srv/workspace"}
    ],
    "reply": {
      "progress": {
        "enabled": true,
        "typing_interval_seconds": 8,
        "first_message_delay_seconds": 15
      },
      "visual": {
        "enabled": true,
        "browser_command": "/usr/bin/chromium",
        "long_replies": true,
        "long_reply_min_runes": 900
      },
      "voice": {
        "enabled": false
      }
    },
    "security": {
      "remote_lock_code": ""
    },
    "management": {
      "listen": "127.0.0.1:18120",
      "public_url": "https://codex-link.example.com"
    }
  }
}
```

## 管理面

- `listen` 必须是回环地址和端口，如 `127.0.0.1:18120`、`[::1]:18120` 或 `localhost:18120`。`0.0.0.0`、局域网 IP 和空主机都会被拒绝。
- `public_url` 可省略；填写时必须是 HTTPS，且不能包含账号密码、查询参数或片段。路径允许存在。
- 首次启动自动生成 `~/.codex-link-clawbot/management-token`，权限固定为 `0600`。服务不会把令牌写进 HTML、URL或日志。

可用环境变量：

```text
CODEX_LINK_CLAWBOT_MANAGEMENT_LISTEN
CODEX_LINK_CLAWBOT_MANAGEMENT_PUBLIC_URL
CODEX_LINK_CLAWBOT_CODEX_COMMAND
CODEX_LINK_CLAWBOT_CODEX_MODEL
CODEX_LINK_CLAWBOT_VISUAL_BROWSER
CODEX_LINK_CLAWBOT_MIMO_API_KEY
```

## 工作空间

`project_entries` 是微信可进入的绝对路径白名单。`id` 使用小写字母、数字、点、下划线或短横线；`root` 必须是干净的绝对路径。浏览器只能选择列表中的条目，不能创建任意路径。

## 回复

- `progress` 只跟随 Codex App Server 的真实结构化阶段，不生成循环“仍在执行”。
- `visual.enabled` 启用阅读图；`browser_command` 如填写必须是绝对路径。
- `voice.enabled` 依赖视觉渲染和 FFmpeg，并要求配置 Piper 或 MiMo 提供商。管理台只调整每个绑定者的模式与风格，不暴露提供商密钥。

## 安全

`remote_lock_code` 为空时关闭远程锁定功能；启用时要求 6–64 个单行字符。锁定后微信工作消息不会进入 Codex；可在微信发送 `0`，选择 `1` 进入解锁码输入页，也可在持有管理令牌的页面输入锁定码。

执行以下命令验证并查看脱敏配置：

```bash
codex-link-clawbot config
```
