# 安装与启动

## 前置要求

- Go 1.25 或更高版本。
- 已安装并登录 `codex`，可运行 `codex app-server --listen stdio://`。
- 至少一个允许 Codex 工作的本机工作空间绝对路径。
- 启用视觉能力时，需要非 Snap Chromium；推荐 `npx playwright install chromium`。
- 启用语音能力时，需要 FFmpeg，以及至少一个 Piper 或 MiMo 提供商。

## 从源码安装

```bash
go install github.com/huixiangyang/codex-link-clawbot/cmd/codex-link-clawbot@main
```

仓库内构建使用唯一入口：

```bash
go build -o codex-link-clawbot ./cmd/codex-link-clawbot
```

根模块不再提供可安装的 `main` 包，也不存在旧入口兼容层。

## 配置

配置文件固定为 `~/.codex-link-clawbot/config.json`。先阅读 [配置参考](configuration.md)，至少确认工作空间白名单、Codex 命令和视觉浏览器。

## 登录与前台启动

```bash
codex-link-clawbot login
codex-link-clawbot start
```

首次登录显示微信二维码。codex-link-clawbot 只接受扫码凭据中绑定者的私聊消息，其他联系人和群聊直接拒绝。

`start` 始终前台运行，不创建 daemon 或 PID 文件。生产环境使用仓库中的 `service/codex-link-clawbot.service` 作为 systemd 用户服务模板。

## 运行检查

```bash
codex-link-clawbot status
codex-link-clawbot restart
codex-link-clawbot stop
```

这些命令只连接权限为 `0600` 的 `~/.codex-link-clawbot/control.sock`。正常状态至少应显示版本、Codex 就绪、微信监控正常和同步游标可提交。

## 微信首次验证

1. 发送“菜单”，确认只收到一张 1080×780 的艺术画布；最近线程与 `5`–`9` 收在顶部左侧，右上单行显示工作空间、全部线程、运行中和微信队列，线程拓扑位于遥测带下方，所有顶部内容不得超过 390 像素；其余区域以三个等宽列面板完整显示 15 个稳定数字动作，不得出现斜杠标识或 CLI、TUI、Windows、实验协议专属项。当前线程只用柔和选中态和“当前”标记识别，不得出现独立统计卡、当前目标面板或表格边框。
2. 发送一条普通问题，确认进入 codex-link-clawbot 持久请求队列，并在启动 Codex 轮次后收到回复。
3. 回复 `5` 打开全局线程，确认当前线程可见；重新发送“菜单”后直接回复 `22` 查看目标线程；回复 `8` 应直接进入工作空间，最后从线程管理页选择“重命名线程”并输入新名称。
4. 切换“阅读模式”，发送短问题，确认收到阅读图片并可回复“文字版”。
5. 如已启用语音，发送“发语音”，确认先收到阅读图，再收到 MP3 文件。

完整回归见 [微信端与部署验收清单](../operations/acceptance.md)。

## 安全提醒

Codex 使用 `approvalPolicy: never` 与 `dangerFullAccess` 在工作空间白名单中工作。绑定者能够通过微信驱动本机代码工具，因此只能绑定你信任的个人账号，并为 codex-link-clawbot 使用独立、最小化的工作空间列表和操作系统用户。
