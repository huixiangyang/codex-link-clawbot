# 架构总览

## 产品边界

codex-link-clawbot 是个人微信到本机 Codex App Server 的单一桥接器。它不提供通用 Agent 接口、多模型路由、远程 Shell、任意工作目录或旧协议回退。所有实现包都位于 `internal/`，仓库没有承诺稳定性的公共 Go API。

本项目基于 [WeClaw](https://github.com/fastclaw-ai/weclaw) 开发改造，保留上游 MIT 许可证、原始版权声明和 Git 历史。当前代码以 `codex-link-clawbot` 作为独立项目身份，已破坏性更换模块、命令、配置、状态目录和服务命名；它不是 WeClaw 的官方版本。完整说明见[上游关系与改造边界](upstream.md)。

## 两条主链路

普通消息进入 codex-link-clawbot 持久请求链路，并在领取后启动 Codex 轮次：

```text
微信 iLink 长轮询
  → 入站身份与附件校验
  → request.json 和 inbox 原子落盘
  → 全局 FIFO Coordinator
  → Codex App Server thread / turn
  → result.json 冻结
  → 文字、阅读图、文件或音频投递
  → 最小终态与按策略清理私密负载
```

控制消息进入确定性控制链路：

```text
微信文字或数字
  → Intent Registry 唯一匹配
  → 领域 Controller
  → ActionResult 校验
  → Presenter 执行唯一副作用
  → 持久 revision 与来源回执
```

两条链路不互相伪装：控制动作不调用 Codex，普通 Codex 文本也不会被宽松规则误判为控制命令。

## 代码目录

```text
cmd/codex-link-clawbot/main.go
  └─ internal/cli
       └─ internal/app
            ├─ internal/bridge
            ├─ internal/control
            ├─ internal/execution
            ├─ internal/delivery
            ├─ internal/access
            └─ 领域状态与协议适配包
```

| 目录 | 单一职责 |
| --- | --- |
| `cmd/codex-link-clawbot` | 只创建 CLI 入口，不包含业务逻辑 |
| `internal/cli` | Cobra 命令、登录、信号、部署和离线迁移；不装配业务对象 |
| `internal/app` | 唯一组合根；一次性构造依赖、管理进程和运行循环 |
| `internal/config` | 严格版本 6 配置、Codex/codex-link-clawbot 分层、默认值和安全校验 |
| `internal/codex` | 与具体传输无关的线程、轮次、阶段和能力契约 |
| `internal/codex/appserver` | App Server 子进程、JSON-RPC、通知解析与协议适配 |
| `internal/ilink` | 微信登录、长轮询、协议客户端和游标 |
| `internal/bridge` | 微信入站、控制器适配、媒体呈现和领域服务编排 |
| `internal/control` | 控制领域、意图注册表、领域枚举和确定性冲突检查 |
| `internal/execution` | 串行执行相关状态机；真实轮次阶段去重、终态冻结与呈现节奏 |
| `internal/delivery` | 严格 v3 交付档案与受限待阅通知 |
| `internal/access` | 远程锁定和绑定者访问状态 |
| `internal/request` | codex-link-clawbot 请求的持久队列、冻结结果和生命周期 |
| `internal/thread` | Codex 线程焦点、工作空间隔离和线程级设置 |
| `internal/workspace` | 受信任工作空间白名单与绑定者当前选择 |
| `internal/preference` | 回答方式与视觉风格偏好 |
| `internal/presentation` | 回答方式、视觉风格和隐私安全的展示文本规则 |
| `internal/visual` | 固定模板、Markdown 文档模型和 Chromium 渲染 |
| `internal/management` | 仅限当前用户 Unix socket 的本机管理协议 |
| `internal/runtimecontrol` | 运行状态、排空、恢复和健康快照 |
| `internal/statefile` | 严格 JSON、原子提交、目录同步、租约和清单 |

## 依赖方向

依赖从装配层指向应用层，再指向领域状态和基础设施：

```text
cmd/codex-link-clawbot
  → cli
     → app
        → bridge / management / runtimecontrol
           → control / execution / delivery / access
              → request / thread / workspace / preference / presentation
                 → codex contracts / codex/appserver / ilink / visual / statefile / config
```

这是约束模型，不要求每个包机械地只依赖下一行；新增依赖不得形成循环，也不能让领域状态反向导入 CLI、HTTP 或模板层。

## 核心不变量

- 只有配置中的工作空间绝对路径可以成为 Codex 工作目录。
- Codex 客户端不保存可变工作目录；创建、恢复、审查和每次轮次都显式携带受信任工作空间根目录。
- `bridge.NewRuntime(Dependencies)` 是消息运行时唯一构造入口；运行期间不存在补注入 Setter 或双路径装配。
- 阶段节奏与控制意图表在组合根启动时一次性校验；请求执行期间不得补建默认依赖或延迟暴露无效配置。
- 机器级配置只从本机版本 6 配置与受控环境读取；微信只修改绑定者偏好、默认工作空间和目标线程。
- 普通输入先成为 codex-link-clawbot 请求并落盘再确认；执行与投递使用入队时冻结的项目入口、Codex 线程和偏好。
- 轮次阶段只消费当前 `turnId` 的结构化 App Server 通知；重复、跨轮次和终态后的事件不能改变请求阶段或产生微信消息。
- Codex 最终回复先冻结，再选择文字、图片、文件或语音交付，不能边生成边产生不可恢复副作用。
- 多媒体在任何内容可见前完成整批 CDN 预上传；响应不确定时不盲目重复正文。
- 所有状态按绑定者隔离，严格拒绝未知字段、尾随数据、符号链接和越界容量。
- 运行健康、部署与恢复只使用当前用户私有 Unix socket；通用主动 TCP 发送不存在。
- 模板只执行编译进二进制的结构化数据，不执行 Codex 返回的 HTML、脚本或远程资源。
- 全局工作台、功能目录、线程关系图与移动审查由控制器直接产出结构化视图；投递层不得从文字标题、分隔符或编号反向猜测页面类型。

## 放置规则

- 新二进制入口放入 `cmd/<name>`，入口只调用 `internal` 包。
- 新业务能力优先进入现有领域包；只有拥有独立状态、不变量和测试边界时才新建包。
- 测试与被测包同目录；跨包验收放在真正拥有该事务的上层包。
- 用户可观察行为变化必须同步 `docs/guides` 与 `docs/operations/acceptance.md`。
- 架构决策改变依赖方向、持久状态或失败语义时，必须同步本文件和对应架构专题。
