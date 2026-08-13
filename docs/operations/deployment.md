# 日常部署

## 构建候选

唯一二进制入口是 `./cmd/codex-link-clawbot`：

```bash
version=v2.7.0-local.1
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
  -ldflags="-s -w -X github.com/huixiangyang/codex-link-clawbot/internal/cli.Version=${version}" \
  -o /absolute/path/to/codex-link-clawbot ./cmd/codex-link-clawbot
```

候选必须先通过：

```bash
make check
```

发布流水线还会构建 Linux amd64 与 arm64，并为不可变发布物生成 SHA-256 清单。

## 事务部署

已进入当前管理面后的生产环境统一使用：

```bash
codex-link-clawbot deploy v2.7.0
codex-link-clawbot deploy --binary /absolute/path/to/codex-link-clawbot --expect-version v2.7.0-local.1
```

部署事务依次执行候选校验、旧服务排空、停机状态快照、离线迁移、原子安装、新服务排空启动、健康验收、正式单元恢复和队列放行。

以下任何一项失败都会触发完整回滚：

- 候选版本、平台或 SHA-256 不符。
- 旧服务无法排空或停止。
- 状态快照、迁移或原子安装失败。
- 新进程版本、Codex、微信监控、同步游标或管理 socket 不健康。

回滚同时恢复旧二进制、systemd 单元、配置、游标和状态，不只替换可执行文件。

## 部署后验证

```bash
codex-link-clawbot status
systemctl --user status codex-link-clawbot.service
```

然后在微信检查 `/`、codex-link-clawbot 执行状态、Codex 线程列表、阅读回复、“为什么没回复”和一次图片或文件请求。完整矩阵见 [验收清单](acceptance.md)。

## 首次跨管理面切换

仍运行旧 TCP 管理面的生产实例不能由新 CLI 直接驱动事务部署。必须按 [旧生产线迁移](migration.md) 和 [首次生产切换](production-cutover.md) 安排维护窗口；完成一次性切换后才回到本文流程。

## 生产边界

- 不在正常运行的生产状态目录上手工执行迁移。
- 不启动第二个微信轮询进程验证候选。
- 不绕过排空直接覆盖二进制。
- 不把包含任务正文、附件或令牌的状态快照长期保留。
- 部署成功只写入固定格式待阅通知，并在绑定者下一次有效交互中补送；不允许部署器借管理面发送任意内容。
