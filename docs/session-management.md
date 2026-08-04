# Codex 会话管理

## 目标

微信不再隐式绑定一个进程内 thread。每个扫码授权用户拥有独立的 Codex 会话集合、明确的当前会话和可恢复的历史选择；服务重启不会创建无关的新 thread，也不会把 Codex Desktop 或其他客户端的历史暴露到微信。

项目只存在 Codex App Server 会话，不再维护其他协议或智能体的上下文分支。

## 状态分工

Codex 是会话内容和运行元数据的唯一来源：

- thread 名称与首条消息预览
- `cwd`、创建时间、更新时间和最近活动时间
- `notLoaded`、`idle`、`active`、`systemError` 运行状态
- `waitingOnApproval` 等活动标记
- 归档后的 Codex 持久化历史

WeClaw 只保存访问控制和用户选择：

```json
{
  "version": 2,
  "owners": {
    "user-id@im.wechat": {
      "active_thread_id": "019f...",
      "threads": {
        "019f...": {
          "id": "019f...",
          "archived": false,
          "created_at": 1785830000,
          "updated_at": 1785831000,
          "last_selected_at": 1785831000
        }
      }
    }
  }
}
```

索引默认位于 `~/.weclaw/session-index.json`，目录权限为 `0700`，文件权限为 `0600`。写入使用同目录临时文件、同步和原子替换。版本不匹配、未知字段、尾随数据、当前 thread 缺失或当前 thread 已归档时，服务拒绝启动，不静默重置。

## 微信命令

| 命令 | 行为 |
| --- | --- |
| `/sessions [页码]` | 每页 6 个，按 Codex 最近活动时间倒序展示未归档会话 |
| `/session` | 展示当前会话完整 ID、短编号、名称、状态、目录和时间 |
| `/session new [名称]` | 调用 `thread/start`，可选调用 `thread/name/set`，持久化成功后切换 |
| `/session use <短编号>` | 校验归属，调用 `thread/resume` 成功后原子更新当前选择 |
| `/session rename <名称>` | 修改当前 Codex thread 名称，名称必须为单行且不超过 80 个字符 |
| `/session archive [短编号]` | 归档指定或当前会话；归档当前会话时切换到最近的可用会话 |
| `/sessions archived [页码]` | 查看当前用户拥有的已归档会话 |
| `/session restore <短编号>` | 恢复归档会话；没有当前会话时同时将它设为当前 |

短编号取 thread UUID 的末尾 8 个字符。解析至少要求 6 个字符；发生碰撞时拒绝操作并要求输入更长编号。完整 thread ID 同样必须通过归属校验。

旧 `/new` 和 `/clear` 不再是别名。收到这两个命令时只提示迁移到 `/session new`，不会执行，也不会把命令当作普通提示词发送给 Codex。

## 生命周期

普通消息进入 Codex 前执行以下流程：

1. 按微信绑定者读取当前 thread。
2. 没有当前 thread 时调用 `thread/start` 并原子写入索引。
3. 调用 `thread/read` 验证 thread 仍存在并取得最新摘要。
4. app-server 进程尚未加载该 thread 时调用 `thread/resume`。
5. 使用明确的 `threadId` 调用 `turn/start`。
6. turn 完成或失败后更新本地最近活动时间。

切换会话时，目标 thread 必须先恢复成功，本地当前选择才会改变。持久化失败时取消目标订阅并保留旧选择；成功后调用 `thread/unsubscribe` 释放旧 thread。归档和恢复的 Codex 操作与本地索引更新也包含反向补偿，避免一半成功造成无效当前选择。

## 状态展示

Codex `thread/read` 返回值是查询时的权威状态。`thread/status/changed` 通知更新进程内状态缓存，`notLoaded` 通知同时清理已加载标记。微信显示映射如下：

| Codex 状态 | 微信显示 |
| --- | --- |
| `active` | 执行中 |
| `active + waitingOnApproval` | 等待确认 |
| `idle` | 空闲 |
| `notLoaded` | 未加载 |
| `systemError` | 异常 |

任务状态与会话状态互不混用：`/status` 展示当前 turn，`/session` 展示当前 thread。

## 并发和访问控制

- `/session`、`/sessions` 是只读命令，活动 turn 中仍可执行。
- 新建、切换、重命名、归档和恢复属于状态变更，活动 turn 中直接返回任务快照。
- 同一微信用户仍只允许一个活动 turn，不排队。
- 列表分别分页调用未归档或已归档的 `thread/list`，随后只保留本地索引内已经归属当前用户的 thread ID；全局结果不会直接展示或写入日志。
- 不能依赖 Codex `sourceKinds` 做归属隔离。当前 WeClaw 与 Codex Desktop 的持久化 source 可能相同，只有本地所有权索引可阻止串会话。

## 重启和旧数据

服务启动时严格读取索引，但不会主动恢复所有 thread。第一次普通消息或显式切换时才调用 `thread/resume`，降低空闲资源占用。

v2 删除了 `owners → agents → codex` 冗余层。运行时代码不会读取或转换 v1；升级部署必须在停服窗口内把已经确认属于 WeClaw 的记录一次性写成 v2。其他 Codex Desktop、CLI 和第三方 thread 始终保持不可见。

## Codex 接口范围

实现只使用当前稳定 App Server 接口：

- `thread/start`
- `thread/read`
- `thread/list`
- `thread/resume`
- `thread/name/set`
- `thread/archive`
- `thread/unarchive`
- `thread/unsubscribe`
- `turn/start`
- `turn/interrupt`

完整 turn 历史分页和其他实验接口不进入本功能。

## 验证

```bash
go test ./...
go test -race ./codex ./messaging ./session
go vet ./...
```

真实 App Server 冒烟测试应创建专用测试 thread，依次完成改名、读取、归档、恢复、重新加载和取消订阅，最后只删除该测试 thread。部署后从微信验证列表、旧会话切换、服务重启保持当前选择、归档和恢复。
