# 架构总览

## 两个入口，一套内核

```text
WeChat owner ── iLink ── bridge ── durable request store ── coordinator ── Codex App Server
                                  │                                  │
                                  └──────── delivery store ◀────────┘

Browser ── HTTPS proxy/Tunnel ── loopback console ── workspace/thread/request/preference/runtime stores

Local CLI ── owner-only Unix socket ── lifecycle controller
```

微信入口只能创建 Codex 请求和读取极小连接摘要。Web 管理台调用显式 JSON API 修改本机状态。部署、停止和健康检查仍使用私有 Unix socket，避免把机器生命周期协议混入公网管理 API。

## 关键包

| 包 | 责任 |
| --- | --- |
| `internal/bridge` | 所有者校验、媒体接收、菜单链接、可靠入队、结果呈现 |
| `internal/management` | 嵌入式管理页面、令牌认证、管理 API、Unix 生命周期端点 |
| `internal/request` | 请求负载、状态机、排队和恢复 |
| `internal/thread` | Codex 全局线程目录和每个绑定者的目标引用 |
| `internal/workspace` | 受信任目录白名单和当前选择 |
| `internal/preference` | 回答模式与视觉风格 |
| `internal/delivery` | 本轮交付物私有副本和可用性 |
| `internal/runtimecontrol` | 健康、排空、恢复和运行指标 |
| `internal/codex/appserver` | 唯一 Codex 协议适配器 |
| `internal/app` | 进程组合根和生命周期 |

## 不变量

1. 只有凭据绑定者的完成态私聊消息能进入请求队列。
2. 附件在入队确认前下载、限额和内容校验；来源键保证微信重投不会重复执行。
3. 工作空间、线程和回复偏好在入队时冻结；之后的管理台切换只影响新请求。
4. 同一进程只有协调器持有 Codex 执行权。
5. 管理 TCP 服务只能绑定回环地址，所有 `/api/*` 请求都必须携带管理令牌。
6. 旧微信控制解析器、数字状态机和控制状态文件不参与运行时。

## 线程语义

Codex 是真实线程的所有者。工作空间只是本机允许微信进入的路径集合。目标线程只决定下一条微信请求送往哪里；如果没有目标，协调器在该工作空间创建线程并保存引用。
