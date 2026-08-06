# 配置与设置

## 两类设置

WeClaw 明确区分机器级配置与绑定者偏好：

| 类型 | 内容 | 修改位置 | 生效方式 |
| --- | --- | --- | --- |
| 机器级配置 | Codex 命令、项目入口、浏览器、语音提供商、自动化、密钥、网络监听 | 本机 `~/.weclaw/config.json` | 校验后重启或部署 |
| 绑定者偏好 | 当前项目入口、当前 Codex 线程、回答方式、视觉风格 | 微信设置中心 | 立即生效并持久化 |

微信端不会改写目录、命令、API 密钥、服务名、健康地址或监听地址。发送 `/` 后进入 `5 WeClaw · 设置`，可以查看脱敏后的有效配置状态，并修改安全范围内的个人偏好。本机执行以下命令可验证配置并查看同一份脱敏摘要：

```bash
weclaw config
```

## 文件与解析规则

主配置位于 `~/.weclaw/config.json`，最大 4 MiB。当前唯一结构版本是 `schema_version: 2`，顶层只允许 `schema_version`、`codex` 和 `weclaw`。未知字段、旧扁平字段、尾随内容、非法枚举和越界值都会让运行时启动失败。

下面是完整结构：

```json
{
  "schema_version": 2,
  "codex": {
    "command": "codex",
    "model": "",
    "env": {}
  },
  "weclaw": {
    "project_entries": [
      {
        "id": "weclaw",
        "name": "WeClaw",
        "root": "/absolute/path/to/weclaw",
        "service_name": "weclaw.service",
        "health_url": "http://127.0.0.1:18011/health"
      }
    ],
    "reply": {
      "progress": {
        "enabled": true,
        "typing_interval_seconds": 8,
        "first_message_delay_seconds": 15,
        "message_interval_seconds": 45
      },
      "visual": {
        "enabled": true,
        "browser_command": "",
        "long_replies": true,
        "long_reply_min_runes": 900
      },
      "voice": {
        "enabled": false,
        "ffmpeg_command": "",
        "providers": []
      }
    },
    "features": {
      "link_archive": {
        "enabled": false
      },
      "automations": []
    },
    "security": {
      "remote_lock_code": "change-this-code"
    },
    "send_api": {
      "enabled": false
    }
  }
}
```

## Codex

`codex.command` 是唯一智能体可执行文件。WeClaw 固定追加 `app-server --listen stdio://`，不接受自定义 Agent 类型、参数路由、`codex exec` 回退或 HTTP-compatible Agent。

`codex.model` 为空时使用 Codex 自身默认配置；微信端选择的线程模型优先级更高，只影响该线程后续轮次。`codex.env` 只传递明确列出的环境变量。

## WeClaw 项目入口

`weclaw.project_entries` 至少包含一项。`id` 只能使用小写字母开头的字母、数字、下划线和连字符，最长 32 字符；`name` 必须唯一；`root` 必须是干净绝对路径。

项目入口只是允许 Codex 进入的受信任目录，不是 Codex 项目对象。`service_name` 和 `health_url` 只定义确定性自动化可以检查的边界，不会成为自由 Shell 或自由 URL。Codex 线程、WeClaw 请求、提示词模板和交付均按入口隔离。

## 微信回复

`weclaw.reply` 统一管理从等待提示到最终交付的体验：

- `progress` 控制长任务的输入状态与文字进度节奏。
- `visual.enabled` 启用固定模板到 PNG 的渲染；语音能力要求它为 `true`。
- `visual.browser_command` 是可选浏览器绝对路径；为空时发现 Playwright Chromium 或系统 Chrome。
- `visual.long_replies` 只控制自适应模式是否把长回复渲染为阅读图。
- `visual.long_reply_min_runes` 允许 300–5000，默认 900。
- `voice` 配置阅读卡与 MP3 配对交付。

Snap Chromium 会被拒绝。阅读模式不受长回复开关与阈值影响，始终优先渲染阅读图。

### 语音提供商

语音启用后需要一个 FFmpeg 绝对路径和 1–4 个有序提供商。失败会按顺序尝试下一项，不会并发调用。

```json
{
  "enabled": true,
  "ffmpeg_command": "/usr/bin/ffmpeg",
  "providers": [
    {
      "id": "local",
      "type": "piper",
      "timeout_seconds": 30,
      "piper": {
        "command": "/opt/weclaw/piper/bin/piper",
        "model": "/opt/weclaw/piper/voices/zh_CN-huayan-medium.onnx",
        "model_config": "/opt/weclaw/piper/voices/zh_CN-huayan-medium.onnx.json",
        "length_scale": 1
      }
    },
    {
      "id": "mimo",
      "type": "mimo",
      "timeout_seconds": 90,
      "mimo": {
        "base_url": "https://api.xiaomimimo.com/v1",
        "api_key": "replace-with-secret",
        "model": "mimo-v2.5-tts",
        "voice": "茉莉",
        "style_prompt": "用自然、克制、清晰的语气播报。"
      }
    }
  ]
}
```

MiMo 密钥可以改由 `WECLAW_MIMO_API_KEY` 注入。Piper 可使用 `scripts/install-piper.sh` 安装到隔离目录；声音模型许可由部署者自行确认。

## WeClaw 功能

### 链接归档

`weclaw.features.link_archive.enabled=true` 时，`directory` 必须是干净绝对路径。禁用时不得保留目录值。纯链接消息会进入受控归档，不消耗 Codex 轮次。

### 自动化

`weclaw.features.automations` 中每项必须设置且只设置 `daily_at` 或 `every_minutes`：

- `daily_at` 使用 `HH:MM`。
- `every_minutes` 允许 5–1440。
- `timezone` 使用 IANA 时区，例如 `Asia/Shanghai`。
- `notify_on` 支持 `always`、`anomaly`、`change`、`anomaly_or_change`。
- `checks` 支持 `git`、`service`、`health`；后两者要求项目入口配置相应边界。
- `commit_lookback_hours` 允许 1–168。

## 安全与主动发送

`weclaw.security.remote_lock_code` 只用于绑定者远程锁定，必须是 6–64 个字符的单行值。

`weclaw.send_api.enabled=false` 时不会创建 TCP 监听。启用时必须配置显式 IP 与端口、1–16 个 SHA-256 token，以及每个 caller 的 `send:text` 和/或 `send:media` scope。非回环监听必须同时启用可信代理模式。

安全模型、幂等回执和公网媒体限制见 [本机管理面与主动发送安全](../architecture/management-security.md)。

## 环境变量

| 环境变量 | 覆盖字段 |
| --- | --- |
| `WECLAW_SAVE_DIR` | 启用 `weclaw.features.link_archive` 并覆盖 `directory` |
| `WECLAW_CODEX_COMMAND` | `codex.command` |
| `WECLAW_CODEX_MODEL` | `codex.model` |
| `WECLAW_VISUAL_BROWSER` | `weclaw.reply.visual.browser_command` |
| `WECLAW_MIMO_API_KEY` | 所有 MiMo 提供商的 `api_key` |

环境覆盖发生在严格解码之后、完整校验之前。密钥优先放入进程 secret 环境，不要提交到仓库。

## 从扁平配置升级

`weclaw deploy` 会在服务切换前使用候选二进制执行一次离线迁移：旧的 `projects`、`progress`、`visual`、`voice`、`automations`、`save_dir`、`security` 和 `send_api` 被原子重组到 `weclaw`；部署失败时由事务快照恢复。运行时不会读取旧结构，也没有双写或兼容分支。
