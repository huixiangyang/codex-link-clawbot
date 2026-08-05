# WeClaw

把个人微信连接到本机 Codex 的专用桥接器。

本 fork 只支持 **Codex App Server**。不存在通用 Agent 层、Claude/Gemini/Kimi 接入、传统 ACP、`codex exec` CLI 回退、OpenAI-compatible HTTP Agent、多 Agent 路由或自动探测。

> 本项目不是微信官方项目，也不隶属于腾讯或 OpenAI。微信接入参考 `@tencent-weixin/openclaw-weixin` 的公开实现，仅用于个人学习和自用。

## 工作方式

```text
绑定者微信
    │ iLink 长轮询 / CDN
    ▼
WeClaw 消息处理器
    │ 稳定 App Server JSON-RPC
    ▼
codex app-server --listen stdio://
    │
    ├── 持久化 thread / turn
    ├── 本机代码与工具
    └── 图片、文件与交付物
```

- 只接受扫码凭据中绑定者发来的消息，其他联系人和群聊消息直接拒绝。
- 所有普通消息都进入 Codex，不存在 Agent 选择、别名或广播。
- 每个微信绑定者拥有独立的会话所有权索引和当前会话。
- App Server 握手失败时服务直接退出，不提供 echo 或备用模型。

## 前置要求

1. Go 1.25 或更高版本。
2. 已安装 `codex`，并完成 Codex 登录。
3. 已创建至少一个项目目录，并能在其中运行 `codex app-server --listen stdio://`。
4. 已安装非 Snap 版 Chromium，用于视觉操作卡片。推荐由 Playwright 管理：

```bash
npx playwright install chromium
```

## 安装

直接从当前 `main` 构建：

```bash
go install github.com/huixiangyang/weclaw@main
go install github.com/huixiangyang/weclaw/cmd/weclaw-silk-encoder@main
weclaw start
```

首次启动会显示微信二维码。也可以先显式登录：

```bash
weclaw login
weclaw start
```

后台管理：

```bash
weclaw status
weclaw stop
weclaw restart
```

## 配置

配置文件位于 `~/.weclaw/config.json`：

```json
{
  "api_addr": "127.0.0.1:18011",
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
      "health_url": "http://127.0.0.1:18011/health",
      "quick_tasks": [
        {"id": "review", "name": "审查改动", "prompt": "审查当前改动并运行必要测试"}
      ]
    }
  ],
  "automations": [
    {
      "id": "daily",
      "name": "每日检查",
      "project_id": "weclaw",
      "daily_at": "09:00",
      "timezone": "Asia/Shanghai",
      "notify_on": "anomaly_or_change",
      "checks": ["git", "service", "health"],
      "commit_lookback_hours": 24
    }
  ],
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
  "security": {"remote_lock_code": "change-this-code"},
  "voice": {
    "enabled": true,
    "ffmpeg_command": "/usr/bin/ffmpeg",
    "silk_command": "/usr/local/bin/weclaw-silk-encoder",
    "providers": [
      {
        "id": "local",
        "type": "piper",
        "timeout_seconds": 30,
        "piper": {
          "command": "/opt/weclaw/piper/venv/bin/piper",
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
          "api_key": "",
          "model": "mimo-v2.5-tts",
          "voice": "茉莉",
          "style_prompt": "用自然、克制、清晰的语气播报，语速稍慢，重点明确。"
        }
      }
    ]
  }
}
```

`projects` 是 Codex 唯一允许进入的目录白名单；ID 必须稳定且唯一，根目录必须是已存在的干净绝对路径。会话和快捷任务均按项目隔离。`codex.command` 只接受 Codex 可执行文件，程序固定追加 `app-server --listen stdio://`；旧 `codex.cwd` 已删除并会被严格拒绝。`model` 为空时沿用 Codex 默认配置。

自动化可以设置 `daily_at`，或改用 5–1440 的 `every_minutes`，两者必须且只能设置一个。`notify_on` 支持 `always`、`anomaly`、`change`、`anomaly_or_change`；`checks` 支持 `git`、`service`、`health`。

语音简报使用严格的有序提供商链，支持 1–4 个 `piper` 或 `mimo` 提供商。程序从 `providers` 第一项开始调用；单项失败或超时后自动尝试下一项，全部失败时一次性返回每个提供商的原因，受理提示会显示实际提供商 ID。提供商音频统一通过 FFmpeg 转为 16 kHz 单声道 PCM，再由独立 `weclaw-silk-encoder` 进程编码为腾讯 SILK V3，以 `VOICE=4` 上传并作为微信原生语音条发送。发送必须带当前会话令牌，接口受理不再被描述为客户端已送达。

`piper` 完全离线运行，直接启动固定可执行文件和 ONNX 模型生成 WAV；参数不经过 Shell，因此正文不能注入命令。`length_scale` 越大语速越慢，允许 0.5–2。推荐使用 `zh_CN-huayan-medium` 中文模型。SILK 编码放在独立进程中，编码器故障不会拖垮主桥接服务。

仓库提供隔离安装器 `./scripts/install-piper.sh`。它要求系统已有 `uv` 和 FFmpeg，只把 Piper 1.4.1、缺失的 CLI 依赖和中文模型写入 `~/.weclaw/tts/piper`，完成后输出共享 FFmpeg 路径和三个 Piper 路径；不会修改系统 Python。`weclaw-silk-encoder` 必须与主程序同时安装。WeClaw 不分发声音模型，部署者必须自行检查所选模型卡和数据集许可；示例 `huayan` 的上游模型卡目前把数据集许可标为 Unknown。

`mimo` 通过 `/chat/completions` 调用 MiMo V2.5 TTS。密钥可以写入私有配置，也可以用 `WECLAW_MIMO_API_KEY` 注入到所有 MiMo 提供商。模型固定为 `mimo-v2.5-tts`；可选音色为 `冰糖`、`茉莉`、`苏打`、`白桦`、`Mia`、`Chloe`、`Milo`、`Dean`。旧的扁平 `voice.base_url`、`voice.api_key`、`voice.model`、`voice.voice`、提供商级 `piper.ffmpeg_command` 以及更早的 `voice.command` 均已删除并会被拒绝。

`weclaw update` 只支持 Linux amd64/arm64。更新时会从同一个不可变发布中下载主程序与 `weclaw-silk-encoder`，按 `checksums.txt` 分别校验，并先安装编码器再替换主程序，避免只升级一半。

视觉操作卡片默认开启，并提供五套完整模板：`刊物`使用纸张、中文衬线和克制红，`构筑`使用平面石材、几何秩序和建筑色，`黑标`使用高对比黑白与香槟金，`可爱`使用奶油纸、圆润轮廓和柔和色块，`简洁`使用高留白、细线与纯粹信息秩序。五者各自拥有独立的控制卡和阅读卡版式，不是简单换色。发送“视觉风格”或从主菜单进入即可预览并切换；选择按微信账号隔离，保存到严格 v1 的 `~/.weclaw/visual-styles.json`，重启后继续生效。

每套模板仍按服务本地时区自动切换外观：07:00–18:59 使用明亮主题，19:00–06:59 使用低刺激深色主题。控制卡会根据内容长度把状态组织成两列或三列，把短操作组织成双列。界面只保留品牌、实际内容、必要状态、数字操作和有效提示；不显示主题名称、渲染时间、装饰计数、巨型水印或仿点击箭头。程序会自动发现 Playwright 管理的 Chromium 或系统 Google Chrome；也可以用 `visual.browser_command` 指定浏览器可执行文件的绝对路径。Snap Chromium 因私有挂载无法稳定访问受保护的渲染目录，会被明确拒绝。功能开启但找不到可用浏览器时，服务拒绝启动并给出安装提示。

支持以下环境变量覆盖：

- `WECLAW_API_ADDR`
- `WECLAW_SAVE_DIR`
- `WECLAW_CODEX_COMMAND`
- `WECLAW_CODEX_MODEL`
- `WECLAW_VISUAL_BROWSER`
- `WECLAW_MIMO_API_KEY`

配置使用严格解码。旧的 `default_agent`、`agents`、`type`、`args`、`endpoint` 和别名配置会导致启动失败，不做兼容转换。

## 微信交互

公开入口只有一个 `/`。空闲主菜单固定为“项目、会话、最近任务、更多功能”四项；任务运行时变为“任务状态、暂存下一条指令、当前会话、更多功能”。会话、自动化、素材和交付列表每页展示 6 条，支持数字与“下一页/上一页”。菜单 10 分钟后自动失效，失效后的数字仍作为普通内容交给 Codex。

不打开菜单也可以直接说：

- `新建会话 叫登录排障`
- `搜索会话 登录`
- `切换会话 登录`
- `重命名当前会话 为发布检查`
- `归档当前会话`
- `项目`、`切换项目 weclaw`、`快捷任务`
- `会话列表`、`当前会话`、`运行中心`
- `视觉风格`、`切换风格 刊物`、`切换风格 构筑`、`切换风格 黑标`、`切换风格 可爱`、`切换风格 简洁`
- `状态`、`暂存下一条指令`、`清除暂存`、`取消`
- `自动化`、`素材箱`、`交付记录`
- `语音简报`、`发语音`、`发个语音`、`来段语音`、`播报一下`、`读给我听`
- `远程锁定`；锁定后用 `解锁 解锁码` 恢复

会话名称支持精确、前缀、包含和按字符顺序的模糊补全。明确说“切换会话”且唯一匹配时直接执行；普通浏览和搜索结果始终先打开详情。详情可以切换或归档非当前会话，归档与任务取消都要求二次确认。当前会话详情可以直接进入重命名、切换和归档。选择态收到其他普通文字时立即退出，文字原样进入自然语言控制层或 Codex，不会被菜单吞掉。

个人微信 iLink Bot 的语音属于会话回复能力，必须复用当前入站消息的 `context_token`。脱离对话的空令牌主动语音即使接口返回成功也可能被微信静默丢弃，因此程序明确拒绝这种伪成功；直接在微信发送上述任一自然说法即可在同一会话窗口生成语音。

主控制台始终显示当前 WeClaw 版本与项目。“运行中心”展示启动时长、本地 API、Codex 协议、模型、项目目录、进程号，以及 App Server 推送的主/次额度使用率。

“任务记录”按绑定者保留最近 20 次 Codex turn，详情包含项目、会话短编号、开始/结束时间、用时和本轮 token 用量；仍不会保存回答正文、终端输出、附件名或私有路径。运行中发送新的普通文字会暂存为唯一后续指令，当前任务结束后自动执行，不再要求反复重发。

创建、切换、重命名、归档和恢复完成后，成功卡片仍可继续进入当前详情、列表或会话中心，不必重新发送 `/`。此时直接发送普通内容仍会退出短期操作态并正常进入 Codex。

“自动化中心”运行不依赖模型的 Git、systemd 与 HTTP 检查。计划可按天或分钟运行，可选择每次、仅异常、仅变化、异常或变化时主动通知；微信详情页还可以立即手动检查，但不能修改配置。

纯链接在 Linkhoard 保存成功后同步进入“链接素材”。Codex 交付物会在 turn 临时目录删除前复制到私有交付库，可在“交付记录”查看并再次发送。发送图片并写“批注图片”“标注这张图”或“在图上标注”会进入图片批注模式，要求 Codex 生成新的 PNG 交付物，不覆盖原图。

达到 `visual.long_reply_min_runes` 的 Codex 回复会被安全解析为标题、段落、列表、引用和代码块，最多生成 10 张高密度移动阅读卡片。阅读卡沿用当前用户选择的视觉风格和自动昼夜主题，每页提高正文容量并根据实际内容计算高度，减少连续图片数量和空白区域。30 分钟内回复“文字版”即可取回完整可复制原文。“文字版”始终由本机控制层消费：缓存过期或不存在时明确提示，不会启动 Codex，也不会误执行旧菜单。内容过长、渲染器不可用、渲染失败或上传失败时自动回退完整文字，不丢失回答。

旧斜杠命令已经全部删除。除单独的 `/` 外，以 `/` 开头的内容不会执行，也不会发送给 Codex，只提示打开菜单。

## 图片、文件和主动发送

- 微信图片会下载到每次 turn 的私有目录，并作为 Codex `localImage` 输入。每条最多 4 张，单张最大 20 MiB，支持 JPEG、PNG、GIF 和 WebP。
- PDF、日志、补丁、压缩包和常见源码作为不可信文件交给 Codex；桥接器不会执行或自动解压。
- Codex 写入本次 turn 专属 `outbox` 的交付物会自动上传回微信。不会解析回复中的任意本机绝对路径。
- 已发送交付物会复制到 `~/.weclaw/deliveries`，通过“交付记录”再次发送；临时 `inbox/outbox` 仍按 turn 删除。
- turn 结束、失败或发送“取消”后，私有 `inbox/outbox` 整体删除。
- 菜单、会话、状态、确认和错误等控制结果由固定本机模板渲染为 PNG；不会执行 Codex 返回的任意 HTML。

主动发送：

```bash
weclaw send --to "user_id@im.wechat" --text "构建完成"
weclaw send --to "user_id@im.wechat" --media "https://example.com/result.png"
```

本地 API 默认只监听 `127.0.0.1:18011`：

```bash
curl -X POST http://127.0.0.1:18011/api/send \
  -H 'Content-Type: application/json' \
  -d '{"to":"user_id@im.wechat","text":"构建完成"}'
```

## 安全边界

- Codex turn 固定使用 `approvalPolicy: never` 和 `dangerFullAccess`。这意味着绑定者能够驱动 Codex 操作本机，必须只绑定你信任的个人微信。
- 会话索引 `~/.weclaw/session-index.json` 使用 v3 严格格式，为每个项目保存独立当前会话。
- 任务记录 `~/.weclaw/task-history.json` 使用 v2 严格格式；项目选择、自动化、素材交付和远程锁定也使用独立严格状态文件。所有状态均原子替换并使用 `0600` 权限。
- `security.remote_lock_code` 启用远程锁；锁定会取消活动任务、清除暂存，并在解锁前阻止文字、图片和文件进入 Codex。
- 会话列表先读取 Codex 全局 thread，再只保留本地所有权索引中的 ID，避免暴露其他 Codex 客户端历史。
- 终端原始输出、命令文本、diff 和环境变量不会作为进度消息发送到微信。
- 运行日志使用 SHA-256 派生的稳定短标签代替微信用户/机器人原始 ID，只记录消息长度，不记录问题或回答预览。
- 视觉渲染禁用页面脚本和外部网络，使用严格 CSP；每次发送后立即删除私有 HTML、浏览器 profile 和 PNG。

## 开发验证

```bash
go test ./...
go test -race ./codex ./messaging ./session ./config ./reporting ./visual
go vet ./...
```

详细文档：

- [会话管理](docs/session-management.md)
- [微信长任务进度](docs/wechat-progress.md)
- [文件、素材、交付物与自动化](docs/attachments-and-reports.md)
- [微信视觉操作卡片](docs/visual-controls.md)
- [v2 破坏性迁移](docs/migration-v2.md)
- [微信交互验收清单](docs/acceptance.md)

## License

MIT。保留原项目版权与提交历史。
