# Codex 会话管理

## 目标

微信不再隐式绑定一个进程内 thread。每个扫码授权用户在每个配置项目中拥有独立的 Codex 会话集合、当前会话和可恢复的历史选择；切换项目不会串用 thread，也不会暴露 Codex Desktop 或其他客户端的历史。

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
  "version": 3,
  "owners": {
    "user-id@im.wechat": {
      "active_threads": {"weclaw": "019f..."},
      "threads": {
        "019f...": {
          "id": "019f...",
          "project_id": "weclaw",
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

## 微信会话式控制

单独发送 `/` 打开视觉主菜单。先在“项目中心”选择受信任项目，再进入项目独立的会话中心。任意微信文本都不能提供本机路径；目录只来自 `projects` 白名单。会话列表、搜索、新建、重命名、归档和恢复继续使用十分钟状态机、分页、返回位置和二次确认。

会话详情只展示便于手机识别的短编号，不展示冗长完整 thread ID；所有读取仍以完整 ID 在内部完成。创建、切换、重命名、归档和恢复的成功卡片提供后续管理入口，普通文字则自动离开该短期状态并进入刚选中的 Codex 会话。

同一能力也接受高置信度中文意图，例如“新建会话 叫登录排障”“搜索会话 登录”“切换会话 登录”“重命名当前会话 为发布检查”和“归档当前会话”。“切换会话”表达了明确变更意图，唯一匹配可直接执行；“搜索会话”永远先展示候选详情。控制层只拦截完整意图；选择菜单时收到非数字普通文字会清理菜单状态并继续路由，不会静默吞消息。

切换和恢复支持服务端补全：先按名称、末尾短编号和完整 thread ID 进行精确匹配，再依次尝试前缀、包含和字符顺序匹配。唯一候选直接执行，多候选按匹配质量和最近活动顺序分页，每页 6 条；导航与会话选项合计最多 8 个数字入口，也接受“下一页”“上一页”。所有候选都先经过本地所有权索引过滤。

短编号仍取 thread UUID 的末尾 8 个字符。旧斜杠命令全部删除；只有单独的 `/` 是控制入口，其他斜杠内容会被明确拒绝。

自动创建或显式跳过命名的会话，会在第一条普通消息到达时使用用户输入首行生成最多 36 字的名称；纯图片和纯文件任务分别使用“图片分析”和文件名摘要。候选标题永远优先使用 Codex thread 名称，回退到 preview 时会剔除 `[WeClaw 入站文件]`、`[WeClaw 交付物回传]` 和后续私有路径，避免内部提示污染管理界面。Codex 返回的置顶状态会显示在会话列表和详情中。

## 生命周期

普通消息进入 Codex 前执行以下流程：

1. 按微信绑定者读取当前项目，并把 Codex 工作目录设为项目根目录。
2. 读取该用户在该项目中的当前 thread。
3. 没有当前 thread 时调用 `thread/start` 并原子写入索引。
4. 调用 `thread/read` 验证 thread 仍存在并取得最新摘要。
5. app-server 进程尚未加载该 thread 时调用 `thread/resume`。
6. 使用明确的 `threadId` 和项目目录调用 `turn/start`。
7. turn 完成或失败后更新本地最近活动时间。

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

任务状态与会话状态互不混用：“状态”展示当前 turn，“当前会话”展示当前 thread。

## 并发和访问控制

- 菜单、当前会话和会话候选属于只读控制，活动 turn 中仍可执行。
- 新建、切换、重命名、归档、恢复和项目切换属于状态变更，活动 turn 中明确拒绝。
- 菜单状态按微信用户隔离并在十分钟后失效；有效数字选择通过原子比较删除，重复到达不会执行两次。任何普通文字和“文字版”取回都会清理旧菜单状态。
- 同一微信用户仍只允许一个活动 turn，不排队。
- 列表分别分页调用未归档或已归档的 `thread/list`，随后只保留本地索引内已经归属当前用户的 thread ID；全局结果不会直接展示或写入日志。
- 不能依赖 Codex `sourceKinds` 做归属隔离。当前 WeClaw 与 Codex Desktop 的持久化 source 可能相同，只有本地所有权索引可阻止串会话。

## 重启和旧数据

服务启动时严格读取索引，但不会主动恢复所有 thread。第一次普通消息或显式切换时才调用 `thread/resume`，降低空闲资源占用。

v3 引入 `project_id` 和按项目索引的 `active_threads`。运行时代码不会读取或转换 v1/v2；升级部署必须在停服窗口内把已有记录一次性归入明确项目。其他 Codex Desktop、CLI 和第三方 thread 始终保持不可见。

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
