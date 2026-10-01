# 首次使用

本页适用于新实例。已有 JSON 状态先阅读[离线迁移](../operations/migration.md)；不要为绕过迁移错误删除状态目录。

## 准备环境

- 使用专用普通系统账号，确保该账号下的 `codex` 已安装并完成认证。
- 从源码构建需要 Go 1.25+；使用已发布程序不需要 Go。
- 启动进程需要能连接微信 iLink 和所配置的 Codex 服务。
- 图片菜单与阅读图需要可用的非 Snap Chromium；语音输出另需 FFmpeg 和已配置的 TTS 提供商。首次使用可先关闭图片能力。

Codex 拥有完整宿主访问权限，不会为每次文件或命令操作征求批准。工作空间白名单不是系统沙箱，先阅读[安全模型](../architecture/management-security.md)。

## 安装程序

在源码仓库中构建并安装到当前用户目录：

```bash
go build -o codex-link-clawbot ./cmd/codex-link-clawbot
mkdir -p "$HOME/.local/bin"
install -m 0755 codex-link-clawbot "$HOME/.local/bin/codex-link-clawbot"
export PATH="$HOME/.local/bin:$PATH"
codex-link-clawbot version --json
```

`export` 只影响当前终端。服务进程使用自己的环境，需要单独配置可执行程序路径。Linux 发布程序及服务安装见[部署](../operations/deployment.md)。

## 设置工作空间

以下是示例路径，请替换为希望交给 Codex 处理的项目目录。命令需在服务未运行时执行：

```bash
codex-link-clawbot config workspace set workspace 主项目 /absolute/path/to/project
codex-link-clawbot config set visual.enabled false
codex-link-clawbot config
```

默认配置只有 `workspace`，目录为 `~/.codex-link-clawbot/workspace`。使用相同 ID 的 `workspace set` 更新它，避免无意保留额外目录。配置和全部凭据最终写入私有 SQLite，不需要手工维护 JSON 文件。其他选项见[配置指南](configuration.md)。

## 扫码并启动

```bash
codex-link-clawbot login
codex-link-clawbot binding list
```

用准备绑定的微信扫码并在手机确认。凭据保存到 `~/.codex-link-clawbot/data/clawbot.db`。只有一个有效绑定时可自动选中；多个绑定必须在停服状态显式选择：

```bash
codex-link-clawbot binding select <bot-id>
codex-link-clawbot start
```

只有一个绑定时直接运行 `start` 即可。它是前台进程，不是安装后台服务的命令；没有凭据时会自动进入扫码流程。暂未安装 Chromium 时应保持 `visual.enabled=false`，否则图片能力初始化失败会阻止启动。

## 连接管理台

保持服务运行，在另一个终端执行：

```bash
codex-link-clawbot status
codex-link-clawbot console
```

`status` 通过本机 Unix socket 查询状态。`console` 输出 `url`、`token` 和 `token_path`；最后一个字段指向保存令牌的数据库，不是独立令牌文件。管理台使用令牌，不存在通用默认密码。令牌只应输入可信页面，不要发进聊天或截图。

确认页面显示正确的绑定、工作空间、目标和连接状态。默认访问地址为 `http://127.0.0.1:18120`；在其他设备访问需配置[HTTPS 转发](../operations/deployment.md#远程管理)。

## 第一次工作

1. 微信单独发送 `#` 或 `/`，确认目标工作空间。
2. 发送一个影响可控的任务，例如“只读取项目结构，概述入口，不修改文件”。
3. 检查执行结果和网页请求记录是否对应同一工作空间与会话；快速请求应只有最终回答，没有额外接收回执。
4. 等待结束后继续追问，确认沿用原会话。

附件需要显式提交，控制编号必须带 `#`。日常操作和异常恢复见[工作台指南](workbench.md)。`status` 为 ready 只证明运行状态，不代表以上真实工作流程已验证。

前台运行时，在确认没有活动工作后用 Ctrl+C 退出。Linux 用户服务使用 `stop` / `restart` 排空；不要把这两个 systemd 命令当作 macOS 或前台进程控制命令。
