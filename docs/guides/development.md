# 开发指南

需要 Go 1.25+ 和 Make。前端使用 Go embed 与原生 JavaScript/CSS，没有独立的前端构建步骤。先阅读[架构总览](../architecture/overview.md)，再按职责修改对应模块。

## 检查与构建

| 命令 | 用途 |
| --- | --- |
| `make fmt` | 格式化 Go 源码 |
| `make docs-check` | 检查 README 与文档的 Markdown 文件链接 |
| `make test-fast PACKAGES=./internal/core/request` | 指定包的普通测试 |
| `make test PACKAGES='./internal/core/request ./internal/adapters/management'` | 指定包的 race 测试 |
| `make check-fast` | 文档、格式、vet、全量普通测试和本机构建 |
| `make check` | 文档、格式、vet、全量 race 和本机构建 |
| `make fuzz-smoke` | 配置、微信协议、附件和 Codex 事件的五项短时 fuzz |
| `make build` | 仓库根目录生成 `codex-link-clawbot` |

Makefile 使用 `/tmp` 的真实路径作为测试临时目录，避开 macOS 默认路径的符号链接及 Unix socket 长度限制。直接运行测试时使用：

```bash
TMPDIR="$(cd /tmp && pwd -P)" go test ./internal/core/request -count=1 -race
```

CI 的质量任务运行 `make check`，另有 fuzz 任务。日常构建只使用本机架构；发布固定为一份 Linux 程序，规则见[部署](../operations/deployment.md#发布契约)。

## 修改位置

| 需求 | 主要入口 |
| --- | --- |
| 消息路由、草稿、菜单和微信反馈 | `adapters/wechat/handler.go`、`draft.go`、`conversation_commands.go`、`number_menu*.go` |
| 会话准入、取消、执行与恢复归档 | `core/execution/coordinator.go`、`runner.go` |
| 跨微信和网页的会话操作 | `core/conversation/service.go` |
| 请求状态、文件校验、SQL 分页 | `core/request` |
| 当前目标、可信线程目录 | `core/target`、`core/thread`、`core/workspace` |
| 管理 API 与界面 | `adapters/management`、其 `web/` 资源 |
| Codex JSON-RPC 与子进程 | `adapters/appserver` |
| 图片、语音及媒体发送 | `adapters/wechat/visual`、`voice`、`reply_*.go` |
| 配置与进程装配 | `app/config`、`app/app.go` |
| 数据库 schema、日志、文件安全 | `platform/storage`、`logging`、`statefile` |
| 命令、部署及离线迁移 | `cli`、`app/migration` |

上述路径均位于 `internal/`。核心业务不导入适配器；不为搬移目录新增转发包或兼容别名。页面和菜单复用会话服务，不各自复制归属与取消规则。

展示原文、回答和清单使用 `InspectRequest` / `InspectResult`，列表使用 `ListSummaries`，下载使用 `OpenArtifact`；执行和投递保留完整附件校验。不要在视图映射函数里查询数据库或扫描文件。持久化变更必须保持事务失败时内存状态不前移，首次投递回执与终态使用 `CompleteDelivery` 一次提交。

## 热重载

安装 Air 后运行 `make dev`。它构建仓库根目录程序并执行 `start`，监控 Go 和嵌入资源，重启前发送中断并等待最多十秒，编译失败停止旧构建。编译错误日志位于 `tmp/build-errors.log`。

该命令使用当前用户的真实配置和凭据，会连接微信并启动 Codex，不是隔离预览；不要与生产实例共用同一状态根。仅检查网页时使用下面的夹具。

## 隔离预览

管理台夹具使用临时数据库、16 条请求、26 个模拟会话，不连接真实微信或 Codex：

```bash
CLAWBOT_BROWSER_FIXTURE=/private/tmp/clawbot-preview.json \
TMPDIR=/private/tmp go test ./internal/adapters/management -run '^TestBrowserWorkbench$' -count=1 -v
```

就绪文件包含临时 URL 和测试令牌。夹具最多运行五分钟；创建 `/private/tmp/clawbot-preview.json.stop` 可提前结束，下一次启动前移除旧停止文件。该 JSON 是测试接口，不是应用持久状态。Linux 可把示例中的 `/private/tmp` 改成 `/tmp`。

已安装 Chromium 时可生成菜单图片与 HTML，检查 320 px 手机宽度、192 px 缩略图以及长名称：

```bash
CLAWBOT_VISUAL_PREVIEW_DIR=/private/tmp/clawbot-menu-preview \
TMPDIR=/private/tmp go test ./internal/adapters/wechat/visual -run '^TestMenuRendersWithInstalledChromium$' -count=1 -v
```

浏览器缺失会跳过相应渲染测试，不能把普通测试通过解释成视觉已验收。静态旧截图不作为现行界面的证据。

## 测试与交付

优先复用现有夹具，只为新的业务边界、数据一致性或并发风险补必要测试。涉及请求和投递时至少检查所有者隔离、到期、重复来源、失败恢复及事务回滚。协议解析变化再补 fuzz，不为文档修改增加单元测试。

列表微基准可独立复测，不把测试夹具数字写成生产性能：

```bash
TMPDIR="$(cd /tmp && pwd -P)" go test ./internal/adapters/management -run '^$' -bench '^BenchmarkRequestList$' -benchtime=10x -count=3
```

功能变化同步更新[对应文档](../README.md)。本地检查、视觉验证、真实渠道联调和部署按[验收](../operations/acceptance.md)分别记录；不要用历史“通过”状态代替当前运行结果。
