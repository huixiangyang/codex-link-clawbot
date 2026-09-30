# codex-link-clawbot

把个人微信连接到 Codex，用单个数字在手机上管理会话、请求和结果。

它是个人自托管的 Codex 远程工作台：接收绑定者的微信文字、图片和文件；把请求可靠送入当前 Codex 工作空间与线程；把结果和交付物发回微信。微信提供适合小屏的数字菜单；Web 提供搜索、详细管理和运行维护。

> 本项目不是微信官方项目，也不隶属于腾讯或 OpenAI。微信接入参考公开 iLink 实现，仅用于个人学习和自托管使用。

> 本项目基于 [WeClaw](https://github.com/fastclaw-ai/weclaw) 改造，保留 MIT 许可证、原始版权声明和 Git 历史。详细来源见[上游关系](docs/architecture/upstream.md)。

[English](README.md) · [完整文档](docs/README.md)

## 产品边界

| 入口 | 能力 |
| --- | --- |
| 微信 | `0` / `菜单` 打开数字菜单；新建与切换对话、工作空间、会话状态、结果和回复设置均可用数字操作；普通内容作为 Codex 请求 |
| 管理台 | 工作空间切换、会话搜索/分页/新建/选择/改名/归档、请求与结果中心、显式恢复、能力偏好、进程排空/恢复、远程锁定 |
| 本机 CLI | 登录、单绑定选择、启动、状态、离线迁移、部署、读取管理台入口 |

数字菜单使用新的单键分页交互，不兼容旧多位数字编号、自然语言控制意图或旧菜单状态文件。完整边界见[能力边界](docs/guides/capability-boundary.md)。

## 工作链路

```text
绑定者微信
  → iLink 私聊校验与附件检查
  → 按会话即时准入（忙碌则拒绝）
  → Codex App Server
  → 冻结最终结果与交付物
  → 微信文字 / 阅读图 / 文件 / MP3

浏览器
  → 管理令牌
  → 127.0.0.1:18120 管理 API
  → 线程 / 工作空间 / 实时会话管理 / 设置
```

普通输入会在确认前完整落盘；执行使用接收时固定的工作空间、线程和回复偏好。Codex App Server 不可用时进程直接失败，不回退到其他模型或协议。

## 快速开始

要求 Go 1.25+、已安装并登录的 `codex`。视觉回复启用时还需要可用的非 Snap Chromium。

```bash
go install github.com/huixiangyang/codex-link-clawbot/cmd/codex-link-clawbot@main
codex-link-clawbot login
codex-link-clawbot start
```

首次启动会在 `~/.codex-link-clawbot/management-token` 生成权限为 `0600` 的随机管理令牌。读取管理入口：

```bash
codex-link-clawbot console
```

命令会输出管理地址、令牌和令牌文件路径。令牌属于敏感信息，只应粘贴到可信浏览器；页面仅将它保存在当前标签页的 `sessionStorage`。

## 配置

配置文件位于 `~/.codex-link-clawbot/config.json`，当前结构版本为 7。最小示例：

```json
{
  "schema_version": 7,
  "codex": {
    "command": "codex",
    "model": "",
    "env": {}
  },
  "codex-link-clawbot": {
    "project_entries": [
      {"id": "workspace", "name": "主工作区", "root": "/srv/workspace"}
    ],
    "reply": {},
    "security": {},
    "management": {
      "listen": "127.0.0.1:18120",
      "public_url": "https://codex-link.example.com"
    }
  }
}
```

管理服务强制只监听回环地址。公网访问应由 Cloudflare Tunnel 或反向代理把 HTTPS 域名转发到 `http://127.0.0.1:18120`；`public_url` 只用于页面状态和微信链接提示，不负责创建隧道。完整字段见[配置指南](docs/guides/configuration.md)。

## 微信交互

发送 `0`、`菜单`、`Codex` 或 `Codex Link` 打开数字菜单。首页 `1–6` 依次为新建对话、切换会话、工作空间、会话状态、最近结果、回复设置。列表每页最多四项，`7/8` 翻页，`0` 回首页，`9` 退出。正常只发一张图，图片不可用时发送同编号文字。见[数字菜单设计](docs/design/wechat-menu.md)。

菜单有效期为十分钟，数字只对应当前展示的选项。过期或重启后的旧编号不会执行操作。普通文字、图片和文件会退出菜单并提交到当前目标；显式退出后，普通数字也可作为任务内容，`0` 仍是菜单快捷入口。解锁码输入页例外：输入只用于解锁。打断、重跑、重发和锁定均有数字确认页。

## 运维与安全

生产环境使用 systemd 用户服务。本机生命周期操作仍通过仅属主可访问的 Unix socket：

```bash
codex-link-clawbot status
codex-link-clawbot restart
codex-link-clawbot stop
```

Web 管理 API 需要固定长度的随机令牌、拒绝跨站框架嵌入，并设置严格 CSP。管理端口不允许直接监听公网网卡。绑定者能够驱动本机 Codex，因此只应绑定可信个人账号，并把工作空间白名单限制到必要目录。详见[管理安全模型](docs/architecture/management-security.md)。

## 开发

```bash
make check-fast   # 日常开发检查
make check
```

`make check` 与 CI 共用同一入口，包含文档链接、格式、`go vet ./...`、race 测试和二进制构建。按包测试、热重载与模块分工见[开发指南](docs/guides/development.md)，设计取舍见[架构调整记录](docs/architecture/refactoring.md)，联调步骤见[验收清单](docs/operations/acceptance.md)。

## License

MIT。

## 业务重整

首条消息建立会话，连续消息持续同一上下文。请求冻结提交时的目标，之后切换不会改变正在执行的工作。同一会话忙碌时再次下令会立即失败，并展示数字操作菜单；被拒指令不会稍后执行。执行完成与微信投递分别记录；已完成的回答和文件在发送失败时仍可由网页取回。

输入保留 24 小时、结果 7 天、记录 30 天。重跑创建关联原请求的新记录，重投只发送已有结果；两者均需显式确认和近期真实微信上下文。实例只启用一个明确选择的绑定。

使用流程见[工作台指南](docs/guides/workbench.md)。旧实例升级需要[离线迁移](docs/operations/migration.md)，运行时不兼容旧请求索引或旧结果格式。
