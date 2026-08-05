# v2 破坏性迁移

v2 不兼容旧运行状态，升级必须停服并一次性迁移；运行时代码不会猜测项目，也不会自动改写旧文件。

## 配置

- 删除 `codex.cwd` 与 `WECLAW_CODEX_CWD`。
- 新增至少一个 `projects` 项；原工作目录成为项目 `root`。
- `scheduled_reports` 删除。原日报改为绑定 `project_id` 的 `automations`，并显式设置 `checks` 与 `notify_on`。
- 可选配置 `security.remote_lock_code`、`voice.ffmpeg_command` 和 `voice.providers` 有序 TTS 提供商链。出站音频统一压缩为 MP3 文件；不可用的原生 SILK 语音链、`voice.silk_command`、提供商级 `piper.ffmpeg_command`、旧扁平 MiMo 字段与 `voice.command` 均已删除。

## 状态文件

- `session-index.json`：v2 → v3。每个 thread 增加 `project_id`，单个 `active_thread_id` 改为 `active_threads[project_id]`。
- `task-history.json`：v1 → v2。已有记录可以保留，新增项目、会话和 token 字段允许为空。
- 旧 `scheduled-reports-state.json` 不再读取；新状态为 `automation-state.json`。
- 删除 `visual-styles.json`。回答方式与视觉风格合并到严格 v1 `preferences.json`，每个绑定者必须同时包含合法的 `style` 与 `response_mode`；运行时不读取或猜测迁移旧文件。

## 部署顺序

1. 停止 `weclaw.service`。
2. 备份配置、会话索引、任务记录、旧视觉风格状态与旧二进制。
3. 在临时目录生成新 JSON，使用 `jq -e` 或等价工具验证版本、项目归属和 JSON 完整性。
4. 以 `0600` 权限原子替换状态文件；如需保留旧视觉选择，显式生成 `preferences.json` 并为每个绑定者补充 `response_mode: "adaptive"`，再移走 `visual-styles.json` 和替换二进制。
5. 启动服务，验证 systemd、`/health`、微信 `/`、项目会话数量和普通 Codex turn。
6. 失败时同时恢复全部状态、配置和旧二进制；禁止只回滚其中一部分。

本机 systemd 部署可以在替换二进制后使用参数化切换脚本；脚本会依次重启、等待健康检查、核对实际版本并主动发送微信就绪通知，不再接收或手工终止旧 PID：

```bash
./scripts/cutover-local.sh v2.3.2-audio.1 'WeClaw v2.3.2-audio.1 已启动，可以继续使用。'
```

非默认服务名、二进制、状态目录或本地 API 可分别通过 `WECLAW_SERVICE_NAME`、`WECLAW_BINARY`、`WECLAW_STATE_DIR`、`WECLAW_API_URL` 覆盖。

本仓库生产实例的迁移备份位于 `~/.weclaw/backups/v2.0.0-projects.1/`。
