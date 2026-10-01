# codex-link-clawbot

个人自托管的 Codex 远程工作台，通过微信继续本机会话、提交附件、查看进度和取回成果。

微信承担日常输入与手机操作，网页提供搜索、请求详情、文件下载和异常恢复，本机 CLI 负责配置与运维。

[English](README.md) · [文档导航](docs/README.md)

## 核心规则

- 一个实例只启用一个明确选定的微信绑定，工作空间由本机配置。
- 每条请求固定接收时的目标；同一会话忙碌则拒绝新工作，没有等待队列。
- 执行、结果保存和微信投递分别记录。发送失败不推翻执行成功，重投不重新调用 Codex。
- 配置、凭据和业务元数据统一存入 SQLite，附件保留为私有文件，诊断日志使用轮转文本文件。

不提供团队权限、任务审批、定时调度、网页工作提交或任意远程 Shell 入口。

## 从源码启动

需要 Go 1.25+，以及已安装、完成认证的 `codex`。在本仓库执行：

```bash
go build -o codex-link-clawbot ./cmd/codex-link-clawbot
./codex-link-clawbot config set visual.enabled false
./codex-link-clawbot login
./codex-link-clawbot start
```

这组最小命令关闭图片渲染，使用文字菜单与默认目录 `~/.codex-link-clawbot/workspace`。另开终端运行 `./codex-link-clawbot console` 获取管理地址和私有令牌。处理真实项目之前，先按[首次使用](docs/guides/getting-started.md)配置工作空间。

已有 JSON 状态的实例必须先完成[离线迁移](docs/operations/migration.md)，不能直接套用首次安装步骤。配置字段和可选的阅读图、语音能力见[配置指南](docs/guides/configuration.md)。

## 微信操作

| 输入 | 行为 |
| --- | --- |
| 普通文字，包含裸数字 | 提交工作；有附件草稿时补充说明 |
| `#` 或 `/` | 打开菜单，用 `#1` 至 `#6` 选择 |
| `草稿` / `提交` / `丢弃草稿` | 查看、显式提交或清空附件草稿 |
| `停止` / `取消` | 请求打断当前观察到的执行 |
| `全文 编号` / `文件 编号` | 阅读已保存文字或取回文件 |
| `继续会话 编号` | 选择结果所属的原会话 |

附件不自动执行。新用户默认完整文字，阅读图和 MP3 为可选输出方式。菜单、结果保留和异常恢复的完整规则见[工作台指南](docs/guides/workbench.md)。

## 安全与维护

当前 Codex 执行使用免审批和完整宿主访问权限。工作空间白名单约束会话选择，不是文件系统沙箱；请使用专用系统账号并只绑定可信使用者。网页管理端口只允许回环监听，远程访问须经过 HTTPS 反向代理，并继续使用管理令牌。上线前阅读[安全模型](docs/architecture/management-security.md)。

发布只提供一个 Linux 程序 `codex-link-clawbot` 及 `checksums.txt`，由固定 Ubuntu runner 原生构建，不维护多 CPU 发布设计。服务安装、事务升级及回滚边界见[部署指南](docs/operations/deployment.md)。

## 开发

```bash
make check
```

检查文档链接、格式、vet、全量 race 测试及本机构建。模块职责见[架构总览](docs/architecture/overview.md)，测试和预览方法见[开发指南](docs/guides/development.md)。文档描述当前源码，不代表某台服务器已经部署或通过[真实验收](docs/operations/acceptance.md)。

## 来源与许可

基于 [WeClaw](https://github.com/fastclaw-ai/weclaw) 改造，保留原始 [MIT 许可证](LICENSE)及版权声明。本项目独立维护，不隶属于微信、腾讯、OpenAI 或上游维护者。详见[项目来源](docs/architecture/upstream.md)。
