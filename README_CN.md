# WeClaw

把个人微信连接到本机 Codex 的专用工作台。

WeClaw 只支持 Codex App Server。它不是通用机器人框架，不提供 Claude、Gemini、Kimi、多 Agent 路由、远程 Shell、任意工作目录或旧协议回退。

> 本项目不是微信官方项目，也不隶属于腾讯或 OpenAI。微信接入参考公开 iLink 实现，仅用于个人学习和自用。

[English](README.md) · [完整文档](docs/README.md) · [产品路线](docs/roadmap.md)

## 能做什么

| 领域 | 当前能力 |
| --- | --- |
| 输入 | 微信文字、最多四张图片、受限文件和图片批注 |
| Codex | 单一 App Server、项目白名单、持久 thread 与 turn |
| 会话 | 新建、搜索、详情、切换、重命名、归档和恢复 |
| 任务 | 持久 FIFO、暂停、重排、取消、重试和重启恢复 |
| 工作流 | 按绑定者和项目隔离的快捷任务与顺序参数 |
| 回复 | 自适应文字、五套阅读图、图片/文件交付和图片 + MP3 语音 |
| 运维 | 确定性检查、远程锁定、无回复诊断、排空和事务部署 |

所有普通消息都进入 Codex；项目、会话、任务、偏好和运维控制由确定性代码处理。发送 `/` 会得到一张包含六个领域、40 个稳定编号的操作总览，常用动作可直接回复两位编号一步到达。

## 工作链路

```text
绑定者微信
  → iLink 长轮询与附件校验
  → 持久任务队列
  → codex app-server --listen stdio://
  → 冻结最终结果
  → 文字 / 阅读图 / 图片 / 文件 / MP3
```

- 只接受扫码凭据中绑定者的私聊消息。
- 普通输入在确认前完整落盘，执行时使用入队时冻结的项目、会话和回答偏好。
- App Server 握手失败时服务直接退出，不提供 echo 或备用模型。
- 多媒体先完成整批微信 CDN 预上传，再开始任何可见发送。

详细边界见 [架构总览](docs/architecture/overview.md)。

## 快速开始

要求 Go 1.25+、已经登录的 `codex`，以及启用视觉能力时可用的非 Snap Chromium。

```bash
npx playwright install chromium
go install github.com/huixiangyang/weclaw/cmd/weclaw@main
weclaw login
weclaw start
```

配置文件位于 `~/.weclaw/config.json`。至少配置一个 Codex 可以进入的项目绝对路径；完整字段、视觉、Piper、MiMo、自动化和主动发送配置见 [配置参考](docs/guides/configuration.md)。

生产环境只由 systemd 用户服务托管。状态、排空、停止和重启使用当前用户私有的 Unix socket：

```bash
weclaw status
weclaw restart
weclaw stop
```

安装、首次微信验证与安全提醒见 [安装与启动](docs/guides/getting-started.md)。日常升级见 [部署手册](docs/operations/deployment.md)。

## 微信交互

发送 `/` 打开一张完整操作总览。总览在 30 分钟内有效，可直接回复 `11` 新建会话、`12` 重命名当前会话、`31` 查看任务、`48` 切换简洁风格；`1`–`6` 进入对应领域中心。也可以直接使用自然语言，例如：

- `新建会话 叫登录排障`、`搜索会话 登录`、`切换会话 登录`
- `项目`、`会话列表`、`任务中心`、`快捷任务`
- `暂停队列`、`继续队列`、`取消`、`重试上次任务`
- `回答方式`、`阅读模式`、`开启语音模式`
- `视觉风格`、`切换风格 刊物`、`切换风格 可爱`
- `自动化`、`素材箱`、`交付记录`、`为什么没回复`
- `远程锁定`，之后使用 `解锁 解锁码`

旧斜杠命令已经删除。除单独的 `/` 外，以 `/` 开头的内容不会执行，也不会进入 Codex。

## 图片与语音回复

阅读回复使用刊物、构筑、黑标、可爱和简洁五套独立模板，按本地时间自动切换昼夜主题。回复 Markdown 会安全解析为标题、段落、任务清单、表格、引用和代码块；不会执行 Codex 返回的 HTML 或脚本。

普通阅读图会整批渲染并预上传。第二页上传失败时第一页不会先出现；部分发送响应不确定时也不会盲目补发整篇正文。30 分钟内回复“文字版”可取回完整可复制原文。

微信通道不能可靠产生原生语音气泡，因此语音固定交付为阅读图和 MP3 文件，不伪装成功。语音支持本机 Piper 与 MiMo TTS 有序回退。

## 仓库结构

```text
cmd/weclaw/        唯一二进制入口
internal/          全部非公开实现
docs/guides/       使用与配置
docs/architecture/ 代码、状态和事务边界
docs/operations/   部署、迁移和验收
scripts/           辅助安装脚本
service/           systemd 与 launchd 定义
```

本仓库是应用而不是 Go 库，因此不公开根级实现包。新增代码必须遵守 [目录与依赖规则](docs/architecture/overview.md)。

## 开发

```bash
make check
```

等价核心检查包括 `go test ./...`、race detector、`go vet ./...` 和 `go build ./cmd/weclaw`。CI 还对配置、微信协议、附件和 Codex 事件解码执行 fuzz smoke test，并构建 Linux amd64/arm64。

完整验收见 [微信端与部署验收清单](docs/operations/acceptance.md)。

## 安全

Codex 使用 `approvalPolicy: never` 与 `dangerFullAccess` 在项目白名单中工作。绑定者能够通过微信驱动本机代码工具，因此只能绑定可信个人账号，并建议使用独立操作系统用户和最小项目列表。

状态文件默认 `0600`、目录 `0700`，拒绝符号链接、未知字段与越界输入。运行管理只走私有 Unix socket；主动发送 TCP API 默认关闭。日志不记录问题正文、回答预览、原始账号、私有路径、令牌、命令输出或 diff。

## License

MIT。保留原始版权和 Git 历史。
