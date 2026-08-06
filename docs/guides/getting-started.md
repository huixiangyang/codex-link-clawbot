# 安装与启动

## 前置要求

- Go 1.25 或更高版本。
- 已安装并登录 `codex`，可运行 `codex app-server --listen stdio://`。
- 至少一个允许 Codex 工作的本机项目绝对路径。
- 启用视觉能力时，需要非 Snap Chromium；推荐 `npx playwright install chromium`。
- 启用语音能力时，需要 FFmpeg，以及至少一个 Piper 或 MiMo 提供商。

## 从源码安装

```bash
go install github.com/huixiangyang/weclaw/cmd/weclaw@main
```

仓库内构建使用唯一入口：

```bash
go build -o weclaw ./cmd/weclaw
```

根模块不再提供可安装的 `main` 包，也不存在旧入口兼容层。

## 配置

配置文件固定为 `~/.weclaw/config.json`。先阅读 [配置参考](configuration.md)，至少确认项目白名单、Codex 命令和视觉浏览器。

## 登录与前台启动

```bash
weclaw login
weclaw start
```

首次登录显示微信二维码。WeClaw 只接受扫码凭据中绑定者的私聊消息，其他联系人和群聊直接拒绝。

`start` 始终前台运行，不创建 daemon 或 PID 文件。生产环境使用仓库中的 `service/weclaw.service` 作为 systemd 用户服务模板。

## 运行检查

```bash
weclaw status
weclaw restart
weclaw stop
```

这些命令只连接权限为 `0600` 的 `~/.weclaw/control.sock`。正常状态至少应显示版本、Codex 就绪、微信监控正常和同步游标可提交。

## 微信首次验证

1. 发送 `/`，确认出现不超过四项的主控制台。
2. 发送一条普通问题，确认进入持久任务并收到 Codex 回复。
3. 打开“会话”，确认当前项目与会话可见。
4. 切换“阅读模式”，发送短问题，确认收到阅读图片并可回复“文字版”。
5. 如已启用语音，发送“发语音”，确认先收到阅读图，再收到 MP3 文件。

完整回归见 [微信端与部署验收清单](../operations/acceptance.md)。

## 安全提醒

Codex 使用 `approvalPolicy: never` 与 `dangerFullAccess` 在项目白名单中工作。绑定者能够通过微信驱动本机代码工具，因此只能绑定你信任的个人账号，并为 WeClaw 使用独立、最小化的项目列表和操作系统用户。
