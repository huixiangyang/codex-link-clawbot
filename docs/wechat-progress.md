# 微信长任务进度桥接

## 目标

Codex 执行本机检查、构建或部署时，微信端不再从一次“正在输入”直接静默到最终答案。桥接层持续提供可见状态，同时保证最终答案只发送一次。

## 事件链路

1. WeClaw 通过 iLink 长轮询接收扫码绑定账号的消息。
2. 文本、微信图片和文件被整理为结构化输入；图片以 `localImage` 进入 Codex App Server，文件以受控本机路径和元数据进入同一个 turn。
3. `turn/plan/updated` 被压缩为已完成步骤数和当前步骤。
4. `agentMessage` 的 `commentary` 阶段作为进度，`final_answer` 阶段只进入最终回复。
5. `commandExecution` 和 `fileChange` 只产生固定活动标签；终端输出、命令文本和 diff 内容不进入微信消息。
6. 任务期间每 8 秒刷新一次“正在输入”；15 秒后检查首条文字进度，此后每 45 秒只发送尚未推送过的新状态。没有新状态时不发送文字保活，也不会重复旧详情。

## 图片输入

- 图片只通过 Codex App Server 的 `localImage` 输入，不存在其他协议回退。
- 图片无需依赖 `save_dir`，统一写入 `~/.weclaw/turns/turn-*/inbox` 私有任务目录，目录权限为 `0700`、文件权限为 `0600`。
- 单条消息最多 4 张图片，单张最大 20 MiB，仅接受按文件内容识别出的 JPEG、PNG、GIF 和 WebP。
- 有文字时按“文字 + 图片”提交；纯图片消息自动补充图片分析指令。
- turn 完成、失败或通过“取消”停止后，包含入站附件和出站交付物的整个任务目录立即删除。

## 并发规则

每个微信用户只允许一个活动任务。旧任务运行时，“状态”“取消”等控制仍立即执行；新的普通文字会作为唯一一条后续指令暂存，并在当前任务结束后自动进入同一项目会话。第二条后续指令不会覆盖第一条，可先发送“清除暂存”。图片和文件不会进入暂存队列。

该规则用于消除同一个 Codex thread 上多个 turn 竞争事件通道的问题。暂存槽只有一个，不恢复旧的并发行为。

## 任务控制

- 发送“状态”随时以视觉卡片返回任务状态、已运行时间和当前阶段；卡片可继续刷新，活动任务可进入二次确认取消；空闲时同时展示 WeClaw 与 Codex 运行摘要。
- 发送“取消”使用当前 `threadId` 和 `turnId` 调用 `turn/interrupt`。
- 单独发送 `/` 打开视觉数字菜单；任务运行期间仍可查询运行信息和会话状态。
- 发送“回答方式”进入统一偏好中心；“开启语音模式”“阅读模式”“自适应模式”可直接切换，重启后保持。
- 发送“视觉风格”在刊物、构筑、黑标、可爱和简洁五套完整模板间切换；选择按绑定者隔离并同时作用于控制卡和长回复。
- 新建、切换、重命名、归档、恢复和项目切换继续被忙碌保护拦截。

取消请求发出后，原任务的文字进度、最终答案和迟到错误都不再推送。任务完成与取消通过同一状态锁原子决胜，避免确认取消后仍发送最终答案；重复发送“取消”只返回“正在取消”，不会重复提交中断。

## 持久化任务记录

主控制台直接显示记录数量，主控制台和任务状态卡片都可以进入“任务记录”，也可以直接发送“最近任务”“任务历史”或“历史任务”。列表每页 6 条，支持数字与“下一页/上一页”，详情显示摘要、状态、开始、结束和用时，并从详情返回原页。

每个绑定者最多保留 20 条记录，状态包括：

| 内部状态 | 微信显示 | 产生时机 |
| --- | --- | --- |
| `running` | 运行中 | turn 已接收且尚未结束 |
| `succeeded` | 已完成 | Codex 正常返回最终结果 |
| `failed` | 失败 | 附件准备或 Codex turn 失败 |
| `cancelled` | 已取消 | 用户取消先于任务完成生效 |
| `interrupted` | 重启中断 | 服务启动时发现上次仍为运行中的记录 |

`~/.weclaw/task-history.json` 采用严格 v2 JSON、原子替换和 `0600` 文件权限。记录包含截断摘要、项目 ID、会话 ID、时间状态和 App Server 推送的本轮 token 用量；不保存完整问题、回答、终端输出、文件名或附件路径。微信详情只展示会话短编号。

## 配置

`~/.weclaw/config.json`：

```json
{
  "progress": {
    "enabled": true,
    "typing_interval_seconds": 8,
    "first_message_delay_seconds": 15,
    "message_interval_seconds": 45
  },
  "visual": {
    "enabled": true,
    "browser_command": "",
    "long_replies": true,
    "long_reply_min_runes": 900
  }
}
```

约束：

- `typing_interval_seconds`：3–30 秒。
- `first_message_delay_seconds`：5–120 秒。
- `message_interval_seconds`：15–300 秒，只控制检查和发送新文字详情的节奏，不会触发重复保活。
- 配置越界时服务拒绝启动，不做静默纠正。
- `visual.browser_command` 为空时自动发现 Playwright Chromium 或系统 Google Chrome；设置时必须使用绝对路径。Snap Chromium 不受支持。
- `visual.long_reply_min_runes`：300–5000 个 Unicode 字符，只决定自适应模式何时从文字转为阅读卡；阅读模式始终优先卡片。
- `visual.long_replies=false` 只关闭自适应长回复卡片；语音功能开启时必须同时设置 `visual.enabled=true`。
- 五套视觉模板按服务本地时区固定切换明暗模式：07:00–18:59 为明亮主题，19:00–06:59 为深色主题；状态与短操作自动采用高密度多列布局，阅读模式每页使用 32 个视觉容量单位。
- `~/.weclaw/preferences.json` 使用严格 v1 JSON、原子替换和 `0600` 权限保存每个绑定者的回答方式与模板 ID，父目录保持 `0700`；旧 `visual-styles.json` 不再读取。

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
go test -race ./codex ./messaging ./session ./config ./reporting ./visual
go test ./...
go vet ./...
systemctl --user status weclaw.service
```

服务切换后，在微信发送一个预计超过 20 秒的只读检查任务。预期先看到持续输入状态，15 秒左右收到阶段消息；同一详情不重复，新阶段出现后才发送下一条文字进度，完成时收到不含中间说明的最终答案。任务期间另发“进度？”应立即收到活动任务快照。

## 安全边界

- 只接受扫码凭据中的 `ILinkUserID` 对应账号，其他联系人和群聊来源直接拒绝。
- Codex 保持本机已授权的 `danger-full-access`；该权限没有扩展到其他微信账号。
- 进度事件不包含命令输出、命令文本、diff、环境变量或终端交互内容。
- 运行日志不写微信用户 ID、机器人 ID、消息正文、回答预览或媒体来源；账号使用稳定短哈希标签关联排障记录。
- 配置只包含单一 `codex` 节点，未知字段和旧多 Agent 字段会使服务拒绝启动。
