# 上游关系与改造边界

## 来源

`codex-link-clawbot` 基于 [WeClaw](https://github.com/fastclaw-ai/weclaw) 项目开发改造。仓库保留原始 Git 历史，根目录 [LICENSE](../../LICENSE) 保留 WeClaw 上游项目的 MIT 许可证和原始版权声明。

本项目是独立衍生项目，不是 WeClaw 的官方发行版，不代表上游项目或其维护者。项目也不隶属于微信、腾讯、OpenAI 或 Codex。

## 当前改造方向

与上游通用微信接入形态相比，当前版本把产品边界收敛为 Codex 桌面端和 CLI 的移动延续：

- 从微信查看本机 Codex 的全局线程、运行状态和工作空间边界；
- 接管目标线程并继续原生 Codex 轮次；
- 通过持久请求队列、交付箱、阅读卡和语音处理移动端交付；
- 只展示微信 Clawbot 通道能够真实执行的 Codex 命令；
- 不提供多模型路由、远程 Shell、任意目录、旧协议回退或客户端专属功能伪装。

## 独立命名

项目使用以下唯一技术身份，不提供旧名称兼容：

| 对象 | 当前值 |
| --- | --- |
| 项目与二进制 | `codex-link-clawbot` |
| Go module | `github.com/huixiangyang/codex-link-clawbot` |
| 配置与状态目录 | `~/.codex-link-clawbot` |
| 配置顶层键 | `codex-link-clawbot` |
| 环境变量前缀 | `CODEX_LINK_CLAWBOT_` |
| systemd 用户服务 | `codex-link-clawbot.service` |
| launchd 标识 | `com.huixiangyang.codex-link-clawbot` |

旧二进制、旧环境变量、旧状态目录和旧配置键不会被读取或自动迁移。需要使用当前版本时，应按新的配置结构重新初始化。

## 许可证义务

分发源码或二进制时必须继续附带根目录 MIT 许可证及其中的原始版权声明。新增改造代码仍按同一许可证发布；任何产品名称变化都不移除上游署名。
