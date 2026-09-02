# codex-link-clawbot

Connect a personal WeChat account to Codex while keeping every management action in a separate self-hosted web console.

The bridge has one narrow job: accept text, images, and files from the bound owner; durably route them to the selected Codex workspace and thread; and return the result and artifacts to WeChat. Threads, queue state, workspaces, presentation preferences, runtime controls, and the remote lock are managed in the web console, not through chat commands.

> This is not an official WeChat, Tencent, or OpenAI project. The iLink integration is intended for personal, self-hosted use.

> This project is derived from [WeClaw](https://github.com/fastclaw-ai/weclaw) and retains its MIT license, original copyright notice, and Git history. See the [upstream relationship](docs/architecture/upstream.md).

[中文](README_CN.md) · [Documentation](docs/README.md)

## Product boundary

| Surface | Responsibility |
| --- | --- |
| WeChat | `菜单`, `Codex`, or `Codex Link` shows connection details; every other text, image, or file becomes a Codex request |
| Web console | Workspace and thread targeting, queue actions, response preferences, deliveries, drain/resume, and remote lock |
| Local CLI | Login, process lifecycle, deployment, and retrieval of the console URL and token |

The former numeric menu, natural-language control intents, chat diagnostics, chat cancellation/retry, and persisted menu state have been removed. There is no compatibility path.

## Data flow

```text
bound WeChat owner
  → private-message and attachment validation
  → durable request queue
  → Codex App Server
  → frozen result and artifacts
  → WeChat text / reading images / files / MP3

browser
  → management token
  → 127.0.0.1:18120 management API
  → threads / workspaces / queue / settings
```

Inputs are persisted before acknowledgement. Execution uses the workspace, thread, and presentation preferences frozen at enqueue time. There is no fallback protocol or model when Codex App Server is unavailable.

## Quick start

Requirements: Go 1.25+, an installed and authenticated `codex`, and a non-Snap Chromium when visual delivery is enabled.

```bash
go install github.com/huixiangyang/codex-link-clawbot/cmd/codex-link-clawbot@main
codex-link-clawbot login
codex-link-clawbot start
codex-link-clawbot console
```

The first start creates a random `0600` token at `~/.codex-link-clawbot/management-token`. The `console` command prints the URL, token, and token path. Treat the token as a secret; the browser keeps it only in the current tab's `sessionStorage`.

Configuration uses schema version 7. The management server must listen on loopback, normally `127.0.0.1:18120`. Publish it through an HTTPS reverse proxy or Cloudflare Tunnel and set `management.public_url` to the resulting URL. See the Chinese [configuration guide](docs/guides/configuration.md).

## WeChat behavior

`菜单`, `Codex`, and `Codex Link` return a compact connection summary with the current workspace, target thread, and console URL. Everything else—including `状态`, `取消`, numeric input, and former menu phrases—is sent to Codex as ordinary user input.

## Security

The web API requires a 256-bit token, sets a strict CSP, rejects framing, and cannot bind to a public interface. The process still uses an owner-only Unix socket for local lifecycle operations. A bound owner can drive local Codex tools, so use a dedicated OS account and a minimal workspace allowlist.

## Development

```bash
make check
```

See the [architecture](docs/architecture/overview.md), [deployment guide](docs/operations/deployment.md), and [acceptance checklist](docs/operations/acceptance.md).

## License

MIT.
