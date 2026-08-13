# Codex 与 codex-link-clawbot 能力边界

codex-link-clawbot 是把个人微信接到本机 Codex 的交付与控制层，不是 Codex 的另一个名字。微信菜单、状态卡和文档必须明确标注能力归属。

## Codex 原生能力

以下状态和操作由 Codex 应用服务拥有，codex-link-clawbot 只负责在微信中展示和调用：

| 概念 | 能力 | 事实来源 |
| --- | --- | --- |
| 全局线程 | 跨桌面端、CLI、IDE 与 App Server 的创建、恢复、搜索、命名、分叉、置顶、压缩、归档、删除 | Codex `thread/*` 接口 |
| 轮次 | 在某个线程中执行一次输入、追加指令、中断 | Codex `turn/*` 接口 |
| 模型与推理 | 模型目录、线程模型、推理强度 | Codex `model/list` 与轮次参数 |
| 审查 | 审查当前工作目录的未提交改动 | Codex 审查接口 |
| 账号、技能与外部工具 | 账号与额度、按工作空间发现技能、外部工具连接状态 | Codex `account/*`、技能与外部工具接口 |

Codex 应用服务没有 codex-link-clawbot 配置中的“项目对象”。Codex 只接收工作目录。菜单里的“Codex 工作空间”是受信任目录白名单和默认执行目录；目标线程只是远程焦点，不决定线程可见性。

微信全局目录直接读取 Codex `thread/list`，不按微信用户或本地索引过滤，再按线程真实工作目录应用白名单。`thread/loaded/list`、`account/read`、`model/list`、`skills/list` 和 `mcpServerStatus/list` 提供全局控制信息。

## codex-link-clawbot 增强能力

以下能力由本系统实现，不应称为 Codex 原生功能：

| 能力 | 作用 |
| --- | --- |
| 微信接入 | 接收文字、图片和受限文件，向绑定者发送结果 |
| 工作空间与目标焦点 | 限制微信可管理的本机目录，并保存默认工作空间和目标线程 |
| 请求队列 | 在 Codex 轮次创建前可靠落盘、去重、排队、暂停和恢复 |
| 执行记录 | 保存请求的排队、Codex 执行和微信发送状态 |
| 回复呈现 | 文字、阅读图、视觉风格、图片、文件和语音配对交付 |
| 结果与交付 | 最近成功结果、交付箱、再次发送和按需语音播报 |
| 运行与安全 | 无回复诊断、远程锁定、排空、部署和状态检查 |

## 请求不等于轮次

一条普通微信消息先成为 codex-link-clawbot 请求。只有队列协调器领取它，并调用 Codex `turn/start` 后，才产生 Codex 轮次：

```text
微信消息
  → codex-link-clawbot 请求
  → codex-link-clawbot 请求队列
  → Codex 轮次
  → codex-link-clawbot 冻结结果与微信交付
```

因此：

- “等待中”表示 codex-link-clawbot 请求尚未创建 Codex 轮次。
- “运行中”表示请求已经启动 Codex 轮次。
- “发送中”表示 Codex 轮次已经结束，codex-link-clawbot 正在交付结果。
- “追加指令”只作用于正在运行的 Codex 轮次。
- “暂停队列”只影响 codex-link-clawbot 后续请求，不改变 Codex 的线程或历史。

## 微信菜单分区

| 分区 | 归属 |
| --- | --- |
| `1` Codex · 全局 | 总览、统一全局线程 `/resume`、账号与额度 `/usage` |
| `2` Codex · 工作空间 | 工作空间、目标线程 `/status`、模型权限、技能工具、微信可用命令 |
| `3` Codex · 执行 | 新建 `/new`、审查 `/review`、请求队列和取消 |
| `4` codex-link-clawbot · 远程 | 最近结果与交付箱、系统健康与诊断、呈现与安全 |

内部持久标识也遵守同一边界：真正的 Codex 轮次使用 `turn.*`，codex-link-clawbot 队列使用 `queue.*`，Codex 线程使用 `thread.*`。Codex 斜杠输入先由固定注册表解析，不能执行的客户端专属项返回能力边界，未知项不会下沉成普通提示词。旧的队列 `turn.*` 控制状态不会兼容读取。
