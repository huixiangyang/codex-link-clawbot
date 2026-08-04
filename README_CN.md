# WeClaw

[English](README.md)

微信 AI Agent 桥接器 — 将微信消息接入 AI Agent（Claude、Codex、Gemini、Kimi 等）。

> 本项目参考 [@tencent-weixin/openclaw-weixin](https://npmx.dev/package/@tencent-weixin/openclaw-weixin) 实现，仅限个人学习，勿做他用。

|                                                 |                                                 |                                                 |
| :---------------------------------------------: | :---------------------------------------------: | :---------------------------------------------: |
| <img src="previews/preview1.png" width="280" /> | <img src="previews/preview2.png" width="280" /> | <img src="previews/preview3.png" width="280" /> |

## 快速开始

```bash
# 一键安装
curl -sSL https://raw.githubusercontent.com/fastclaw-ai/weclaw/main/install.sh | sh

# 启动（首次运行会弹出微信扫码登录）
weclaw start
```

就这么简单。首次启动时，WeClaw 会：

1. 显示二维码 — 用微信扫码登录
2. 自动检测已安装的 AI Agent（Claude、Codex、Gemini 等）
3. 保存配置到 `~/.weclaw/config.json`
4. 开始接收和回复微信消息

使用 `weclaw login` 可以添加更多微信账号。

### 其他安装方式

```bash
# 通过 Go 安装
go install github.com/fastclaw-ai/weclaw@latest

# 通过 Docker
docker run -it -v ~/.weclaw:/root/.weclaw ghcr.io/fastclaw-ai/weclaw start
```

## 架构

<p align="center">
  <img src="previews/architecture.png" width="600" />
</p>

**Agent 接入模式：**

| 模式 | 工作方式                                                         | 支持的 Agent                                            |
| ---- | ---------------------------------------------------------------- | ------------------------------------------------------- |
| ACP  | 长驻子进程，通过 stdio JSON-RPC 通信。速度最快，复用进程和会话。 | Claude, Codex, Kimi, Gemini, Cursor, OpenCode, OpenClaw |
| CLI  | 每条消息启动一个新进程，支持通过 `--resume` 恢复会话。           | Claude (`claude -p`)、Codex (`codex exec`)              |
| HTTP | OpenAI 兼容的 Chat Completions API。                             | OpenClaw（HTTP 回退）                                   |

同时存在 ACP 和 CLI 时，自动优先选择 ACP。

## 聊天命令

在微信中发送以下命令：

| 命令                    | 说明                     |
| ----------------------- | ------------------------ |
| `你好`                  | 发送给默认 Agent         |
| `/codex 写一个排序函数` | 发送给指定 Agent         |
| `/cc 解释一下这段代码`  | 通过别名发送             |
| `/claude`               | 切换默认 Agent 为 Claude |
| `/cwd /path/to/project` | 切换工作目录             |
| `/status`               | 查看当前任务状态         |
| `/cancel`               | 取消当前任务             |
| `/info`                 | 查看当前 Agent 信息      |
| `/sessions [页码]`      | 查看当前用户的会话列表   |
| `/session`              | 查看当前 Codex 会话      |
| `/session new [名称]`   | 创建并切换到新会话       |
| `/session use <短编号>` | 切换会话                 |
| `/session rename <名称>` | 重命名当前会话          |
| `/session archive [短编号]` | 归档会话             |
| `/sessions archived [页码]` | 查看已归档会话       |
| `/session restore <短编号>` | 恢复已归档会话       |
| `/help`                 | 查看帮助信息             |

### 快捷别名

| 别名   | Agent    |
| ------ | -------- |
| `/cc`  | Claude   |
| `/cx`  | Codex    |
| `/cs`  | Cursor   |
| `/km`  | Kimi     |
| `/gm`  | Gemini   |
| `/ocd` | OpenCode |
| `/oc`  | OpenClaw |

也可以在配置文件中为每个 Agent 自定义触发命令：

```json
{
  "agents": {
    "claude": {
      "type": "acp",
      "aliases": ["ai", "c"]
    }
  }
}
```

然后 `/ai 你好` 或 `/c 你好` 就会路由到 claude。

切换默认 Agent 会写入配置文件，重启后仍然生效。

### Codex 会话管理

Codex App Server 会话现在使用显式、持久化的 thread。WeClaw 只在 `~/.weclaw/session-index.json` 保存微信用户与 thread 的归属关系以及当前选择；名称、预览、工作目录、时间和运行状态仍以 Codex 为准。索引使用私有文件权限，即使提供完整 thread ID，其他微信用户也不能打开不属于自己的会话。

当前会话在服务重启后继续生效。列表使用 thread ID 末尾生成的稳定 8 位短编号，并展示执行中、等待确认、空闲、未加载或异常状态。任务运行时仍可读取 `/session` 和 `/sessions`；创建、切换、重命名、归档和恢复会被忙碌保护拦截，直到当前 turn 结束。

旧 `/new` 和 `/clear` 已删除，统一使用 `/session new`。旧版内存映射不会自动兼容导入；Codex 历史仍保留在磁盘，但只有明确写入当前微信用户归属索引后才会展示。

状态模型、命令行为、重启规则和安全边界见 [Codex 会话管理](docs/session-management.md)。

### 长任务进度与并发保护

Codex App Server 模式会把阶段说明、计划更新和工具活动转换成微信端进度：任务开始后持续刷新“正在输入”，超过首条延迟后发送文字状态。单次任务内相同的文字详情只发送一次；没有新状态时仅刷新“正在输入”，不会重复推送旧详情。终端原始输出不会转发到微信。

同一个微信用户同一时间只能运行一个 Codex turn。任务进行中再次发消息时，WeClaw 会立即返回当前状态和已运行时间，不会创建并发 turn，也不会覆盖原任务的事件通道。当前消息不会排队，任务结束后需要重新发送。

任务运行期间仍可使用 `/status` 查询当前阶段，或使用 `/cancel` 中断本次 Codex turn。取消命令会调用 Codex App Server 的 `turn/interrupt`，取消后的迟到结果和错误不会继续推送到微信。

详细机制、配置和本机部署方式见 [微信长任务进度桥接](docs/wechat-progress.md)。

## 富媒体消息

WeClaw 支持微信图片、文件和语音输入，并支持向微信发送图片、视频和文件。

**微信图片输入：** Codex App Server 模式会下载并解密微信图片，把文字和图片作为同一个多模态 turn 发送给 Codex。纯图片消息会自动补充图片分析指令，不需要配置 `save_dir`。单条消息最多 4 张、单张最大 20 MiB，仅接受 JPEG、PNG、GIF 和 WebP；临时文件使用私有权限，并在任务完成或取消后删除。不支持图片输入的 Agent 会明确返回错误，不会静默忽略图片。

**微信文件输入：** 可以直接发送 PDF、ZIP/TAR/GZip、日志、补丁以及常见源代码文件。单条消息最多 8 个文件、单个最大 50 MiB，图片与文件合计最大 100 MiB。文件进入 `~/.weclaw/turns/turn-*/inbox` 私有目录，Codex 会收到文件名、类型、大小和绝对路径；文件被视为不可信数据，不会由桥接器执行或自动解压。任务完成或取消后整个 turn 目录删除。

**语音消息：** 在微信中发送语音消息时，WeClaw 会自动使用微信的语音转文字功能，将转写后的文本发送给 AI Agent。重复的语音消息事件会自动去重。

**Agent 回复自动处理：** 当 AI Agent 返回包含图片的 markdown（`![](url)`）时，WeClaw 会自动提取图片 URL，下载文件，上传到微信 CDN（AES-128-ECB 加密），然后作为图片消息发送。

**交付物自动回传：** 每个 turn 都会为 Agent 提供独立的 `outbox` 路径。Codex 把最终报告、补丁、压缩包或其他受支持文件写入该目录后，WeClaw 会自动上传并发送到微信，再在最终文字中列出成功和失败的附件。旧的“从回复中提取任意绝对路径”机制已删除，工作区文件不会被误发。单次最多回传 8 个文件、单个最大 50 MiB、总计最大 100 MiB。

**Markdown 转换：** Agent 的回复会自动从 markdown 转为纯文本再发送 — 代码块去掉围栏、链接只保留文字、加粗斜体标记去除等。

## 主动推送消息

无需等待用户发消息，主动向微信用户推送消息。

**命令行：**

```bash
# 发送文本
weclaw send --to "user_id@im.wechat" --text "你好，来自 weclaw"

# 发送图片
weclaw send --to "user_id@im.wechat" --media "https://example.com/photo.png"

# 发送文本 + 图片
weclaw send --to "user_id@im.wechat" --text "看看这个" --media "https://example.com/photo.png"

# 发送文件
weclaw send --to "user_id@im.wechat" --media "https://example.com/report.pdf"
```

**HTTP API**（`weclaw start` 运行时，默认监听 `127.0.0.1:18011`）：

```bash
# 发送文本
curl -X POST http://127.0.0.1:18011/api/send \
  -H "Content-Type: application/json" \
  -d '{"to": "user_id@im.wechat", "text": "你好，来自 weclaw"}'

# 发送图片
curl -X POST http://127.0.0.1:18011/api/send \
  -H "Content-Type: application/json" \
  -d '{"to": "user_id@im.wechat", "media_url": "https://example.com/photo.png"}'

# 发送文本 + 媒体
curl -X POST http://127.0.0.1:18011/api/send \
  -H "Content-Type: application/json" \
  -d '{"to": "user_id@im.wechat", "text": "看看这个", "media_url": "https://example.com/photo.png"}'
```

支持的媒体类型：图片（png、jpg、gif、webp）、视频（mp4、mov）、文件（pdf、doc、zip 等）。

设置 `WECLAW_API_ADDR` 环境变量可更改监听地址（如 `0.0.0.0:18011`）。

## 定时项目巡检

WeClaw 可以每天主动向扫码绑定者发送确定性巡检报告，内容直接采集自 Git、systemd 和 HTTP 健康端点，不经过 Agent 推断。报告包含当前分支、未提交改动数、与上游的领先/落后、指定时间窗口内的最近提交、用户服务状态和健康检查响应。

```json
{
  "scheduled_reports": [
    {
      "name": "项目日报",
      "daily_at": "09:00",
      "timezone": "Asia/Shanghai",
      "project_dir": "/absolute/path/to/project",
      "service_name": "weclaw.service",
      "health_url": "http://127.0.0.1:18011/health",
      "commit_lookback_hours": 24
    }
  ]
}
```

所有字段必填且启动时严格校验。到达设定时间后，每个报告会发送给所有已登录账号的绑定者；当天发送状态保存在 `~/.weclaw/scheduled-reports-state.json`，服务重启不会重复推送，错过时间后当天首次启动会补发。删除数组中的配置即可停用，不存在隐式默认任务。

完整的文件安全边界、产物协议和调度行为见 [微信文件、交付物与定时巡检](docs/attachments-and-reports.md)。

## 配置

配置文件路径：`~/.weclaw/config.json`

```json
{
  "default_agent": "claude",
  "progress": {
    "enabled": true,
    "typing_interval_seconds": 8,
    "first_message_delay_seconds": 15,
    "message_interval_seconds": 45
  },
  "scheduled_reports": [
    {
      "name": "项目日报",
      "daily_at": "09:00",
      "timezone": "Asia/Shanghai",
      "project_dir": "/home/user/my-project",
      "service_name": "weclaw.service",
      "health_url": "http://127.0.0.1:18011/health",
      "commit_lookback_hours": 24
    }
  ],
  "agents": {
    "claude": {
      "type": "acp",
      "command": "/usr/local/bin/claude-agent-acp",
      "env": {
        "ANTHROPIC_API_KEY": "sk-ant-xxx"
      },
      "model": "sonnet"
    },
    "codex": {
      "type": "acp",
      "command": "/usr/local/bin/codex-acp",
      "env": {
        "OPENAI_API_KEY": "sk-xxx"
      }
    },
    "openclaw": {
      "type": "http",
      "endpoint": "https://api.example.com/v1/chat/completions",
      "api_key": "sk-xxx",
      "model": "openclaw:main"
    }
  }
}
```

环境变量：

- `WECLAW_DEFAULT_AGENT` — 覆盖默认 Agent
- `OPENCLAW_GATEWAY_URL` — OpenClaw HTTP 回退地址
- `OPENCLAW_GATEWAY_TOKEN` — OpenClaw API Token

自定义 agent cli 环境变量

```json
{
  "default_agent": "...",
  "agents": {
    "...": {
      ...
      "env": {
        "ENV_NAME": "ENV_VALUE"
      }
    },
  }
}
```

### 权限配置

部分 Agent 默认需要交互式权限确认，在微信场景下无法操作会导致卡住。可通过 `args` 配置跳过：

| Agent | 参数 | 说明 |
|-------|------|------|
| Claude (CLI) | `--dangerously-skip-permissions` | 跳过所有工具权限确认 |
| Codex (CLI) | `--skip-git-repo-check` | 允许在非 git 仓库目录运行 |

配置示例：

```json
{
  "claude": {
    "type": "cli",
    "command": "/usr/local/bin/claude",
    "cwd": "/home/user/my-project",
    "args": ["--dangerously-skip-permissions"]
  },
  "codex": {
    "type": "cli",
    "command": "/usr/local/bin/codex",
    "cwd": "/home/user/my-project",
    "args": ["--skip-git-repo-check"]
  }
}
```

通过 `cwd` 指定 Agent 的工作目录（workspace）。不设置则默认为 `~/.weclaw/workspace`。

> **注意：** 这些参数会跳过安全检查，请了解风险后再启用。ACP 模式的 Agent 会自动处理权限，无需配置。

## 后台运行

```bash
# 启动（默认后台运行）
weclaw start

# 查看状态
weclaw status

# 停止
weclaw stop

# 前台运行（调试用）
weclaw start -f
```

日志输出到 `~/.weclaw/weclaw.log`。

### 系统服务（开机自启）

**macOS (launchd)：**

```bash
cp service/com.fastclaw.weclaw.plist ~/Library/LaunchAgents/
launchctl load ~/Library/LaunchAgents/com.fastclaw.weclaw.plist
```

**Linux (systemd)：**

```bash
sudo cp service/weclaw.service /etc/systemd/system/
sudo systemctl enable --now weclaw
```

## Docker

```bash
# 构建
docker build -t weclaw .

# 登录（交互式，扫描二维码）
docker run -it -v ~/.weclaw:/root/.weclaw weclaw login

# 使用 HTTP Agent 启动
docker run -d --name weclaw \
  -v ~/.weclaw:/root/.weclaw \
  -e OPENCLAW_GATEWAY_URL=https://api.example.com \
  -e OPENCLAW_GATEWAY_TOKEN=sk-xxx \
  weclaw

# 查看日志
docker logs -f weclaw
```

> 注意：ACP 和 CLI 模式需要容器内有对应的 Agent 二进制文件。
> 默认镜像只包含 WeClaw 本体。如需使用 ACP/CLI Agent，请挂载二进制文件或构建自定义镜像。
> HTTP 模式开箱即用。

## 发版

```bash
# 打 tag 触发 GitHub Actions 自动构建发版
git tag v0.1.0
git push origin v0.1.0
```

自动构建 `darwin/linux/windows` x `amd64/arm64` 的二进制，创建 GitHub Release 并上传所有产物和校验文件。

## 更新

```bash
# 更新到最新版本（运行中会自动重启）
weclaw update

# 查看当前版本
weclaw version
```

## 开发

```bash
# 热重载
make dev

# 编译
go build -o weclaw .

# 运行
./weclaw start
```

## 贡献者

<a href="https://github.com/fastclaw-ai/weclaw/graphs/contributors">
  <img src="https://contrib.rocks/image?repo=fastclaw-ai/weclaw" />
</a>

## Star 趋势

[![Star History Chart](https://api.star-history.com/svg?repos=fastclaw-ai/weclaw&type=Timeline)](https://star-history.com/#fastclaw-ai/weclaw&Timeline)

## 许可证

[MIT](LICENSE)
