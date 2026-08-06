# 配置参考

## 文件与解析规则

主配置位于 `~/.weclaw/config.json`，最大 4 MiB，使用严格 JSON 解码。未知字段、旧字段、尾随内容、非法枚举和越界值都会让启动失败；运行时不做兼容迁移。

下面是一个可工作的基础结构：

```json
{
  "send_api": {"enabled": false},
  "save_dir": "",
  "progress": {
    "enabled": true,
    "typing_interval_seconds": 8,
    "first_message_delay_seconds": 15,
    "message_interval_seconds": 45
  },
  "projects": [
    {
      "id": "weclaw",
      "name": "WeClaw",
      "root": "/absolute/path/to/weclaw",
      "service_name": "weclaw.service",
      "health_url": "http://127.0.0.1:18011/health"
    }
  ],
  "automations": [],
  "visual": {
    "enabled": true,
    "browser_command": "",
    "long_replies": true,
    "long_reply_min_runes": 900
  },
  "codex": {
    "command": "codex",
    "model": "",
    "env": {}
  },
  "security": {
    "remote_lock_code": "change-this-code"
  },
  "voice": {
    "enabled": false,
    "ffmpeg_command": "",
    "providers": []
  }
}
```

## 项目白名单

`projects` 至少包含一项。`id` 只能使用小写字母开头的字母、数字、下划线和连字符，最长 32 字符；`name` 必须唯一；`root` 必须是干净绝对路径。

`service_name` 和 `health_url` 只定义确定性自动化可以检查的边界，不会成为自由 Shell 或自由 URL。线程、轮次、提示词模板和交付均按项目隔离。

## Codex

`codex.command` 是唯一智能体可执行文件。WeClaw 固定追加 `app-server --listen stdio://`，不接受自定义 Agent 类型、参数路由、`codex exec` 回退或 HTTP-compatible Agent。

`model` 为空时使用 Codex 自身默认配置。`env` 只传递明确列出的环境变量。

## 视觉回复

- `enabled`：启用固定模板到 PNG 的渲染；语音能力要求它为 `true`。
- `browser_command`：可选的浏览器绝对路径；为空时发现 Playwright Chromium 或系统 Chrome。
- `long_replies`：只控制自适应模式是否将长回复渲染为阅读图。
- `long_reply_min_runes`：允许 300–5000，默认 900。

Snap Chromium 会被拒绝。阅读模式不受 `long_replies` 与阈值影响，始终优先渲染阅读图。视觉与投递细节见 [视觉回复](visual-replies.md)。

## 语音提供商

语音启用后需要一个 FFmpeg 绝对路径和 1–4 个有序提供商。失败会按顺序尝试下一项，不会并发调用。

```json
{
  "voice": {
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
}
```

MiMo 密钥可以改由 `WECLAW_MIMO_API_KEY` 注入。Piper 可使用 `scripts/install-piper.sh` 安装到隔离目录；声音模型许可由部署者自行确认。

## 自动化

每项自动化必须设置且只设置 `daily_at` 或 `every_minutes`：

- `daily_at` 使用 `HH:MM`。
- `every_minutes` 允许 5–1440。
- `timezone` 使用 IANA 时区，例如 `Asia/Shanghai`。
- `notify_on` 支持 `always`、`anomaly`、`change`、`anomaly_or_change`。
- `checks` 支持 `git`、`service`、`health`；后两者要求项目配置相应边界。
- `commit_lookback_hours` 允许 1–168。

## 主动发送 API

`send_api.enabled=false` 时不会创建 TCP 监听。启用时必须配置显式 IP 与端口、1–16 个 SHA-256 token，以及每个 caller 的 `send:text` 和/或 `send:media` scope。非回环监听必须同时启用可信代理模式。

安全模型、幂等回执和公网媒体限制见 [本机管理面与主动发送安全](../architecture/management-security.md)。

## 环境变量

| 环境变量 | 覆盖字段 |
| --- | --- |
| `WECLAW_SAVE_DIR` | `save_dir` |
| `WECLAW_CODEX_COMMAND` | `codex.command` |
| `WECLAW_CODEX_MODEL` | `codex.model` |
| `WECLAW_VISUAL_BROWSER` | `visual.browser_command` |
| `WECLAW_MIMO_API_KEY` | 所有 MiMo 提供商的 `api_key` |

环境覆盖发生在严格解码之后、完整校验之前。密钥优先放入进程 secret 环境，不要提交到仓库。
