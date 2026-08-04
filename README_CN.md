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

支持以下环境变量覆盖：

- `WECLAW_API_ADDR`
- `WECLAW_SAVE_DIR`
- `WECLAW_CODEX_COMMAND`
- `WECLAW_CODEX_CWD`
- `WECLAW_CODEX_MODEL`

配置使用严格解码。旧的 `default_agent`、`agents`、`type`、`args`、`endpoint` 和别名配置会导致启动失败，不做兼容转换。

## 微信命令

| 命令 | 作用 |
| --- | --- |
| `/status` | 查看当前 Codex turn 状态和运行时间 |
| `/cancel` | 调用 `turn/interrupt` 取消当前任务 |
| `/info` | 查看 Codex App Server、模型、PID 和工作目录 |
| `/sessions [页码]` | 查看未归档会话 |
| `/session` | 查看当前会话详情 |
| `/session new [名称]` | 创建并切换会话 |
| `/session use <短编号>` | 切换到已有会话 |
| `/session rename <名称>` | 重命名当前会话 |
| `/session archive [短编号]` | 归档指定或当前会话 |
| `/sessions archived [页码]` | 查看已归档会话 |
| `/session restore <短编号>` | 恢复会话 |
| `/cwd` | 查看当前工作目录 |
| `/cwd /绝对路径` | 修改后续 thread/turn 的工作目录 |
| `/help` | 查看命令 |

旧 `/new` 和 `/clear` 已删除，只会提示使用 `/session new`。其他原 Agent 路由命令不再具有特殊含义。

## 图片、文件和主动发送

- 微信图片会下载到每次 turn 的私有目录，并作为 Codex `localImage` 输入。每条最多 4 张，单张最大 20 MiB，支持 JPEG、PNG、GIF 和 WebP。
- PDF、日志、补丁、压缩包和常见源码作为不可信文件交给 Codex；桥接器不会执行或自动解压。
- Codex 写入本次 turn 专属 `outbox` 的交付物会自动上传回微信。不会解析回复中的任意本机绝对路径。
- turn 结束、失败或取消后，私有 `inbox/outbox` 整体删除。

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
- 会话列表先读取 Codex 全局 thread，再只保留本地所有权索引中的 ID，避免暴露其他 Codex 客户端历史。
- 终端原始输出、命令文本、diff 和环境变量不会作为进度消息发送到微信。

## 开发验证

```bash
go test ./...
go test -race ./codex ./messaging ./session ./config ./reporting
go vet ./...
```

详细文档：

- [会话管理](docs/session-management.md)
- [微信长任务进度](docs/wechat-progress.md)
- [文件、交付物与定时巡检](docs/attachments-and-reports.md)

## License

MIT。保留原项目版权与提交历史。
