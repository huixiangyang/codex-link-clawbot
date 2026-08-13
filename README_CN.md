# codex-link-clawbot

把个人微信连接到本机 Codex 的专用工作台。

codex-link-clawbot 只支持 Codex 应用服务。它不是通用机器人框架，不提供 Claude、Gemini、Kimi、多智能体路由、远程命令执行、任意工作目录或旧协议回退。

> 本项目不是微信官方项目，也不隶属于腾讯或 OpenAI。微信接入参考公开 iLink 实现，仅用于个人学习和自用。

> 上游说明：本项目基于 [WeClaw](https://github.com/fastclaw-ai/weclaw) 开发改造，保留其 MIT 许可证、原始版权声明和 Git 历史。`codex-link-clawbot` 是面向 Codex 全局管理与微信 Clawbot 场景的独立衍生项目，不代表 WeClaw 上游项目或其维护者。

[English](README.md) · [完整文档](docs/README.md) · [产品路线](docs/roadmap.md)

## 能做什么

| 归属 | 当前能力 |
| --- | --- |
| Codex | 跨桌面端、CLI、IDE 与 App Server 的全局线程目录、真实轮次、模型、审查、技能和外部工具 |
| codex-link-clawbot | 微信文字、最多四张图片、受限文件和图片批注 |
| codex-link-clawbot | 受信任工作空间、远程目标线程、持久请求队列、取消、重试和重启恢复 |
| codex-link-clawbot | 成功请求续接、最近结果、带线程来源和摘要校验的私有交付箱、明确失效与再次发送 |
| codex-link-clawbot | 自适应文字、五套视觉系统、移动审查包、图片/文件交付和图片 + MP3 语音 |
| codex-link-clawbot | 部署与恢复待阅通知、远程锁定、无回复诊断、排空和事务部署 |

Codex 与 codex-link-clawbot 是两层：Codex 拥有全局线程、轮次、账号、模型、审查、技能和外部工具；codex-link-clawbot 提供微信接入、工作空间白名单、目标线程焦点、请求队列、回复呈现、交付箱和运维。发送 `/` 得到全局工作台，直接查看最近线程、全局遥测和执行状态，并在同一页使用 17 个微信可用命令；不依赖二级功能目录。所有可以映射的 Codex 操作都按“中文 · `/command`”同时渲染。

发送 `/review` 或“代码审查”会在原线程调用 Codex 原生审查器，并返回专用移动审查卡。卡片除结论、最高优先级、最多三项重点和脱敏位置外，还展示不含文件名、命令和终端输出的变更、验证与交付事实；可继续修复、接受结论或冻结目标后重新审查，30 分钟内回复“文字版”可取回完整审查原文。

Codex 应用服务没有独立的项目对象。菜单中的“Codex 工作空间”是允许微信查看、接管和执行 Codex 工作的本机目录白名单；“目标线程”只是下一次远程操作的焦点，不决定全局可见性。微信消息先成为 codex-link-clawbot 请求，协调器调用 `turn/start` 后才成为 Codex 轮次。完整定义见 [能力边界](docs/guides/capability-boundary.md)。

## 工作链路

```text
绑定者微信
  → iLink 长轮询与附件校验
  → codex-link-clawbot 持久请求队列
  → Codex 应用服务
  → 冻结最终结果
  → 文字 / 阅读图 / 图片 / 文件 / MP3
```

- 只接受扫码凭据中绑定者的私聊消息。
- 普通输入在确认前完整落盘，执行时使用入队时冻结的项目、线程和回答偏好。
- Codex 应用服务握手失败时直接退出，不提供回显或备用模型。
- 多媒体先完成整批微信 CDN 预上传，再开始任何可见发送。

部署完成事件和长任务交付恢复提醒不会使用过期消息令牌主动发送，而是进入私有待阅状态；绑定者下一次发来有效消息时最多合并补送四条。待阅状态不保存微信 `context_token`。

详细边界见 [架构总览](docs/architecture/overview.md)。

## 快速开始

要求 Go 1.25+、已经登录的 `codex`，以及启用视觉能力时可用的非 Snap Chromium。

```bash
npx playwright install chromium
go install github.com/huixiangyang/codex-link-clawbot/cmd/codex-link-clawbot@main
codex-link-clawbot login
codex-link-clawbot start
```

配置文件位于 `~/.codex-link-clawbot/config.json`，当前结构版本为 5，顶层明确分为 `codex` 与 `codex-link-clawbot`。至少配置一个 Codex 可以进入的工作空间绝对路径；执行 `codex-link-clawbot config` 可查看脱敏后的有效配置。完整字段、视觉、Piper 和 MiMo 配置见 [配置与设置](docs/guides/configuration.md)。

生产环境只由 systemd 用户服务托管。状态、排空、停止和重启使用当前用户私有的 Unix socket：

```bash
codex-link-clawbot status
codex-link-clawbot restart
codex-link-clawbot stop
```

安装、首次微信验证与安全提醒见 [安装与启动](docs/guides/getting-started.md)。日常升级见 [部署手册](docs/operations/deployment.md)。

## 微信交互

发送 `/` 打开 1080×780 Codex 全局工作台：首页左侧展示最近四个线程，右上展示工作空间、全部线程、运行中和微信队列遥测，下方直接展示 17 个微信可用命令。`1`–`4` 接管对应最近线程，`5` 打开全部线程 `/resume`，`6` 新建线程 `/new`，`7` 查看执行与队列，`8` 打开工作空间，`9` 刷新；`11`–`43` 的稳定能力也可从首页直接回复。首页状态有效期为 5 分钟，不需要进入二级功能目录。

“当前目标”只表示下一条微信内容的发送位置，不等于 Codex 正在执行。桌面端、CLI 或其他来源可以同时存在多个运行中线程；codex-link-clawbot 请求的等待、执行和发送状态也单独展示，不与 Codex 线程状态混为一谈。

机器级配置与微信偏好严格分开：回答方式、视觉风格、默认工作空间和目标线程可以在微信立即修改；命令、工作空间白名单、浏览器和 TTS 密钥只允许在本机配置。完整模型见 [功能与配置模型](docs/architecture/feature-and-configuration.md)。

- `新建线程 叫登录排障`、`搜索线程 登录`、`切换线程 登录`
- `分叉当前线程`、`置顶当前线程`、`压缩上下文`
- `设置线程目标为 完成发布`、`线程模型`、`推理强度`
- `项目`、`线程列表`、`请求队列`、`再次执行最近请求`
- `追加指令 先修复失败测试`、`暂停队列`、`继续队列`、`取消当前执行`
- `代码审查`、`永久删除线程`
- `回答方式`、`阅读模式`、`开启语音模式`
- `视觉风格`、`切换风格 刊物`、`切换风格 可爱`
- `交付箱`、`发语音`、`为什么没回复`
- `远程锁定`，之后使用 `解锁 解锁码`

codex-link-clawbot 注册表识别当前[官方 Codex CLI 斜杠命令目录](https://learn.chatgpt.com/docs/developer-commands.md?surface=cli)的 49 个主命令和 5 个别名，但微信界面只展示 17 个能够真实执行的原生或适配命令。`/resume` 搜索所有受信任工作空间中的全局线程，`/usage` 展示账号与额度，`/skills` 和 `/mcp` 汇总所有工作空间；`/status`、`/rename`、`/fork`、`/compact`、`/goal` 和线程模型修改只作用于目标线程。TUI、Windows 或实验协议专属命令不进入首页、可用命令目录或文字降级菜单。未知斜杠命令会被拒绝，也不会进入 Codex 提示词。

## 图片与语音回复

阅读回复使用刊物、构筑、黑标、可爱和简洁五套独立模板，按本地时间自动切换昼夜主题。回复 Markdown 会安全解析为标题、段落、任务清单、表格、引用和代码块；不会执行 Codex 返回的 HTML 或脚本。

普通阅读图会整批渲染并预上传。第二页上传失败时第一页不会先出现；部分发送响应不确定时也不会盲目补发整篇正文。30 分钟内回复“文字版”可取回完整可复制原文。

微信通道不能可靠产生原生语音气泡，因此语音固定交付为阅读图和 MP3 文件，不伪装成功。语音支持本机 Piper 与 MiMo TTS 有序回退。

## 仓库结构

```text
cmd/codex-link-clawbot/        唯一二进制入口
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

等价核心检查包括 `go test ./...`、race detector、`go vet ./...` 和 `go build ./cmd/codex-link-clawbot`。CI 还对配置、微信协议、附件和 Codex 事件解码执行 fuzz smoke test，并构建 Linux amd64/arm64。

完整验收见 [微信端与部署验收清单](docs/operations/acceptance.md)。

## 安全

Codex 使用 `approvalPolicy: never` 与 `dangerFullAccess` 在项目白名单中工作。绑定者能够通过微信驱动本机代码工具，因此只能绑定可信个人账号，并建议使用独立操作系统用户和最小项目列表。

状态文件默认 `0600`、目录 `0700`，拒绝符号链接、未知字段与越界输入。运行管理只走私有 Unix socket，不提供通用主动发送 TCP API；系统通知只写入私有待阅状态并随下一次有效交互补送。日志不记录问题正文、回答预览、原始账号、私有路径、令牌、命令输出或 diff。

## License

MIT。保留 WeClaw 上游项目的原始版权、许可证声明和 Git 历史；详细来源关系见[上游关系与改造边界](docs/architecture/upstream.md)。
