# 部署

## 构建

在目标架构或交叉编译环境生成带版本的二进制：

```bash
go test ./...
go vet ./...
go build -ldflags '-X github.com/huixiangyang/codex-link-clawbot/internal/cli.Version=v3.0.0' -o codex-link-clawbot ./cmd/codex-link-clawbot
```

候选二进制会在服务停止且快照完成后，把严格合法的 schema 6 单向升级为 schema 7；不要在事务部署前手工改写线上配置。更旧或包含已下线字段的配置会被拒绝并触发整体回滚。

## 事务部署

在 Linux 目标机执行：

```bash
codex-link-clawbot deploy \
  --binary /absolute/path/codex-link-clawbot \
  --expect-version v3.0.0
```

部署器通过私有 Unix socket 排空请求、创建快照、执行离线状态迁移、原子替换二进制并等待新版本就绪。失败时恢复上一版本及状态快照。

## systemd 用户服务

服务以专用普通用户运行，不使用 root：

```bash
systemctl --user daemon-reload
systemctl --user enable --now codex-link-clawbot.service
journalctl --user -u codex-link-clawbot.service -f
```

## 公网管理域名

Cloudflare Tunnel 的入口应指向：

```text
http://127.0.0.1:18120
```

创建 HTTPS 主机名且候选部署提交后，把公网 HTTPS 地址写入 `management.public_url` 并重启服务。不要把应用监听改成 `0.0.0.0`，也不要把令牌写入 Tunnel 配置或 URL 查询参数。

## 检查

```bash
codex-link-clawbot status
codex-link-clawbot config
codex-link-clawbot console
```

`status` 和部署生命周期使用 Unix socket；`console` 输出 Web 管理入口。两者是不同安全边界。
