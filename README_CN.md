# WeClaw

把个人微信连接到本机 Codex 的专用桥接器。

当前产品边界、目标架构与分阶段路线见 [`docs/product-plan.md`](docs/product-plan.md)。

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
weclaw start
```

首次启动会显示微信二维码。也可以先显式登录：

```bash
weclaw login
weclaw start
```

生产运行只由 systemd 用户服务托管；`start` 始终以前台方式运行，不再创建 daemon 或 PID 文件。运行状态、排空、停止和重启只通过当前用户私有的 `~/.weclaw/control.sock` 完成：

```bash
weclaw status
weclaw stop
weclaw restart
```

升级不再使用 `update`、`upgrade` 或本机切换脚本。发布版本与本地构建都进入同一个可回滚事务：

```bash
weclaw deploy v2.5.0
weclaw deploy --binary /absolute/path/to/weclaw --expect-version v2.5.0-local.1
```

部署会验证候选版本、平台和 SHA-256，排空旧服务，停机后快照全部运行状态，离线迁移，原子安装，并让新进程先以排空模式完成健康验收。只有版本、Codex、微信监控和同步游标都就绪后才恢复队列；失败会同时还原二进制、systemd 单元、配置和任务状态。部署成功后发送纯文字微信通知并写入 `~/.weclaw/deployments/<事务>/receipt.json`。

首次从 v2.5 跨到 v2.6 必须安排维护窗口：管理面从回环 TCP 破坏性迁移到当前用户私有的 Unix socket，代码不保留兼容客户端。先用当前已安装的 v2.5 二进制排空并停止，完整备份二进制、systemd 单元和状态，再执行 v2.6 离线迁移并启动。完成这一次切换后，后续版本重新统一使用事务 `weclaw deploy`。具体步骤见 [破坏性迁移文档](docs/migration-v2.md)。

## 配置

配置文件位于 `~/.weclaw/config.json`：

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

语音简报与语音回答共用严格的有序提供商链，支持 1–4 个 `piper` 或 `mimo` 提供商。程序从 `providers` 第一项开始调用；单项失败或超时后自动尝试下一项，全部失败时一次性返回每个提供商的原因，结果显示实际提供商 ID。Piper 返回 WAV，MiMo 返回 MP3；共享发送层通过 FFmpeg 统一压缩为 24 kHz 单声道 MP3，再走微信官方支持的文件消息链路发送。每次成功请求会先发送一张包含实际播报稿、当前项目和实际提供商的阅读卡图片，再发送内容完全一致的 MP3；不追加低信息量状态卡。专用单卡不会显示单页页码和 100% 进度装饰。图片与音频会先整批上传到微信 CDN，全部成功后才按顺序对用户可见，避免第二项上传失败时留下孤立图片。这样即使文件消息没有微信原生语音转文字，用户仍能直接扫读图片。图片渲染是成对交付的硬性条件，因此 `voice.enabled` 要求同时启用 `visual.enabled`。个人微信 iLink Bot 会静默丢弃出站原生 `VOICE`，即使接口返回成功也不会出现语音气泡，因此原生 SILK 出站链已彻底删除，不再制造伪成功。发送仍必须带当前会话令牌。

`piper` 完全离线运行，直接启动固定可执行文件和 ONNX 模型生成 WAV；参数不经过 Shell，因此正文不能注入命令。`length_scale` 越大语速越慢，允许 0.5–2。推荐使用 `zh_CN-huayan-medium` 中文模型。

仓库提供隔离安装器 `./scripts/install-piper.sh`。它要求系统已有 `uv` 和 FFmpeg，只把 Piper 1.4.1、缺失的 CLI 依赖和中文模型写入 `~/.weclaw/tts/piper`，完成后输出共享 FFmpeg 路径和三个 Piper 路径；不会修改系统 Python。WeClaw 不分发声音模型，部署者必须自行检查所选模型卡和数据集许可；示例 `huayan` 的上游模型卡目前把数据集许可标为 Unknown。

`mimo` 通过 `/chat/completions` 调用 MiMo V2.5 TTS。密钥可以写入私有配置，也可以用 `WECLAW_MIMO_API_KEY` 注入到所有 MiMo 提供商。模型固定为 `mimo-v2.5-tts`；可选音色为 `冰糖`、`茉莉`、`苏打`、`白桦`、`Mia`、`Chloe`、`Milo`、`Dean`。旧的扁平 `voice.base_url`、`voice.api_key`、`voice.model`、`voice.voice`、提供商级 `piper.ffmpeg_command` 以及更早的 `voice.command` 均已删除并会被拒绝。

视觉操作卡片默认开启，并提供五套完整模板：`刊物`使用纸张、中文衬线和克制红，`构筑`使用平面石材、几何秩序和建筑色，`黑标`使用高对比黑白与香槟金，`可爱`使用奶油纸、圆润轮廓和柔和色块，`简洁`使用高留白、细线与纯粹信息秩序。五者各自拥有独立的控制卡和阅读卡版式，不是简单换色。发送“回答方式”进入统一偏好中心，或直接发送“视觉风格”预览并切换。回答方式与视觉风格按微信账号隔离，共同保存到严格 v1 的 `~/.weclaw/preferences.json`，重启后继续生效；旧 `visual-styles.json` 不再读取。

每套模板仍按服务本地时区自动切换外观：07:00–18:59 使用明亮主题，19:00–06:59 使用低刺激深色主题。控制卡会根据内容长度把状态组织成两列或三列，把短操作组织成双列。界面只保留品牌、实际内容、必要状态、数字操作和有效提示；不显示主题名称、渲染时间、装饰计数、巨型水印或仿点击箭头。程序会自动发现 Playwright 管理的 Chromium 或系统 Google Chrome；也可以用 `visual.browser_command` 指定浏览器可执行文件的绝对路径。Snap Chromium 因私有挂载无法稳定访问受保护的渲染目录，会被明确拒绝。功能开启但找不到可用浏览器时，服务拒绝启动并给出安装提示。

支持以下环境变量覆盖：

- `WECLAW_SAVE_DIR`
- `WECLAW_CODEX_COMMAND`
- `WECLAW_CODEX_MODEL`
- `WECLAW_VISUAL_BROWSER`
- `WECLAW_MIMO_API_KEY`

配置使用严格解码。旧的 `default_agent`、`agents`、`type`、`args`、`endpoint` 和别名配置会导致启动失败，不做兼容转换。

## 微信交互

公开入口只有一个 `/`。空闲主菜单固定为“项目、会话、任务中心、更多功能”四项；任务运行时变为“当前任务、任务中心、当前会话、更多功能”。会话、任务、自动化、素材和交付列表每页展示 6 条，支持数字与“下一页/上一页”。菜单 revision、页码和待输入状态会跨进程重启保留，10 分钟后自动失效；失效后的数字、返回和翻页会明确拒绝，绝不会进入 Codex。

不打开菜单也可以直接说：

- `新建会话 叫登录排障`
- `搜索会话 登录`
- `切换会话 登录`
- `重命名当前会话 为发布检查`
- `归档当前会话`
- `项目`、`切换项目 weclaw`、`快捷任务`
- `会话列表`、`当前会话`、`运行中心`
- `回答方式`、`开启语音模式`、`关闭语音模式`、`阅读模式`、`自适应模式`
- `视觉风格`、`切换风格 刊物`、`切换风格 构筑`、`切换风格 黑标`、`切换风格 可爱`、`切换风格 简洁`
- `状态`、`任务中心`、`暂停队列`、`继续队列`、`清空队列`、`取消`
- `自动化`、`素材箱`、`交付记录`
- `语音简报`、`发语音`、`发个语音`、`来段语音`、`播报一下`、`读给我听`
- `远程锁定`；锁定后用 `解锁 解锁码` 恢复

会话名称支持精确、前缀、包含和按字符顺序的模糊补全。明确说“切换会话”且唯一匹配时直接执行；普通浏览和搜索结果始终先打开详情。详情可以切换或归档非当前会话，归档与任务取消都要求二次确认。当前会话详情可以直接进入重命名、切换和归档。选择态收到其他普通文字时立即退出，文字原样进入自然语言控制层或 Codex，不会被菜单吞掉。

个人微信 iLink Bot 的音频属于会话回复能力，必须复用当前入站消息的 `context_token`。“发语音”等说法生成一次语音简报；“开启语音模式”会让后续 Codex 回答持续使用图片与 MP3 成对交付，直到发送“关闭语音模式”。原生语音气泡和脱离对话的空令牌主动发送均不受通道支持。

主控制台始终显示当前 WeClaw 版本与项目。“运行中心”展示启动时长、主动发送是否启用、Codex 协议、模型、项目目录、进程号，以及 App Server 推送的主/次额度使用率。

“任务中心”统一展示等待、执行、发送和最近终态任务。文字、图片和文件会先完整落盘，再按全局 FIFO 由唯一 Coordinator 串行执行；忙碌时可以连续排队，不存在单条暂存槽。队列项固定使用创建时的项目、会话、回答方式和视觉风格，之后切换界面不会串任务。详情按状态提供移到最前、删除、暂停、继续、取消、重试或取回冻结文字，且不展示正文、附件名、账号 ID 或私有路径。

服务重启时，未开始任务继续等待；执行中任务变为人工处理中断，发送中任务保留冻结结果但不会自动重复 Codex 或整批发送。明确发送失败与“可能已有部分内容可见”会进入不同恢复路径。

创建、切换、重命名、归档和恢复完成后，成功卡片仍可继续进入当前详情、列表或会话中心，不必重新发送 `/`。此时直接发送普通内容仍会退出短期操作态并正常进入 Codex。

“自动化中心”运行不依赖模型的 Git、systemd 与 HTTP 检查。计划可按天或分钟运行，可选择每次、仅异常、仅变化、异常或变化时主动通知；微信详情页还可以立即手动检查，但不能修改配置。

纯链接在 Linkhoard 保存成功后同步进入“链接素材”。Codex 交付物会在 turn 临时目录删除前复制到私有交付库，可在“交付记录”查看并再次发送。发送图片并写“批注图片”“标注这张图”或“在图上标注”会进入图片批注模式，要求 Codex 生成新的 PNG 交付物，不覆盖原图。

回答方式分为三种：默认“自适应”以文字发送短回复，达到 `visual.long_reply_min_runes` 后改为阅读卡；“阅读”始终优先生成阅读卡，不受自适应阈值与 `visual.long_replies` 开关影响；“语音”把短回答的同一份正文做成阅读卡和 MP3，超过 2200 个字符时先发送完整阅读卡，再发送明确标注的语音节选卡与 MP3。阅读卡最多 10 页并沿用当前视觉风格和自动昼夜主题。30 分钟内回复“文字版”即可取回完整可复制原文。生成、渲染或整批预上传在任何内容可见前失败时，语音模式降级为完整阅读卡，再失败则发送完整文字；发送阶段响应不确定时不会盲目重复正文。

旧斜杠命令已经全部删除。除单独的 `/` 外，以 `/` 开头的内容不会执行，也不会发送给 Codex，只提示打开菜单。

自然语言控制先由确定性 Intent Registry 唯一解析，再进入系统、任务、项目、会话、偏好、素材、自动化或安全控制器。规范化后的短语或前缀发生冲突时注册直接失败；没有匹配的文字仍原样进入 Codex。控制器只返回经过校验的 `ActionResult`，微信投递、任务入队、重试、冻结文字、归档媒体和语音简报统一由 Presenter 执行。旧的跨消息副作用 map、内存菜单和跨领域大路由已经删除。修改状态或产生外部交付的动作必须保存稳定微信来源回执，重复消息采用保守的 at-most-once 语义，不会再次执行。

## 图片、文件和主动发送

- 微信图片会在确认入队前下载到任务私有目录，并在执行时作为 Codex `localImage` 输入。每条最多 4 张，单张最大 20 MiB，支持 JPEG、PNG、GIF 和 WebP。
- PDF、日志、补丁、压缩包和常见源码作为不可信文件交给 Codex；桥接器不会执行或自动解压。
- Codex 写入本次 turn 专属 `outbox` 的交付物会自动上传回微信。不会解析回复中的任意本机绝对路径。
- 已发送交付物会复制到 `~/.weclaw/deliveries`，通过“交付记录”再次发送；临时 `inbox/outbox` 仍按 turn 删除。
- 成功或取消后立即删除任务正文、令牌和临时 `inbox/outbox`；失败或中断负载最多保留 24 小时供人工恢复。
- 菜单、会话、状态、确认和错误等控制结果由固定本机模板渲染为 PNG；不会执行 Codex 返回的任意 HTML。

主动发送：

```bash
weclaw send-token --caller local-cli
weclaw send --caller local-cli --to "绑定者-id" --text "构建完成"
```

显式启用 `127.0.0.1:18011` 回环监听后的请求示例：

```bash
curl -X POST http://127.0.0.1:18011/api/send \
  -H "Authorization: Bearer $WECLAW_SEND_TOKEN" \
  -H 'Idempotency-Key: release-2026-08-05' \
  -H 'Content-Type: application/json' \
  -d '{"caller_id":"local-cli","target_owner":"绑定者-id","text":"构建完成"}'
```

主动发送 API 默认关闭，并且不会创建任何 TCP 监听。`weclaw send-token` 在离线状态生成 256 位随机令牌，只显示一次明文；可写入配置的部分仅包含 SHA-256 哈希。`weclaw send` 只从调用方 secret 管理器预先注入的 `WECLAW_SEND_TOKEN` 环境值读取明文。回环模式必须显式设置 `send_api.enabled`、回环 `listen_addr`，并为每个 caller 配置 `send:text` 和/或 `send:media` 最小权限。非回环监听必须启用 `proxy_mode`、填写规范化可信代理 CIDR，并由用户自己的 TLS 反向代理提供外层加密；WeClaw 只校验直接对端，不相信转发的客户端地址。每个请求必须带 caller、已绑定微信所有者和幂等键；文字最多 8,000 字符，媒体只能是一个公网 HTTPS URL。媒体下载不继承环境代理，拒绝私网、链路本地地址与 DNS 重绑定，大小上限为 25 MiB。24 小时严格回执 `~/.weclaw/send-api-state.json` 只保存哈希、caller、时间和结果。

## 安全边界

- Codex turn 固定使用 `approvalPolicy: never` 和 `dangerFullAccess`。这意味着绑定者能够驱动 Codex 操作本机，必须只绑定你信任的个人微信。
- 会话索引 `~/.weclaw/session-index.json` 使用 v3 严格格式，为每个项目保存独立当前会话。
- 任务队列 `~/.weclaw/tasks/index.json` 与每项 `request.json/result.json` 使用严格 v1 格式；旧 `task-history.json` 已删除。目录、正文、附件、令牌和冻结结果均为私有状态，索引只保留脱敏元数据。
- `~/.weclaw/preferences.json` 使用 v1 严格格式，按绑定者保存回答方式与视觉风格；未知字段、非法枚举和尾随数据都会拒绝启动。
- 所有领域 JSON 状态统一通过 `statefile` 原子内核读写：文件 `0600`、目录 `0700`，拒绝符号链接与超限内容，并在目录同步失败时恢复旧文件。运行服务与离线迁移持有互斥状态锁。
- 账号凭据使用严格 v1；部署离线迁移会一次性加入 `version: 1`。未知字段、错误文件名或损坏凭据会明确阻止启动，不再静默跳过。
- `~/.weclaw/control-state.json` 使用严格 v1 revision 保存可跨重启菜单和 24 小时最小控制回执；显示正文、提示词、路径、附件名、令牌和 `context_token` 不会写入。
- 健康、排空、恢复和部署通知只存在于当前用户拥有的 `0600` Unix socket；TCP 不存在 `/health` 或 `/admin/*`，主动发送未显式启用时也没有 TCP 监听。
- 主动发送 token 只保存 SHA-256 哈希，并按 caller 分配文字/媒体权限。幂等回执不保存正文、目标绑定者、URL、原始幂等键或明文 token。
- `security.remote_lock_code` 启用远程锁；锁定会取消当前任务并暂停该绑定者队列，解锁后仍需显式继续，避免旧任务突然恢复。
- 会话列表先读取 Codex 全局 thread，再只保留本地所有权索引中的 ID，避免暴露其他 Codex 客户端历史。
- 终端原始输出、命令文本、diff 和环境变量不会作为进度消息发送到微信。
- 运行日志使用 SHA-256 派生的稳定短标签代替微信用户/机器人原始 ID，只记录消息长度，不记录问题或回答预览。
- 视觉渲染禁用页面脚本和外部网络，使用严格 CSP；每次发送后立即删除私有 HTML、浏览器 profile 和 PNG。

## 开发验证

```bash
go test ./...
go test -race ./statefile ./taskqueue ./messaging ./ilink ./api ./cmd
go vet ./...
```

详细文档：

- [会话管理](docs/session-management.md)
- [微信长任务进度](docs/wechat-progress.md)
- [文件、素材、交付物与自动化](docs/attachments-and-reports.md)
- [微信视觉操作卡片](docs/visual-controls.md)
- [v2 破坏性迁移](docs/migration-v2.md)
- [微信交互验收清单](docs/acceptance.md)
- [v2.5 持久任务队列与安全部署规格](docs/v2.5-task-queue.md)
- [v2.6 可靠控制面与状态内核规划](docs/v2.6-control-plane.md)
- [v2.6 统一状态内核实现](docs/v2.6-state-kernel.md)
- [v2.6 类型化控制路由实现](docs/v2.6-control-routing.md)
- [v2.6 持久交互与控制回执实现](docs/v2.6-persistent-control.md)
- [v2.6 本机管理面与主动发送安全](docs/v2.6-api-security.md)

## License

MIT。保留原项目版权与提交历史。
