# codex-link-clawbot

把个人微信连接到 Codex，并把所有管理能力收进独立 Web 控制台。

它只做三件事：接收绑定者的微信文字、图片和文件；把请求可靠送入当前 Codex 工作空间与线程；把结果和交付物发回微信。线程、队列、工作空间、回复偏好、运行状态和远程锁定不再通过微信命令操作。

> 本项目不是微信官方项目，也不隶属于腾讯或 OpenAI。微信接入参考公开 iLink 实现，仅用于个人学习和自托管使用。

> 本项目基于 [WeClaw](https://github.com/fastclaw-ai/weclaw) 改造，保留 MIT 许可证、原始版权声明和 Git 历史。详细来源见[上游关系](docs/architecture/upstream.md)。

[English](README.md) · [完整文档](docs/README.md)

## 产品边界

| 入口 | 能力 |
| --- | --- |
| 微信 | `菜单` / `Codex` / `Codex Link` 显示当前连接和管理地址；其他文字、图片、文件一律作为 Codex 请求 |
| 管理台 | 工作空间切换、线程创建与目标选择、请求队列、回复偏好、交付记录、进程排空/恢复、远程锁定 |
| 本机 CLI | 登录、启动、状态、部署、读取管理台地址与令牌 |

旧数字菜单、中文控制意图、微信内诊断、微信内取消/重试和短期菜单状态已经删除，不提供兼容分支。完整边界见[能力边界](docs/guides/capability-boundary.md)。

## 工作链路

```text
绑定者微信
  → iLink 私聊校验与附件检查
  → 持久请求队列
  → Codex App Server
  → 冻结最终结果与交付物
  → 微信文字 / 阅读图 / 文件 / MP3

浏览器
  → 管理令牌
  → 127.0.0.1:18120 管理 API
  → 线程 / 工作空间 / 队列 / 设置
```

普通输入会在确认前完整落盘；执行使用入队时冻结的工作空间、线程和回复偏好。Codex App Server 不可用时进程直接失败，不回退到其他模型或协议。

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

发送 `菜单`、`Codex` 或 `Codex Link` 会得到一段很短的连接摘要：状态、当前工作空间、目标线程和管理地址。

除此之外不再解析命令。例如 `状态`、`取消`、`1`、`请求队列`、`视觉风格` 和 `文字版` 都是普通 Codex 提示词。图片和文件也直接进入当前目标；控制动作只能在管理页面完成。

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
make check
```

核心检查包括文档链接、格式、`go vet ./...`、race 测试和二进制构建。验收步骤见[验收清单](docs/operations/acceptance.md)。

## License

MIT。
