# 破坏性更名与状态边界

`codex-link-clawbot` 是基于 [WeClaw](https://github.com/fastclaw-ai/weclaw) 开发改造的独立项目。本次更名同时改变二进制、模块、配置、状态目录、环境变量和服务身份，不提供旧名称兼容或自动导入。

## 唯一运行身份

当前版本只读取以下身份：

```text
二进制  codex-link-clawbot
配置    ~/.codex-link-clawbot/config.json
状态    ~/.codex-link-clawbot/
服务    codex-link-clawbot.service
配置键  codex-link-clawbot
环境    CODEX_LINK_CLAWBOT_*
```

旧二进制、`~/.weclaw`、旧 systemd/launchd 标识、旧配置键和旧环境变量都不会被探测、读取、翻译或双写。

## 配置

机器配置只接受严格 `schema_version: 5`。顶层只允许：

- `schema_version`
- `codex`
- `codex-link-clawbot`

`codex-link-clawbot` 只允许 `project_entries`、`reply` 和 `security`。无版本、v2、v3、v4、旧品牌键、未知字段和尾随 JSON 全部拒绝；离线部署事务也不会把它们转换为 v5。

完整示例见[配置参考](../guides/configuration.md)。

## 当前状态 schema

项目内部仍对当前命名空间中的业务状态执行严格离线校验：

- 微信同步和账号凭据使用严格 v1；
- 控制状态使用严格 v13，v1–v12 菜单与回执直接清空；
- 交付库使用严格 v3，旧交付记录无法补齐来源时销毁；
- 项目监控、自动化、工作流模板和相关待阅通知已经删除；
- 服务运行时持有 `~/.codex-link-clawbot/.state.lock`，迁移只能在服务停止后执行。

这些规则只适用于 `~/.codex-link-clawbot` 当前状态根，不会扫描或导入 `~/.weclaw`。

## 从 WeClaw 手工切换

如需从上游或旧改造版本切换：

1. 停止旧服务并保留独立备份；
2. 安装 `codex-link-clawbot` 新二进制与新服务单元；
3. 在 `~/.codex-link-clawbot/config.json` 按 v5 结构重新配置工作空间、视觉、语音和安全；
4. 重新执行微信登录；
5. 启动新服务并运行[完整验收](acceptance.md)。

Codex 线程仍由 Codex App Server 拥有，只要实际工作目录仍位于新配置的工作空间白名单内，就会被全局线程目录重新发现。旧项目自己的请求队列、绑定者偏好、交付箱和临时菜单不导入。

## 部署事务

同一 `codex-link-clawbot` 命名空间内的后续升级使用：

```bash
codex-link-clawbot deploy <version>
```

部署器会校验候选版本和摘要、排空当前服务、创建二进制与完整状态快照、在停服状态校验 v5 配置及当前状态 schema、安装候选、验证 Codex/微信/Unix socket 健康并恢复队列。任一步失败都恢复同一命名空间的完整快照；它不会回退或迁入旧品牌运行状态。
