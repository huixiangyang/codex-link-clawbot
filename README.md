# WeClaw

A dedicated bridge from a personal WeChat account to local Codex.

This fork supports **Codex App Server only**. It has no generic agent layer, legacy ACP, `codex exec` fallback, OpenAI-compatible HTTP agent, agent discovery, aliases, switching, or multi-agent broadcast.

> This is not an official WeChat, Tencent, or OpenAI project. The WeChat integration was inspired by the public `@tencent-weixin/openclaw-weixin` implementation. Use it only for personal learning and self-hosting.

[中文文档](README_CN.md)

## Architecture

```text
Bound WeChat owner
    │ iLink polling / CDN
    ▼
WeClaw message handler
    │ stable App Server JSON-RPC
    ▼
codex app-server --listen stdio://
    ├── persistent threads and turns
    ├── local coding tools
    └── images, files, and artifacts
```

- Only messages from the account owner stored in the QR credentials are accepted.
- Every ordinary message goes to Codex; there is no provider routing.
- Each owner has an isolated session ownership index and an explicit active thread.
- Startup fails closed when the App Server handshake fails.

## Requirements and installation

Requirements: Go 1.25+, an installed `codex` CLI, an authenticated Codex session, and a non-Snap Chromium build for visual control cards. The recommended browser installation is:

```bash
npx playwright install chromium
```

```bash
go install github.com/huixiangyang/weclaw@main
weclaw login
weclaw start
```

Process commands:

```bash
weclaw status
weclaw stop
weclaw restart
```

## Configuration

`~/.weclaw/config.json`:

```json
{
  "api_addr": "127.0.0.1:18011",
  "save_dir": "",
  "progress": {
    "enabled": true,
    "typing_interval_seconds": 8,
    "first_message_delay_seconds": 15,
    "message_interval_seconds": 45
  },
  "projects": [
    {
      "id": "weclaw",
      "name": "WeClaw",
      "root": "/absolute/path/to/weclaw",
      "service_name": "weclaw.service",
      "health_url": "http://127.0.0.1:18011/health",
      "quick_tasks": [
        {"id": "review", "name": "Review changes", "prompt": "Review current changes and run the necessary tests"}
      ]
    }
  ],
  "automations": [
    {
      "id": "daily",
      "name": "Daily checks",
      "project_id": "weclaw",
      "daily_at": "09:00",
      "timezone": "Asia/Shanghai",
      "notify_on": "anomaly_or_change",
      "checks": ["git", "service", "health"],
      "commit_lookback_hours": 24
    }
  ],
  "visual": {
    "enabled": true,
    "browser_command": "",
    "long_replies": true,
    "long_reply_min_runes": 900
  },
  "codex": {
    "command": "codex",
    "model": "",
    "env": {}
  },
  "security": {"remote_lock_code": "change-this-code"},
  "voice": {
    "enabled": false,
    "base_url": "https://api.xiaomimimo.com/v1",
    "api_key": "",
    "model": "mimo-v2.5-tts",
    "voice": "茉莉",
    "style_prompt": "Read naturally and clearly at a slightly slower pace."
  }
}
```

`projects` is the only working-directory allowlist. Sessions and quick tasks are isolated per project, and roots must already exist. WeClaw always appends `app-server --listen stdio://` to `codex.command`; the removed `codex.cwd` field is rejected. An empty `model` preserves the user's Codex default. Automations support either `daily_at` or `every_minutes`, deterministic Git/service/health checks, and `always`, `anomaly`, `change`, or `anomaly_or_change` notification policies.

Voice briefings call MiMo V2.5 TTS through `/chat/completions` and are sent as native WeChat MP3 voice messages. Enabling the feature requires an HTTPS `voice.base_url` and `voice.api_key`; the key may instead be supplied through `WECLAW_MIMO_API_KEY`. The model is deliberately fixed to `mimo-v2.5-tts`. Supported preset voices are `冰糖`, `茉莉`, `苏打`, `白桦`, `Mia`, `Chloe`, `Milo`, and `Dean`. The removed `voice.command` field is rejected.

Visual controls are enabled by default and include five complete template systems: `刊物` uses paper, Chinese serif typography, and restrained red; `构筑` uses flat mineral surfaces and architectural order; `黑标` uses high-contrast black and white with champagne metal accents; `可爱` uses cream paper, rounded forms, and soft color blocks; `简洁` uses generous whitespace, hairlines, and pure information hierarchy. Each has an independent control-card and reading-card layout rather than a color swap. Send `视觉风格`, or use the main menu, to preview and switch. The choice is isolated per WeChat owner, persisted in strict v1 `~/.weclaw/visual-styles.json`, and restored after restart.

Every template follows the service's local timezone automatically: a bright theme is used from 07:00 through 18:59, and a low-glare dark theme from 19:00 through 06:59. Cards arrange short facts in two or three columns and compact actions in two columns. The interface exposes only the brand, actual content, required state, numbered actions, and useful instructions; theme labels, render time, decorative counters, watermarks, and click-like arrows are deliberately omitted. WeClaw discovers Playwright-managed Chromium or a system Google Chrome. `visual.browser_command` can select an executable by absolute path. Snap Chromium is rejected because its private mount cannot reliably access the protected render directory. Startup fails with an installation hint when visual controls are enabled but no supported browser exists.

Environment overrides:

- `WECLAW_API_ADDR`
- `WECLAW_SAVE_DIR`
- `WECLAW_CODEX_COMMAND`
- `WECLAW_CODEX_MODEL`
- `WECLAW_VISUAL_BROWSER`
- `WECLAW_MIMO_API_KEY`

Configuration decoding is strict. Legacy `default_agent`, `agents`, `type`, `args`, `endpoint`, and alias fields fail startup and are not migrated at runtime.

## WeChat interaction

The only public slash entry is `/`. It opens a context-aware numbered menu rendered as a mobile-first visual card; reply with a number to continue. Actionable cards are followed by a short text instruction so input remains convenient in WeChat. Session and scheduled-report lists use six items per page and accept both numbered navigation and the natural phrases `下一页` / `上一页`. Selecting a session from the browsing list opens its status and sanitized prompt summary before any switch or archive operation. The original search query and page survive detail navigation. Menu state expires after ten minutes, and an expired number remains ordinary Codex input.

The idle home card has only four primary actions: project, session, recent tasks, and more. While a task is active it changes to task status, pending next instruction, current session, and more. Direct phrases include `切换项目 weclaw`, `快捷任务`, `新建会话 叫登录排障`, `搜索会话 登录`, `自动化`, `素材箱`, `交付记录`, `语音简报`, `远程锁定`, `状态`, and `取消`.

The main card always identifies its bridge version. The runtime center adds bridge uptime and API listen address alongside the Codex App Server protocol, model, working directory, and process ID, with direct refresh and working-directory actions.

The task-history center keeps the newest 20 Codex turns per bound owner. It shows safe first-line summaries, start/end times, duration, and `running`, `succeeded`, `failed`, `cancelled`, or restart-interrupted outcomes. It never stores answer bodies, terminal output, attachment names, or private paths.

Successful create, switch, rename, archive, and restore results remain actionable: they link back to the current detail, list, or session center without requiring another `/`. Any ordinary text still leaves that short-lived result state and goes directly to Codex.

The automation center shows schedules, checks, notification policy, last run and delivery, and allows a deterministic manual check. Pure links saved to Linkhoard are indexed in the material library. Successful Codex artifacts are copied into a private delivery archive and can be sent again from WeChat. Image messages containing an annotation intent request a new annotated PNG artifact.

Codex replies at or above `visual.long_reply_min_runes` are parsed into safe heading, paragraph, list, quote, and code blocks, then delivered as at most ten high-density mobile reading cards. Reading cards share the owner's selected visual system and automatic day/night theme, hold more text per page, and size themselves to the actual content to reduce blank space and image count. Reply with `文字版` within 30 minutes to retrieve the complete copyable text. This phrase is always consumed locally: an expired or missing copy returns an explicit notice and never starts a Codex turn or executes an older menu. Excessively large replies, unsupported renderers, and any rendering or upload failure fall back to the full text without losing content.

All legacy slash commands are removed. Any slash-prefixed text other than the single `/` is rejected by the control layer and is never forwarded to Codex.

## Media and proactive delivery

- WeChat images are passed to Codex as `localImage` inputs. Limits: four images, 20 MiB each, JPEG/PNG/GIF/WebP.
- PDFs, logs, patches, archives, and common source files are treated as untrusted input. The bridge never executes or extracts them.
- Files Codex writes into the turn-specific outbox are uploaded to WeChat automatically.
- The private inbox/outbox tree is deleted after completion, failure, or the natural-language `取消` action.
- Control cards are rendered from fixed, escaped local templates and uploaded as PNG images. Arbitrary HTML from Codex is never executed.

```bash
weclaw send --to "user_id@im.wechat" --text "Build complete"
weclaw send --to "user_id@im.wechat" --media "https://example.com/result.png"

curl -X POST http://127.0.0.1:18011/api/send \
  -H 'Content-Type: application/json' \
  -d '{"to":"user_id@im.wechat","text":"Build complete"}'
```

## Security

- Turns use `approvalPolicy: never` and `dangerFullAccess`. The bound owner can drive Codex on the host; bind only a trusted personal account.
- `~/.weclaw/session-index.json` uses strict v3 project-scoped active threads; task history uses strict v2 receipts with project, session, duration, and token usage.
- Project selection, automations, material/delivery records, and remote-lock state use independent strict JSON stores with atomic replacement and mode `0600`.
- `~/.weclaw/visual-styles.json` uses strict v1 JSON, atomic replacement, mode `0600`, and per-owner style isolation.
- Global Codex thread results are intersected with the local ownership index before display.
- Raw terminal output, commands, diffs, and environment variables are never forwarded as progress messages.
- Runtime logs use stable SHA-256-derived labels instead of raw WeChat user/bot IDs and record only message lengths, never prompt or reply previews.
- Visual rendering disables page networking and scripts, enforces a restrictive CSP, and deletes each protected HTML/profile/PNG directory after delivery.

## Development

```bash
go test ./...
go test -race ./codex ./messaging ./session ./config ./reporting ./visual
go vet ./...
```

More details:

- [Session management](docs/session-management.md)
- [Long-running task progress](docs/wechat-progress.md)
- [Attachments, artifacts, and reports](docs/attachments-and-reports.md)
- [Visual control cards](docs/visual-controls.md)
- [v2 breaking migration](docs/migration-v2.md)
- [Acceptance checklist](docs/acceptance.md)

## License

MIT. The original copyright and Git history are retained.
