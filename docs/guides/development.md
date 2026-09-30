# 开发指南

## 环境与命令

需要 Go 1.25+ 与 Make。热重载额外需要已安装的 Air；运行桥接服务还需要完成 Codex 登录与微信绑定。

| 命令 | 用途 |
| --- | --- |
| `make fmt` | 格式化 Go 源码 |
| `make test-fast PACKAGES=./internal/execution` | 只运行指定包的测试，不启用 race |
| `make test PACKAGES='./internal/bridge ./internal/execution'` | 对相关包运行 race 测试 |
| `make check-fast` | 文档、格式、vet、全量普通测试和构建 |
| `make check` | 提交前完整检查，与 CI 使用同一入口 |
| `make fuzz-smoke` | 协议和输入校验的短时 fuzz 检查 |
| `make dev` | 通过 Air 构建并运行 `start`，监控 Go 与嵌入静态资源 |

测试默认通过 Makefile 将 `TMPDIR` 指向 `/tmp` 的真实路径。直接执行 `go test` 时，macOS 默认路径可能因符号链接安全检查或 Unix socket 路径长度而失败；可使用：

```bash
TMPDIR="$(cd /tmp && pwd -P)" go test ./internal/execution -race
```

`make dev` 使用当前用户的配置与微信凭据，会真正启动消息服务；不要同时运行占用相同状态租约的生产实例。保存 JS/CSS/HTML 或视觉 WebP 资源后也会重建。Air 在重启前发送中断，最多等待 10 秒；编译失败停止运行旧构建。

## 修改位置

- 入站消息与交付：`internal/bridge`。
- 即时准入、打断、排空与会话互斥：`internal/execution/coordinator.go`；不要引入微信或 HTTP 客户端。
- 当前目标与延迟创建线程：`internal/target`；旧请求解析自身意图，不能覆盖当前选择。
- 离线业务迁移：`internal/businessmigration`；先备份和验证，再发布，运行时禁止兼容旧格式。
- 持久请求状态机：`internal/request`；业务状态必须在发送确认或交付前落盘。
- 管理 API 与网页：`internal/management` 和 `internal/management/web`。
- 进程装配与故障退出：`internal/app`；新增后台循环必须加入 `serviceGroup`。
- Codex 协议：`internal/codex/appserver`；上层面向 `internal/codex` 的接口。

功能变化同步更新对应指南。验证优先复用已有集成测试，仅为新的业务边界或并发风险补充必要用例。架构边界与设计取舍见[架构调整记录](../architecture/refactoring.md)。

## 隔离管理台预览

无需真实微信或 Codex，可以启动带 16 条请求、26 个会话的真实 HTTP 夹具：

```bash
CLAWBOT_BROWSER_FIXTURE=/private/tmp/clawbot-preview.json \
TMPDIR=/private/tmp go test ./internal/management -run '^TestBrowserWorkbench$' -count=1 -v
```

该路径适用于 macOS；Linux 可改用 `/tmp`。就绪后指定 JSON 文件包含临时 URL 与仅限测试的令牌。夹具最多运行 5 分钟，在同一路径后追加 `.stop` 创建空文件可提前结束。全部状态位于测试临时目录。启动前确保上次 `.stop` 文件已经移除。

页面无前端构建依赖，使用原生 JS/CSS 和 Go embed；修改资源后重启夹具。浏览器验收与生产联调边界见[验证记录](../operations/business-validation.md)。

微信菜单图片有独立的 Chromium 渲染夹具，见[图片菜单预览](../design/wechat-menu.md#预览与验证)。`CLAWBOT_VISUAL_PREVIEW_DIR` 可导出图片；菜单测试同时导出 HTML，便于检查长名称与手机尺寸下的排版。
