# Codex 工作空间与全局线程

## 产品模型

codex-link-clawbot 是 Codex 桌面端在微信中的远程延续，不是某个微信会话的线程容器。

| 概念 | 含义 | 事实来源 |
| --- | --- | --- |
| Codex 工作空间 | 微信端允许查看、接管和执行工作的本机目录白名单 | codex-link-clawbot 配置 |
| Codex 全局线程 | 桌面端、CLI、IDE 和 App Server 创建的持久对话 | Codex App Server `thread/list` |
| 目标线程 | 当前微信用户下一次上下文操作的远程焦点 | codex-link-clawbot `session-index.json` |
| Codex 轮次 | 目标线程中的一次输入与执行 | Codex App Server |
| codex-link-clawbot 请求 | 等待可靠投递的微信输入 | codex-link-clawbot 请求队列 |

Codex App Server 没有“项目对象”，只有线程的 `cwd`。codex-link-clawbot 对每个线程工作目录做真实路径解析，只允许落在配置工作空间中的线程进入全局目录；路径不存在、解析失败、越出白名单或命中符号链接逃逸时直接拒绝。嵌套工作空间按更具体的根目录归类。

## 全局发现与目标接管

全局目录直接调用 `thread/list`，不依赖 `session-index.json` 判断可见性，也不传固定 `sourceKinds`，因此不会漏掉桌面端、CLI、IDE 或未来新增来源。搜索词由 App Server 处理，再应用工作空间白名单。运行中页面只保留 `active` 线程；归档页面单独查询归档目录。

选择线程时，codex-link-clawbot 会再次调用 `thread/read` 校验工作目录，然后调用 `thread/resume` 加载线程，将所属工作空间设为默认工作空间，并把该线程记为远程目标。`session-index.json` 只保存焦点、队列关联、分叉来源和线程级执行设置，不复制聊天正文，也不拥有 Codex 历史。

`thread/loaded/list` 只描述当前 codex-link-clawbot App Server 进程已经加载的线程。其他 Codex 客户端的“正在运行”状态以 `thread/list` 返回状态为准；codex-link-clawbot 不伪造跨进程暂停、中断或实时控制能力。

## 全局工作台

发送 `/` 时，codex-link-clawbot 直接读取全局 `thread/list`，按最近活动时间展示最多四项；标题、工作空间、状态和相对时间都来自本次快照，不写入菜单文件。当前目标固定在顶部，并在最近列表自然命中时追加标记。回复 `1`–`4` 会消费本版快照、重新读取线程并复核工作空间，然后才恢复并接管目标。

| 首页编号 | 操作 |
| --- | --- |
| `1`–`4` | 接管本版图片中的最近线程 |
| `5` | 全部线程 `/resume` |
| `6` | 新建线程 `/new` |
| `7` | codex-link-clawbot 执行与队列 |
| `8` | 工作空间 |
| `9` | 重新读取全局状态 |

## 首页稳定直达

以下编号不再要求先进入二级目录，发送 `/` 后可直接回复：

| 编号 | 中文操作 | 命令或范围 |
| --- | --- | --- |
| `11` | 全局总览 | 工作空间、活动、运行、加载、归档与目标线程 |
| `12` | 全局线程 | `/resume`；中心内包含运行中、全部、搜索与归档 |
| `13` | 账号与额度 | `/usage` |
| `21` | 工作空间 | 选择默认执行目录 |
| `22` | 目标线程 | `/status` |
| `23` | 模型与权限 | `/model`、`/permissions` |
| `24` | 技能与工具 | `/skills`、`/mcp` |
| `25` | 微信可用命令 | 17 个原生或适配命令 |
| `31` | 新建工作 | `/new [名称]` |
| `32` | 审查改动 | `/review` |
| `33` | 请求队列 | codex-link-clawbot 持久投递状态 |
| `34` | 取消执行 | 独立确认 |

## 斜杠命令作用域

- 全局命令：`/resume`、`/usage`、`/skills`、`/mcp`。
- 目标线程命令：`/status`、`/rename`、`/archive`、`/delete`、`/fork`、`/compact`、`/goal`、`/review` 与模型/推理强度修改。
- 新工作命令：`/new`、适配后的 `/clear`。
- 客户端边界命令：不进入微信首页、可用命令目录或文字降级菜单，也不转成普通提示词。

模型目录来自 `model/list`，账号来自 `account/read`，额度来自 App Server 推送的 rate limit 快照，技能按每个工作空间调用 `skills/list` 后去重汇总，MCP 就绪摘要来自 `mcpServerStatus/list`。微信端对账号和执行权限只读，不允许退出登录、修改凭据、放宽沙箱或变更审批策略。

## 并发与队列

普通微信输入入队时冻结工作空间和目标线程。用户之后切换目标，不会改变已经排队的请求。Codex 轮次运行期间仍可读取全局目录和状态，但切换目标、分叉、压缩、归档、永久删除与审查需要独占运行时；冲突时明确拒绝。

`/review` 默认以 inline 方式审查当前目标线程的未提交改动。结果进入专用移动审查卡；“继续修复”和“重新审查”都保存审查时的工作空间 ID 与线程 ID，不会因用户随后切换界面焦点而漂移。“接受结论”只结束本次移动审查交互，不执行提交、推送或部署。

## 验证

```bash
go test ./internal/codex ./internal/session ./internal/messaging
go test -race ./internal/codex ./internal/session ./internal/messaging
```

真机至少验证：全局总览、跨客户端线程发现、运行中筛选、跨工作空间搜索与接管、越界路径拒绝、账号与额度、全局技能/MCP、目标线程状态、新建、审查、归档、恢复和永久删除确认。
