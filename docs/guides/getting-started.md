# 安装与首次绑定

## 依赖

- Go 1.25 或更新版本；
- 已安装并完成账号认证的 `codex`；
- 可访问微信 iLink 服务；
- 视觉回复启用时，安装非 Snap Chromium；
- 语音模式启用时，安装 FFmpeg 和至少一个 TTS 提供商。

## 安装与扫码

```bash
go install github.com/huixiangyang/codex-link-clawbot/cmd/codex-link-clawbot@main
codex-link-clawbot login
```

终端显示二维码后，用准备绑定的个人微信扫码并在手机确认。凭据写入私有账号目录。服务只接受凭据中 `ilink_user_id` 对应绑定者的私聊消息。

## 配置并启动

先按[配置指南](configuration.md)设置工作空间，再启动：

```bash
codex-link-clawbot config
codex-link-clawbot start
```

另开终端读取管理入口：

```bash
codex-link-clawbot console
```

打开输出的 URL，粘贴令牌。管理页面成功连接后应显示微信监听器、Codex App Server、当前工作空间和目标线程。

## 首条消息

在微信发送 `菜单` 验证连接摘要；再发送一个真实任务，例如“检查当前项目的测试失败原因”。第二条消息应先收到可靠入队确认，随后收到 Codex 结果。

不要用旧数字菜单验证。`1`、`状态` 和 `取消` 已经是普通 Codex 输入。
