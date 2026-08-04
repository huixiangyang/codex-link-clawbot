# 微信长任务进度桥接

## 目标

Codex 执行本机检查、构建或部署时，微信端不再从一次“正在输入”直接静默到最终答案。桥接层持续提供可见状态，同时保证最终答案只发送一次。

## 事件链路

1. WeClaw 通过 iLink 长轮询接收扫码绑定账号的消息。
2. 文本与微信图片被整理为结构化输入；图片经下载、解密和格式校验后，以 `localImage` 与文本一起进入同一个 Codex App Server turn。
3. `turn/plan/updated` 被压缩为已完成步骤数和当前步骤。
4. `agentMessage` 的 `commentary` 阶段作为进度，`final_answer` 阶段只进入最终回复。
5. `commandExecution` 和 `fileChange` 只产生固定活动标签；终端输出、命令文本和 diff 内容不进入微信消息。
6. 任务期间每 8 秒刷新一次“正在输入”；15 秒后检查首条文字进度，此后每 45 秒只发送尚未推送过的新状态。没有新状态时不发送文字保活，也不会重复旧详情。

## 图片输入

- 仅 Codex App Server 协议接收微信图片；不支持结构化图片输入的 Agent 直接返回错误。
- 图片无需依赖 `save_dir`，统一写入 `~/.weclaw/inbox/turn-*` 私有任务目录，目录权限为 `0700`、文件权限为 `0600`。
- 单条消息最多 4 张图片，单张最大 20 MiB，仅接受按文件内容识别出的 JPEG、PNG、GIF 和 WebP。
- 有文字时按“文字 + 图片”提交；纯图片消息自动补充图片分析指令。
- turn 完成、失败或通过 `/cancel` 取消后，任务图片目录立即删除。

## 并发规则

每个微信用户只允许一个活动任务。新消息到达时若旧任务尚未结束，桥接层直接返回旧任务的最新状态和已运行时间，并明确说明新消息未执行。

该规则用于消除同一个 Codex thread 上多个 turn 竞争同一事件通道的问题。它是严格拒绝机制，不提供旧的并发行为，也不做隐式排队。

## 任务控制命令

- `/status`：随时返回任务状态、已运行时间和当前阶段；空闲时返回默认 Agent 信息。
- `/cancel`：取消当前任务。Codex App Server 模式会使用当前 `threadId` 和 `turnId` 调用 `turn/interrupt`；CLI 与 HTTP 模式通过任务上下文终止当前进程或请求。
- `/info` 与 `/help`：任务运行期间仍可查询；`/new`、`/cwd` 和 Agent 切换等状态变更命令会继续被忙碌保护拦截。

取消请求发出后，原任务的文字进度、最终答案和迟到错误都不再推送。任务完成与取消通过同一状态锁原子决胜，避免“已确认取消”后仍发送最终答案；重复发送 `/cancel` 只返回“正在取消”，不会重复提交中断。

## 配置

`~/.weclaw/config.json`：

```json
{
  "progress": {
    "enabled": true,
    "typing_interval_seconds": 8,
    "first_message_delay_seconds": 15,
    "message_interval_seconds": 45
  }
}
```

约束：

- `typing_interval_seconds`：3–30 秒。
- `first_message_delay_seconds`：5–120 秒。
- `message_interval_seconds`：15–300 秒，只控制检查和发送新文字详情的节奏，不会触发重复保活。
- 配置越界时服务拒绝启动，不做静默纠正。

## 本机部署

- 源码：`/root/CODES/weclaw-progress`
- 二进制：`/root/.local/bin/weclaw-codex-direct`
- 配置：`/root/.weclaw/config.json`
- 日志：`/root/.weclaw/weclaw.log`
- systemd 用户服务：`/root/.config/systemd/user/weclaw.service`

前台模式会登记并在退出时核对清理 `~/.weclaw/weclaw.pid`，因此 `weclaw status` 与 systemd 主进程保持一致。
systemd 使用 `Restart=always` 自动拉起桥接器，标准输出和错误继续追加到 `~/.weclaw/weclaw.log`。本机切换脚本是 `scripts/cutover-local.sh`，它会验证 systemd 状态、`/health` 端点和主动微信发送链路，并把结果写入 `~/.weclaw/cutover-status.log`。
启动顺序是 Codex App Server 握手成功后再开放健康端点和微信轮询；初始化失败时进程退出并由 systemd 重试，不再使用 echo 模式接收消息。

验证命令：

```bash
go test -race ./agent ./messaging ./config
go test ./...
go vet ./...
systemctl --user status weclaw.service
```

服务切换后，在微信发送一个预计超过 20 秒的只读检查任务。预期先看到持续输入状态，15 秒左右收到阶段消息；同一详情不重复，新阶段出现后才发送下一条文字进度，完成时收到不含中间说明的最终答案。任务期间另发“进度？”应立即收到活动任务快照。

## 安全边界

- 只接受扫码凭据中的 `ILinkUserID` 对应账号，其他联系人和群聊来源直接拒绝。
- Codex 保持本机已授权的 `danger-full-access`；该权限没有扩展到其他微信账号。
- 进度事件不包含命令输出、命令文本、diff、环境变量或终端交互内容。
- 配置中存在显式 Agent 时不运行自动发现，避免切换到未授权的备用 Agent。
