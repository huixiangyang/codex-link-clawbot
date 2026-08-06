# 旧生产线破坏性迁移

v2.5 不兼容旧运行状态。运行时只读取当前严格 schema，不猜测、不兜底，也不自动改写旧文件；所有必要迁移只允许在旧服务排空并完全停止后，由目标二进制的一次性离线迁移执行。

## 破坏性变化

- 删除 `task-history.json` 和独立 ActivityStore；`~/.weclaw/tasks/` 的严格 v1 索引、私有请求与冻结结果成为唯一任务来源。
- 删除单条暂存指令、按绑定者内存活动任务、五分钟内存去重和附件忙碌拒绝。
- 微信同步状态升级为严格 v1，增加 `pending_cursor` 与已消费消息回执。
- 删除 daemon、PID 文件、`--foreground`、`update`、`upgrade` 和 `scripts/cutover-local.sh`。
- `start` 永远在前台运行；生产生命周期只交给 systemd 用户服务。

v2.6 删除项目、会话、偏好、自动化、素材、交付、远程锁、同步游标和任务 JSON 各自的文件 writer，统一交给 `statefile`。此前 v2 的 `codex.cwd`、多 Agent 字段、旧语音字段和 `visual-styles.json` 继续被拒绝，不恢复兼容。

账号凭据是 v2.6 唯一需要改变磁盘 schema 的现存领域：无版本账号文件一次性升级为严格 v1。运行时只接受 `version: 1`，不会猜测或静默跳过损坏凭据。

v2.6 首次启动会创建严格 v1 `~/.weclaw/control-state.json`，用于菜单 revision 和最小控制回执。旧内存菜单没有可迁移格式，也不会双写。该文件不保存卡片正文、提示词、回答、路径、附件名、令牌或 `context_token`；损坏或未知 schema 会阻止启动。

v2.6 同时删除共享 TCP `api_addr`、`WECLAW_API_ADDR`、`start --api-addr`、TCP 健康/管理端点和无认证发送。管理面固定迁移到 `~/.weclaw/control.sock`；主动发送默认关闭，启用后使用独立 `send_api` 哈希 token 配置与严格 v1 无正文回执。

v2.7 删除 `projects[].quick_tasks` 运行时配置。离线迁移在持有独占状态锁时，把每个旧快捷任务复制到每个已绑定者对应项目的严格 v1 `~/.weclaw/workflows.json`，确认全部写入后才原子删除配置字段。工作流保存原始提示模板和参数槽，不保存 Codex 回答；没有可确定绑定者时迁移直接失败，不会把任务变成无归属的共享数据。运行时不读取或兼容旧字段。

## 自动离线迁移

`weclaw deploy` 会调用目标候选的隐藏 `migrate-state` 命令，迁移内容严格限定为：

1. 将只有 `get_updates_buf` 的旧 `accounts/*.sync.json` 原子转换为严格 v1。
2. 将已知无版本 `accounts/*.json` 凭据原子转换为严格 v1；非法或未知结构立即失败。
3. 已是合法 v1 的同步与凭据文件保持不变；未知字段、非法回执或尾随 JSON 立即失败。
4. 删除已进入事务快照的 `task-history.json` 与 `weclaw.pid`。
5. 从 `config.json` 删除旧 `api_addr` 并加入 `"send_api":{"enabled":false}`；已有新发送配置保持原样，未知配置字段仍由新运行时严格拒绝。
6. 新任务索引由 v2.5 启动时创建，不导入旧任务历史。

迁移命令不是日常修复工具，也不会在 `weclaw start` 中自动运行。服务生命周期持有 `~/.weclaw/.state.lock`；服务仍在运行时迁移必须以 `conflict` 失败，部署器停服后才可获取迁移锁。

## systemd 前提

用户服务必须有且只有一个简单 `ExecStart=<binary> start ...`。部署事务会保留其他参数，移除旧 `--foreground`，验收期间临时加入 `--draining`，成功后再移除。带引号、转义或非标准启动器的 `ExecStart` 会被拒绝，避免猜测 Shell 语义。

推荐固定路径：

```text
二进制  ~/.local/bin/weclaw
单元    ~/.config/systemd/user/weclaw.service
状态    ~/.weclaw
管理面  ~/.weclaw/control.sock
发送面  默认关闭；需要时显式配置独立监听
```

## 部署方式

不可变 GitHub 发布：

```bash
weclaw deploy v2.7.0
```

本地构建：

```bash
go build -ldflags '-X github.com/huixiangyang/weclaw/internal/cli.Version=v2.7.0-local.1' -o /absolute/path/to/weclaw ./cmd/weclaw
weclaw deploy --binary /absolute/path/to/weclaw --expect-version v2.7.0-local.1
```

候选必须通过 `version --json` 自证精确版本、`linux` 和当前 `amd64/arm64` 架构。发布模式额外按 `checksums.txt` 校验 SHA-256；本地模式记录实际 SHA-256。

完整事务顺序：

1. 校验候选，不改变运行服务。
2. 通过本机管理 socket 读取结构化健康状态，请求排空并等待所有执行、发送、staging 与同步批次结束。
3. 停止 systemd 服务，创建私有二进制、单元、配置和完整运行状态快照。
4. 运行目标二进制离线迁移，改写单元并原子安装。
5. 目标版本以排空模式启动；验收 Codex、全部微信监控、同步游标和版本。
6. 写回正常 systemd 单元，显式恢复队列并等待 `ready`。
7. 写部署收据，主动发送纯文字微信就绪通知，立即删除包含正文、附件和令牌的状态副本。

任何迁移、安装、启动或验收错误都会停止候选，恢复旧二进制、systemd 单元和完整状态快照，重新启动并验证旧健康版本。只恢复二进制而保留新 schema 不属于有效回滚。

## 首次跨越 v2.6 管理面

v2.5 使用回环 TCP 健康和管理端点，v2.6 只使用 Unix socket。按照本项目的破坏性重构约束，新二进制不会保留旧 TCP 客户端或服务器兼容分支，因此不能直接用 v2.6 的 `weclaw deploy` 驱动仍在运行的 v2.5 服务。

首次跨越必须安排维护窗口：先用当前已安装的 v2.5 二进制执行排空和停止，独立备份二进制、systemd 单元与完整 `~/.weclaw`，再安装当前候选、运行候选的离线迁移并删除单元中的 `--api-addr`，最后启动候选并用 `weclaw status` 验证 Unix 管理面。失败时必须在服务停止状态下同时恢复旧二进制、单元和完整状态备份。完成这一次切换后，后续版本重新统一使用事务 `weclaw deploy`。

当前生产会一次性跨越到包含新状态 schema 的主线，不建立未验收的线上中间态。冻结、排空、备份、迁移、真机矩阵、回滚演练和稳定观察见 [首次生产切换与真机验收](production-cutover.md)。

## 首次跨越 v2.4

事务部署要求旧进程已经支持 v2.5 结构化健康与排空协议，因此不能直接由 v2.4 的明文 `/health` 自举。首次跨越必须在维护窗口执行一次人工停机、完整备份和候选离线迁移；验证 v2.5 `ready` 后，后续版本统一使用 `weclaw deploy`。禁止为这一次跨越把旧健康、PID 猜测或强杀逻辑重新塞回运行时代码。
