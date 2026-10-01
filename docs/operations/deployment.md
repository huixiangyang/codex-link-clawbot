# 部署

生产部署使用 Linux systemd 用户服务。前台运行可用于本地开发，macOS 可使用仓库 LaunchAgent 模板，但事务 `deploy` 仅支持 Linux，`stop` / `restart` 也依赖 systemd，不是跨平台服务管理器。

## 发布契约

[发布工作流](../../.github/workflows/release.yml)只生成两个资产：

```text
codex-link-clawbot
checksums.txt
```

程序由固定的 `ubuntu-24.04` runner 原生构建，当前目标为 Linux x86-64。没有 CPU 构建矩阵、架构后缀、自动架构选包或备用下载分支。`version --json` 中的系统和 CPU 元数据仅用于拒绝不匹配候选程序。

正式标签为 `v<major>.<minor>.<patch>`，候选版为同格式加 `-rc.<number>`。工作流拒绝覆盖已存在的 Release；CI 分支构建是单独的开发 artifact，不等于已发布版本。文档不指定“最新版”，部署时明确选择已发布的不可变版本。

## 首次安装用户服务

先按[首次使用](../guides/getting-started.md)在运行服务的普通账号下完成配置、扫码和前台验证。将已验证的程序安装到 `~/.local/bin/codex-link-clawbot`，在源码仓库安装服务模板：

```bash
mkdir -p "$HOME/.config/systemd/user"
install -m 0644 service/codex-link-clawbot.service "$HOME/.config/systemd/user/codex-link-clawbot.service"
systemctl --user daemon-reload
systemctl --user enable --now codex-link-clawbot.service
```

先退出前台实例，避免占用同一运行锁。模板固定使用 `%h/.local/bin/codex-link-clawbot start`，自动重启并把 stderr 收入 journal。确认用户服务环境能找到 Codex、浏览器和 FFmpeg；优先在应用配置中使用绝对路径，所需环境变量通过用户服务配置提供。不能假设服务继承交互终端的 PATH、代理或密钥。

查看及有序维护：

```bash
codex-link-clawbot status
codex-link-clawbot restart
codex-link-clawbot stop
systemctl --user start codex-link-clawbot.service
```

`stop` / `restart` 先排空，缺省最多等待十分钟，可用 `--timeout` 和 `--service` 调整。它们要求服务的本机 socket 可用；进程已故障时需通过 systemd 和日志排障，而不是反复等待正常排空。后台持续运行和用户会话策略由主机管理员配置。

macOS 模板位于 [LaunchAgent](../../service/com.huixiangyang.codex-link-clawbot.plist)，使用前必须调整实际程序路径与环境。它不是 Linux Release 的第二个发行目标，不提供自动事务升级流程。

## 事务升级

旧服务必须已运行、健康且未处于排空状态。已使用 SQLite 的服务由当前安装的程序发起升级，不手工覆盖正在运行的可执行文件：

```bash
codex-link-clawbot deploy <version>
```

从源码生成本地候选时，在目标 Linux 平台构建并使用明确版本；以下 `v3.0.0` 仅为格式示例，不表示该版本已发布：

```bash
make check
CGO_ENABLED=0 go build -trimpath \
  -ldflags '-s -w -X github.com/huixiangyang/codex-link-clawbot/internal/cli.Version=v3.0.0' \
  -o /absolute/path/to/candidate ./cmd/codex-link-clawbot
codex-link-clawbot deploy --binary /absolute/path/to/candidate --expect-version v3.0.0
```

远程下载校验 `checksums.txt`，所有候选都核对版本和平台。本地候选没有远端校验清单，操作者需确保来源可信。不提供旧 CPU 后缀资产名回退。

首次从 JSON 版本切换时，使用已经验证的新版候选作为部署器，并显式指定安装路径和状态根目录。旧部署器的回滚范围不包含 SQLite `data` 目录，不能用于这次跨存储格式升级：

```bash
/absolute/path/to/candidate deploy \
  --binary /absolute/path/to/candidate --expect-version v3.0.0 \
  --target "$HOME/.local/bin/codex-link-clawbot" \
  --state-root "$HOME/.codex-link-clawbot"
```

候选部署器仍通过旧服务的本机控制协议执行排空，在停服后备份旧 JSON，并在提交前失败时清理新增 SQLite 状态、恢复旧版本。迁移预演只能使用隔离的数据副本，不能提前改写生产状态。

从仍会调用部署通知接口的版本升级时，也使用上述新版候选部署器。新版已删除该接口，不保留兼容路由；旧部署器在提交成功后调用它会失败，但不代表升级已回滚。

可选参数为 `--service`、`--timeout`、`--target`、`--state-root`；后两项必须与实际用户服务一致，不能用它们代替配置完整的第二个实例。

## 提交与回滚

1. 校验候选，通过 Unix socket 排空并等待活动请求收尾。
2. 停止用户服务，快照业务状态、原程序和服务单元。
3. 用候选执行离线迁移，原子替换程序，以排空模式启动候选。
4. 校验版本、Codex、微信监听和无活动请求，再将服务单元恢复正常启动模式。
5. 提交恢复准入，确认 ready，记录部署回执。网页管理台展示当前版本与健康状态，不发送部署成功微信通知。

提交前的失败会尝试恢复旧程序、服务单元和状态，并验证旧服务。回滚自身也可能失败，必须检查实际版本与健康状态，不能只看命令已退出。

进入恢复准入的不可逆提交点后，如果响应不确定，记录 `commit_uncertain`，不再用旧快照覆盖状态，因为新版本可能已消费消息。此时保留现场，检查服务版本、排空状态和日志，再前向确认；不要直接恢复旧数据库或重放旧消息。

回执保存在 `backups/deploy-<id>/data/clawbot.db` 的 `deployment_receipts` 表中，独立于业务库回滚。成功或成功回滚后清理该次敏感业务快照；清理失败会单独报错。失败或提交不确定的残留需人工处置，部署快照不替代长期备份。

启动时清理业务库中旧的部署成功待发通知，旧 JSON 导入也不保留这类通知；请求失败与结果待取回通知不受影响。

快照只覆盖明确受管路径，符号链接会被拒绝；用户工作空间、未知根文件、日志和既有备份不随业务状态回滚。不要在部署事务前单独迁移线上数据，迁移由步骤 3 在停服和快照后完成。

## 远程管理

反向代理或 Tunnel 的目标是本机回环服务，例如 `http://127.0.0.1:18120`，对外提供 HTTPS。之后在停服状态设置 `management.public_url` 并启动；该设置只改变展示链接，不建立代理或 TLS。

不要监听 `0.0.0.0`，不要公开 Unix socket、数据库或日志，也不要把令牌写入 URL。代理仍须允许客户端发送 `X-Codex-Link-Token`，应用认证不能关闭。更多边界见[安全模型](../architecture/management-security.md)。

## 升级后检查

检查 `version --json` 的候选信息与 `status` 的运行版本一致，微信监听和 Codex 均健康；确认网页认证、结果读取与文件下载，再执行一条可控的真实微信任务。自动健康检查不代替[业务验收](acceptance.md)，日志位置见[日志与排障](logging.md)。
