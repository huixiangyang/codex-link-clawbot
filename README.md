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

Requirements: Go 1.25+, an installed `codex` CLI, and an authenticated Codex session.

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

Environment overrides:

- `WECLAW_API_ADDR`
- `WECLAW_SAVE_DIR`
- `WECLAW_CODEX_COMMAND`
- `WECLAW_CODEX_CWD`
- `WECLAW_CODEX_MODEL`

Configuration decoding is strict. Legacy `default_agent`, `agents`, `type`, `args`, `endpoint`, and alias fields fail startup and are not migrated at runtime.

## WeChat commands

| Command | Action |
| --- | --- |
| `/status` | Show the current Codex turn and elapsed time |
| `/cancel` | Interrupt the current turn |
| `/info` | Show App Server model, PID, and working directory |
| `/sessions [page]` | List active sessions |
| `/session` | Show the current session |
| `/session new [name]` | Create and select a session |
| `/session use <code>` | Switch sessions |
| `/session rename <name>` | Rename the current session |
| `/session archive [code]` | Archive a session |
| `/sessions archived [page]` | List archived sessions |
| `/session restore <code>` | Restore a session |
| `/cwd` | Show the Codex working directory |
| `/cwd /absolute/path` | Change the directory for later threads and turns |
| `/help` | Show commands |

The old `/new` and `/clear` commands are removed and only point users to `/session new`. Former agent-routing commands have no special meaning.

## Media and proactive delivery

- WeChat images are passed to Codex as `localImage` inputs. Limits: four images, 20 MiB each, JPEG/PNG/GIF/WebP.
- PDFs, logs, patches, archives, and common source files are treated as untrusted input. The bridge never executes or extracts them.
- Files Codex writes into the turn-specific outbox are uploaded to WeChat automatically.
- The private inbox/outbox tree is deleted after completion, failure, or cancellation.

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

## Development

```bash
go test ./...
go test -race ./codex ./messaging ./session ./config ./reporting
go vet ./...
```

More details:

- [Session management](docs/session-management.md)
- [Long-running task progress](docs/wechat-progress.md)
- [Attachments, artifacts, and reports](docs/attachments-and-reports.md)

## License

MIT. The original copyright and Git history are retained.
