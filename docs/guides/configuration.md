# 配置与设置

## 两类设置

codex-link-clawbot 明确区分机器级配置与绑定者偏好：

| 类型 | 内容 | 修改位置 | 生效方式 |
| --- | --- | --- | --- |
| 机器级配置 | Codex 命令、工作空间、浏览器、语音提供商与密钥 | 本机 `~/.codex-link-clawbot/config.json` | 校验后重启或部署 |
| 绑定者偏好 | 当前工作空间、目标 Codex 线程、回答方式、视觉风格 | 微信“呈现与安全”或对应上下文 | 立即生效并持久化 |

微信端不会改写目录、命令或 API 密钥。发送 `/` 后回复 `43` 进入“呈现与安全”，可以修改个人回复偏好；脱敏后的有效配置只在 `42`“系统健康与诊断”中查看。本机也可以执行：

```bash
codex-link-clawbot config
```

## 文件与解析规则

主配置位于 `~/.codex-link-clawbot/config.json`，最大 4 MiB。当前唯一结构版本是 `schema_version: 5`，顶层只允许 `schema_version`、`codex` 和 `codex-link-clawbot`。未知字段、旧扁平字段、尾随内容、非法枚举和越界值都会让运行时启动失败。

下面是完整结构：

```json
{
  "schema_version": 5,
  "codex": {
    "command": "codex",
    "model": "",
    "env": {}
  },
  "codex-link-clawbot": {
    "project_entries": [
      {
        "id": "codex-link-clawbot",
        "name": "codex-link-clawbot",
        "root": "/absolute/path/to/codex-link-clawbot"
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
	"security": {
	  "remote_lock_code": "change-this-code"
	}
  }
}
```

## Codex

`codex.command` 是唯一智能体可执行文件。codex-link-clawbot 固定追加 `app-server --listen stdio://`，不接受自定义 Agent 类型、参数路由、`codex exec` 回退或 HTTP-compatible Agent。

`codex.model` 为空时使用 Codex 自身默认配置；微信端选择的线程模型优先级更高，只影响该线程后续轮次。`codex.env` 只传递明确列出的环境变量。

## Codex 工作空间

`codex-link-clawbot.project_entries` 至少包含一项。`id` 只能使用小写字母开头的字母、数字、下划线和连字符，最长 32 字符；`name` 必须唯一；`root` 必须是干净绝对路径。

工作空间是微信端可以发现、接管和执行 Codex 工作的受信任目录边界，不是 Codex 项目对象。它只包含 `id`、`name` 和 `root`，不承载服务、健康地址、监控或计划任务。全局线程可跨工作空间浏览；codex-link-clawbot 请求和交付仍按入队时冻结或声明的工作空间隔离。

## 微信回复

`codex-link-clawbot.reply` 统一管理从等待提示到最终交付的体验：

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
        "command": "/opt/codex-link-clawbot/piper/bin/piper",
        "model": "/opt/codex-link-clawbot/piper/voices/zh_CN-huayan-medium.onnx",
        "model_config": "/opt/codex-link-clawbot/piper/voices/zh_CN-huayan-medium.onnx.json",
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

MiMo 密钥可以改由 `CODEX_LINK_CLAWBOT_MIMO_API_KEY` 注入。Piper 可使用 `scripts/install-piper.sh` 安装到隔离目录；声音模型许可由部署者自行确认。

## 安全

`codex-link-clawbot.security.remote_lock_code` 只用于绑定者远程锁定，必须是 6–64 个字符的单行值。

codex-link-clawbot 不提供通用主动发送 TCP API。健康、排空、恢复和部署提交只通过当前用户私有的 Unix socket；部署完成和长任务恢复结果只进入待阅状态。边界见 [本机管理面安全](../architecture/management-security.md)。

## 环境变量

| 环境变量 | 覆盖字段 |
| --- | --- |
| `CODEX_LINK_CLAWBOT_CODEX_COMMAND` | `codex.command` |
| `CODEX_LINK_CLAWBOT_CODEX_MODEL` | `codex.model` |
| `CODEX_LINK_CLAWBOT_VISUAL_BROWSER` | `codex-link-clawbot.reply.visual.browser_command` |
| `CODEX_LINK_CLAWBOT_MIMO_API_KEY` | 所有 MiMo 提供商的 `api_key` |

环境覆盖发生在严格解码之后、完整校验之前。密钥优先放入进程 secret 环境，不要提交到仓库。

## 破坏性升级

`codex-link-clawbot deploy` 会在服务切换前使用候选二进制离线校验配置和状态。配置必须已经是严格 v5；无版本、v2、v3、v4、旧品牌键和旧扁平字段都直接拒绝，不会被自动转换。当前命名空间内的控制状态与交付库仍按各自 schema 执行破坏性迁移；失败时由事务快照整体恢复。完整边界见[破坏性更名与状态边界](../operations/migration.md)。
