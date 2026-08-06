# WeClaw 项目入口与 Codex 线程

## 概念

微信界面同时展示 Codex 原生概念和一个明确标注的 WeClaw 项目入口：

| 归属 | 中文名称 | 含义 | 数据来源 |
| --- | --- | --- | --- |
| WeClaw | 项目入口 | Codex 可以工作的受信任本机目录及当前选择 | WeClaw 配置与状态 |
| Codex | 线程 | 一段可持续、可恢复、可分叉的对话 | Codex 应用服务 |
| Codex | 轮次 | 线程中的一次用户输入与 Codex 执行 | Codex 应用服务 |

Codex 应用服务没有独立的“项目对象”。WeClaw 项目入口直接映射为 Codex 工作目录；微信不能提交任意路径。WeClaw 请求队列负责消息可靠落盘和串行投递，不等于 Codex 轮次。详见 [能力边界](capability-boundary.md)。

## 微信入口

发送 `/` 可以一次看到完整菜单。线程和项目的稳定编号如下：

| 编号 | 操作 |
| --- | --- |
| `11` | 新建线程 |
| `12` | 当前线程 |
| `13` | 切换线程 |
| `14` | 搜索线程 |
| `15` | 分叉当前线程 |
| `16` | 压缩上下文 |
| `17` | 设置线程目标 |
| `18` | 归档当前线程 |
| `19` | 恢复归档线程 |
| `21` | WeClaw 项目入口与切换 |
| `22` | 线程模型 |
| `23` | 推理强度 |
| `24` | 审查未提交改动 |
| `25` | 刷新 Codex 能力 |

也可以直接发送“新建线程 叫登录排障”“切换线程 登录”“分叉当前线程”“置顶当前线程”“设置线程目标为 完成发布”“线程模型”“推理强度”或“代码审查”。旧的“会话/任务”命令和英文命令别名已经删除。

## 线程生命周期

Codex 保存线程正文、名称、预览、工作目录、状态、Git 信息和归档历史。WeClaw 的 `~/.weclaw/session-index.json` 只保存微信绑定者的线程所有权、当前选择、项目归属、分叉来源以及线程级模型和推理强度；它不复制聊天正文。

创建、读取、列表、恢复、命名、归档和取消订阅分别调用 Codex 的 `thread/start`、`thread/read`、`thread/list`、`thread/resume`、`thread/name/set`、`thread/archive` 和 `thread/unsubscribe`。所有列表都先经过本地所有权过滤，其他 Codex 客户端创建的线程不会直接暴露给微信。

高级生命周期直接使用 Codex 原生能力：

- 分叉调用 `thread/fork`，复制历史并创建新线程；新线程继承源线程的模型与推理强度。
- 置顶调用 `thread/metadata/update`，不是微信侧假排序。
- 压缩调用 `thread/compact/start`，保留关键上下文并释放空间。
- 归档可恢复；永久删除调用 `thread/delete`，会同时删除所有派生线程，不能撤销。
- 线程目标调用 `thread/goal/set`、`thread/goal/get` 和 `thread/goal/clear`，与 Codex 的目标状态是同一份数据。

永久删除当前线程后，WeClaw 清空当前选择。下一条普通消息会创建新线程，不会猜测并切换到一个可能属于被删分支的后代。

## 模型与推理强度

模型目录来自 `model/list`。微信只展示模型显示名，以及“无、极低、低、中、高、极高”这些中文推理强度。设置保存在当前线程，并从下一轮执行开始生效；分叉线程继承设置。模型不再作为项目或全局微信偏好保存。

## Codex 执行能力

“Codex 执行环境”会展示 WeClaw 当前项目入口，以及 Codex 返回的线程数量、Git 分支、加载指令、启用技能和外部工具连接状态：

- `thread/start`、`thread/resume` 和 `thread/fork` 返回实际加载的指令文件路径。
- `skills/list` 返回当前项目可用的 Codex 技能。
- `mcpServerStatus/list` 只读取外部工具连接摘要，不执行工具。

提示词模板是 WeClaw 的可靠复用功能，不冒充 Codex 技能。项目技能始终来自 Codex 自己的发现结果。

## 状态展示与并发

Codex 线程状态映射为“执行中、等待确认、空闲、未加载、异常”。“WeClaw 执行状态”展示请求从排队到微信发送的完整链路；“当前线程”只展示 Codex 持久对话；Codex 轮次只在确实调用 `turn/start` 后出现，三者不混用。

Codex 轮次运行期间仍可读取项目入口和线程信息，也可发送“追加指令 …”通过 `turn/steer` 调整当前轮次。切换、分叉、压缩、归档、永久删除和代码审查需要独占 Codex 运行时；冲突时会明确拒绝，不排队偷偷执行。

## 验证

```bash
go test ./internal/codex ./internal/session ./internal/messaging
go test -race ./internal/codex ./internal/session ./internal/messaging
```

真机至少验证：新建、搜索、切换、分叉、置顶、目标、模型、推理强度、压缩、代码审查、归档、恢复和永久删除确认。
