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
3. 能在目标工作目录运行 `codex app-server --listen stdio://`。
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
  "visual": {
    "enabled": true,
    "browser_command": "",
    "long_replies": true,
    "long_reply_min_runes": 900
  },
  "codex": {
    "command": "codex",
    "cwd": "/absolute/path/to/project",
    "model": "",
    "env": {}
  },
  "scheduled_reports": []
}
```

`command` 只能是 Codex 可执行文件，不接受额外协议参数；程序固定追加 `app-server --listen stdio://`。`cwd` 为空时使用 `~/.weclaw/workspace`，设置时必须是绝对路径。`model` 为空时使用用户现有 Codex 默认配置。

视觉操作卡片默认开启，并按服务本地时区自动切换外观：07:00–18:59 使用明亮暖白主题，19:00–06:59 使用低刺激深色主题。控制卡会根据内容长度把状态组织成两列或三列，把短操作组织成双列。界面只保留品牌、实际内容、必要状态、数字操作和有效提示；不显示主题名称、渲染时间、装饰计数、巨型水印或仿点击箭头。程序会自动发现 Playwright 管理的 Chromium 或系统 Google Chrome；也可以用 `visual.browser_command` 指定浏览器可执行文件的绝对路径。Snap Chromium 因私有挂载无法稳定访问受保护的渲染目录，会被明确拒绝。功能开启但找不到可用浏览器时，服务拒绝启动并给出安装提示。

支持以下环境变量覆盖：

- `WECLAW_API_ADDR`
- `WECLAW_SAVE_DIR`
- `WECLAW_CODEX_COMMAND`
- `WECLAW_CODEX_CWD`
- `WECLAW_CODEX_MODEL`
- `WECLAW_VISUAL_BROWSER`

配置使用严格解码。旧的 `default_agent`、`agents`、`type`、`args`、`endpoint` 和别名配置会导致启动失败，不做兼容转换。

## 微信交互

公开入口只有一个 `/`。发送后返回为手机设计的视觉操作卡片，内容随当前任务和会话变化，回复数字即可继续。会话和定时巡检列表每页展示 6 条，可以回复数字，也可以直接说“下一页”“上一页”。会话列表点选后先展示状态和经过清洗的提示摘要，不会立即切换；从详情返回时保留原搜索词和页码。需要输入的卡片会额外附一条短文字提示；菜单 10 分钟后自动失效，失效后的数字仍作为普通内容交给 Codex。

不打开菜单也可以直接说：

- `新建会话 叫登录排障`
- `搜索会话 登录`
- `切换会话 登录`
- `重命名当前会话 为发布检查`
- `归档当前会话`
- `会话列表`、`当前会话`、`运行中心`、`工作目录`
- `状态`、`取消`
- `定时巡检`、`报告计划`

会话名称支持精确、前缀、包含和按字符顺序的模糊补全。明确说“切换会话”且唯一匹配时直接执行；普通浏览和搜索结果始终先打开详情。详情可以切换或归档非当前会话，归档与任务取消都要求二次确认。当前会话详情可以直接进入重命名、切换和归档。选择态收到其他普通文字时立即退出，文字原样进入自然语言控制层或 Codex，不会被菜单吞掉。

主控制台始终显示当前 WeClaw 版本。“运行中心”集中展示桥接器启动时长、本地 API 监听地址，以及 Codex App Server 的协议、模型、工作目录和进程号，并提供刷新和工作目录管理入口。

“任务记录”按绑定者保留最近 20 次 Codex turn，展示安全首行摘要、开始/结束时间、用时，以及运行中、完成、失败、取消或重启中断状态。不会保存回答正文、终端输出、附件名或任何私有路径。

创建、切换、重命名、归档和恢复完成后，成功卡片仍可继续进入当前详情、列表或会话中心，不必重新发送 `/`。此时直接发送普通内容仍会退出短期操作态并正常进入 Codex。

配置 `scheduled_reports` 后，主菜单自动出现只读“定时巡检”入口。列表展示今日发送状态和最近下次运行，详情展示计划、时区、上次发送、项目目录、systemd 服务与健康端点；多计划同样按 6 条分页，并保留进入详情前的页码。微信端不允许修改调度配置或强制执行巡检。

达到 `visual.long_reply_min_runes` 的 Codex 回复会被安全解析为标题、段落、列表、引用和代码块，最多生成 10 张高密度移动阅读卡片。阅读卡沿用相同的自动昼夜主题，每页提高正文容量并根据实际内容计算高度，减少连续图片数量和空白区域。30 分钟内回复“文字版”即可取回完整可复制原文。“文字版”始终由本机控制层消费：缓存过期或不存在时明确提示，不会启动 Codex，也不会误执行旧菜单。内容过长、渲染器不可用、渲染失败或上传失败时自动回退完整文字，不丢失回答。

旧斜杠命令已经全部删除。除单独的 `/` 外，以 `/` 开头的内容不会执行，也不会发送给 Codex，只提示打开菜单。

## 图片、文件和主动发送

- 微信图片会下载到每次 turn 的私有目录，并作为 Codex `localImage` 输入。每条最多 4 张，单张最大 20 MiB，支持 JPEG、PNG、GIF 和 WebP。
- PDF、日志、补丁、压缩包和常见源码作为不可信文件交给 Codex；桥接器不会执行或自动解压。
- Codex 写入本次 turn 专属 `outbox` 的交付物会自动上传回微信。不会解析回复中的任意本机绝对路径。
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
- 会话索引 `~/.weclaw/session-index.json` 使用 v2 严格格式、原子替换和 `0600` 权限。
- 任务记录 `~/.weclaw/task-history.json` 使用严格格式、原子替换、`0600` 权限和每个绑定者最多 20 条的硬限制。
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
- [文件、交付物与定时巡检](docs/attachments-and-reports.md)
- [微信视觉操作卡片](docs/visual-controls.md)
- [微信交互验收清单](docs/acceptance.md)

## License

MIT。保留原项目版权与提交历史。
