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
  "visual": {
    "enabled": true,
    "browser_command": ""
  },
  "codex": {
    "command": "codex",
    "cwd": "/absolute/path/to/project",
    "model": "",
    "env": {}
  },
  "scheduled_reports": []
}
```

WeClaw always appends `app-server --listen stdio://` to `codex.command`. An empty `cwd` uses `~/.weclaw/workspace`; a configured path must be absolute. An empty `model` preserves the user's Codex default.

Visual controls are enabled by default. WeClaw discovers Playwright-managed Chromium or a system Google Chrome. `visual.browser_command` can select an executable by absolute path. Snap Chromium is rejected because its private mount cannot reliably access the protected render directory. Startup fails with an installation hint when visual controls are enabled but no supported browser exists.

Environment overrides:

- `WECLAW_API_ADDR`
- `WECLAW_SAVE_DIR`
- `WECLAW_CODEX_COMMAND`
- `WECLAW_CODEX_CWD`
- `WECLAW_CODEX_MODEL`
- `WECLAW_VISUAL_BROWSER`

Configuration decoding is strict. Legacy `default_agent`, `agents`, `type`, `args`, `endpoint`, and alias fields fail startup and are not migrated at runtime.

## WeChat interaction

The only public slash entry is `/`. It opens a context-aware numbered menu rendered as a mobile-first visual card; reply with a number to continue. Actionable cards are followed by a short text instruction so input remains convenient in WeChat. Menu state expires after two minutes, and an expired number remains ordinary Codex input.

Controls also accept direct natural-language phrases, including `新建会话 叫登录排障`, `切换会话 登录`, `当前会话`, `会话列表`, `工作目录`, `状态`, and `取消`. Session lookup supports exact, prefix, substring, and ordered-character fuzzy matching. A unique match runs immediately, multiple matches become numbered candidates, and archive requires confirmation.

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
- `~/.weclaw/session-index.json` uses the strict v2 schema, atomic replacement, and mode `0600`.
- Global Codex thread results are intersected with the local ownership index before display.
- Raw terminal output, commands, diffs, and environment variables are never forwarded as progress messages.
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

## License

MIT. The original copyright and Git history are retained.
