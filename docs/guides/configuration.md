# 配置指南

配置、微信凭据与用户偏好均保存在 `~/.codex-link-clawbot/data/clawbot.db`，不维护运行期 JSON 配置文件。机器级配置与绑定选择必须停服后修改，下次启动生效；每个绑定的回复偏好可在微信或网页在线修改。

## 修改方式

```bash
codex-link-clawbot config
codex-link-clawbot config set management.public_url https://codex-link.example.com
codex-link-clawbot config workspace set workspace 主项目 /absolute/path/to/project
codex-link-clawbot config workspace remove unused
```

`config` 校验环境变量覆盖后的有效配置，但只输出状态摘要，不含完整工作空间路径、密钥或提供商参数，不能将其输出直接交给 `config apply` 作为备份恢复。`set` 修改单一标量，`workspace set` 按 ID 新增或更新，删除后至少保留一个工作空间。写入先取得独占锁并整体校验，失败不提交。

Linux 用户服务可用 `stop` 排空停服，修改后通过 systemd 启动。前台进程和 macOS 服务按各自方式停止，不要并行运行第二个实例。生命周期见[部署](../operations/deployment.md)。

## 标量设置

以下为 `config set` 接受的键；空字符串可写成 `''`。

| 键 | 默认值 | 约束与含义 |
| --- | --- | --- |
| `codex.command` | `codex` | 可执行命令，不能为空；服务环境建议使用绝对路径 |
| `codex.model` | 空 | 不指定时由 Codex 决定 |
| `management.listen` | `127.0.0.1:18120` | 只能是回环 host:port，可用 `[::1]:18120` 或 `localhost:18120` |
| `management.public_url` | 空 | 对外展示地址，必须 HTTPS，不含用户信息、查询串或片段；不负责创建代理 |
| `security.remote_lock_code` | 空 | 空值关闭锁定功能；启用需 6–64 个单行字符 |
| `progress.enabled` | `true` | 启用输入状态和单次延迟等待提示；不影响真实阶段持久化 |
| `progress.typing_interval` | `8` | 秒，启用时范围 3–30 |
| `progress.first_delay` | `15` | 秒，启用时范围 5–120；从准备阶段计时，每条请求最多提示一次 |
| `visual.enabled` | `true` | 图片菜单及阅读图能力；无法初始化浏览器时启动失败 |
| `visual.browser` | 空 | 自动发现浏览器；指定时须为绝对路径，拒绝 Snap 浏览器 |
| `visual.long_replies` | `true` | 阅读图长答处理开关，不改变新用户的完整文字偏好 |
| `visual.min_runes` | `900` | 长答阈值，视觉与长答开关同时开启时范围 300–5000 |
| `voice.enabled` | `false` | 开启时必须同时存在有效 FFmpeg 和提供商配置 |
| `voice.ffmpeg` | 空 | FFmpeg 的规范绝对路径 |

默认工作空间 ID 为 `workspace`，目录为 `~/.codex-link-clawbot/workspace`。ID 匹配 `[a-z][a-z0-9_-]{0,31}`，名称单行、去除首尾空白后非空且最多 40 字，ID 和名称分别唯一。路径必须是规范绝对路径；会话归属还会按实际目录解析校验。

启动时会检查目录是否真实可用。仅配置一个且 ID 为 `workspace` 时，应用会尝试创建该目录；其他目录需提前准备，配置校验通过不等于目录、权限或项目已就绪。

## 整体配置输入

`config apply` 从标准输入接收 schema 7 的 JSON 协议，最大 4 MiB，拒绝未知字段。它从默认配置构造新对象后整体替换机器配置，不是合并补丁；省略字段恢复默认值，省略工作空间会恢复默认工作空间。JSON 只是输入协议，不会另存一份 JSON 状态。

以下示例会替换当前配置，路径须先改成实际项目路径，已有提供商和环境变量也必须一并保留：

```bash
codex-link-clawbot config apply <<'JSON'
{
  "schema_version": 7,
  "codex": {"command": "codex", "model": "", "env": {}},
  "codex-link-clawbot": {
    "project_entries": [
      {"id": "workspace", "name": "主项目", "root": "/absolute/path/to/project"}
    ],
    "reply": {
      "progress": {"enabled": true, "typing_interval_seconds": 8, "first_message_delay_seconds": 15},
      "visual": {"enabled": false, "browser_command": "", "long_replies": true, "long_reply_min_runes": 900},
      "voice": {"enabled": false, "ffmpeg_command": "", "providers": []}
    },
    "security": {"remote_lock_code": ""},
    "management": {"listen": "127.0.0.1:18120", "public_url": ""}
  }
}
JSON
```

含密钥的输入应通过受保护的标准输入传递，避免写进 Shell 历史、聊天或日志；不要在版本库中保存真实配置。配置 schema 7 是输入结构版本，不等于 SQLite schema 版本。

## 语音提供商

通过整体配置设置 `reply.voice`，启用时要求 1–4 个按顺序尝试的提供商。每个提供商需要唯一 `id`、`type` 和 5–180 秒的 `timeout_seconds`；只能包含与类型相符的配置对象。

| 类型 | 配置对象与字段 |
| --- | --- |
| `piper` | `piper.command`、`model`、`model_config` 均为规范绝对路径；`length_scale` 范围 0.5–2 |
| `mimo` | `mimo.base_url`、`api_key`、`model`、`voice`，以及可选 `style_prompt` |

MiMo 地址要求 HTTPS（仅回环测试地址可 HTTP），不含账号、查询串和片段；密钥非空且单行。当前代码只接受模型 `mimo-v2.5-tts`，音色为冰糖、茉莉、苏打、白桦、Mia、Chloe、Milo、Dean，风格说明最多 500 字。这是本项目当前适配器的限制，不代表提供商全部能力。

FFmpeg 与 Piper 由部署者预先安装，Piper 的模型及模型配置属于外部资产。配置校验不等于真实合成成功，启用后需按[验收](../operations/acceptance.md)检查 MP3 生成和微信发送。

## 环境覆盖

以下非空环境变量在加载时覆盖持久配置，不回写数据库：

| 环境变量 | 覆盖项 |
| --- | --- |
| `CODEX_LINK_CLAWBOT_CODEX_COMMAND` | Codex 可执行命令 |
| `CODEX_LINK_CLAWBOT_CODEX_MODEL` | Codex 模型 |
| `CODEX_LINK_CLAWBOT_VISUAL_BROWSER` | 浏览器绝对路径 |
| `CODEX_LINK_CLAWBOT_MANAGEMENT_LISTEN` | 管理监听地址 |
| `CODEX_LINK_CLAWBOT_MANAGEMENT_PUBLIC_URL` | 展示用公网地址 |
| `CODEX_LINK_CLAWBOT_MIMO_API_KEY` | 已配置 MiMo 提供商的密钥 |

`codex.env` 只传给 Codex 子进程；上表读取的是桥接服务自身环境。MiMo 环境覆盖不会创建提供商，保存启用语音的配置仍需通过完整字段校验。服务环境与交互终端不同，应在所属服务中设置必要变量。

字段与验证的源码入口为 [config.go](../../internal/app/config/config.go)、[标量与 SQLite 映射](../../internal/app/config/sqlite.go)和 [CLI 写入](../../internal/cli/config_write.go)。
