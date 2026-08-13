# Codex 轮次与 codex-link-clawbot 请求队列

## 概念边界

用户发送一条普通微信消息时，codex-link-clawbot 先创建“请求”，而不是立即声称已经创建 Codex 轮次。请求进入持久队列并被协调器领取后，调用 `turn/start` 才会在当前 Codex 线程中创建轮次。

两个状态不能混用：Codex 拥有线程和轮次；codex-link-clawbot 拥有微信请求、请求队列和执行记录。完整定义见 [Codex 与 codex-link-clawbot 能力边界](capability-boundary.md)。

## 入站与执行

1. 文字、图片或文件先完成校验并私有落盘。
2. 来源键保证微信重投不会创建第二个 codex-link-clawbot 请求。
3. 请求冻结项目入口、Codex 线程、回答方式和视觉风格。
4. 协调器按先进先出顺序领取，设置 Codex 工作目录并恢复明确线程。
5. 线程级模型和推理强度在执行前读取，并随 `turn/start` 提交。
6. 最终正文、图片地址和交付物先冻结，再进入微信发送事务。

所有项目入口共用一个 Codex 运行时，同时最多执行一个 Codex 轮次。等待中的 codex-link-clawbot 请求不会因用户后来切换项目入口或 Codex 线程而改绑。

## 微信入口

| 编号 | 操作 |
| --- | --- |
| `31` | 查看 codex-link-clawbot 执行状态 |
| `32` | 最近执行结果 |
| `33` | 打开请求记录；页面内可暂停、继续或清空队列 |
| `34` | 取消 codex-link-clawbot 当前执行；已启动时同时请求中断 Codex 轮次 |

发送“请求队列”查看每页 6 条 codex-link-clawbot 执行记录。等待请求可以移到最前或删除；失败请求可以重试；发送阶段中断且存在冻结结果时可以只取回文字。清空队列和取消当前执行都需要确认。

Codex 轮次正在执行时发送“追加指令 先修复失败测试”，codex-link-clawbot 会调用 `turn/steer` 把内容追加到当前轮次，不创建新轮次。发送“状态”查看 codex-link-clawbot 执行进度，发送“取消”会取消当前请求；如果 Codex 轮次已经启动，同时调用 `turn/interrupt`。

## 进度与恢复

Codex 的计划、说明和受控活动会转换为简短进度；命令、输出、差异、环境变量和私有路径不会进入微信。最终回答只发送一次。

重启后的处理规则：

- 等待请求继续保留，此时还没有 Codex 轮次。
- 执行中或发送中的请求标记为中断，不自动重复 Codex 轮次或整批媒体。
- 明确发送失败与可能已经部分可见的发送结果分开记录。
- 失败和中断的私密恢复数据最多保留 24 小时。

“为什么没回复”是 codex-link-clawbot 确定性诊断，不调用 Codex。它按远程锁定、排空、Codex 就绪、微信监控、持久化、请求队列和最近发送结果给出结论。

## 成功请求续接

成功纯文字请求在私密复用副本有效期内支持继续原线程、再次执行和在新线程执行。长期规范应进入项目 `AGENTS.md` 或 Codex Skill，codex-link-clawbot 不维护另一套提示词模板状态。

## 验证

```bash
go test ./internal/taskqueue ./internal/messaging
go test -race ./internal/taskqueue ./internal/messaging
```

真机至少连续发送文字、图片和文件，验证排队、追加指令、暂停、继续、取消、重试、重启恢复和冻结文字取回。
