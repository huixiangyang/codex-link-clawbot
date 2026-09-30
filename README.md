# codex-link-clawbot

Connect a personal WeChat account to Codex with single-digit menus designed for small phone screens.

The bridge has one narrow job: accept text, images, and files from the bound owner; durably route them to the selected Codex workspace and thread; and return the result and artifacts to WeChat. Numbered WeChat menus handle everyday conversations, workspaces, live sessions, results, preferences, and locking. The web console provides search, detailed management, and runtime maintenance.

> This is not an official WeChat, Tencent, or OpenAI project. The iLink integration is intended for personal, self-hosted use.

> This project is derived from [WeClaw](https://github.com/fastclaw-ai/weclaw) and retains its MIT license, original copyright notice, and Git history. See the [upstream relationship](docs/architecture/upstream.md).

[中文](README_CN.md) · [Documentation](docs/README.md)

## Product boundary

| Surface | Responsibility |
| --- | --- |
| WeChat | `0` or `菜单` opens numbered menus for conversations, workspaces, requests, results, and preferences; ordinary content becomes a Codex request |
| Web console | Workspace and thread targeting, live session controls, response preferences, deliveries, drain/resume, and remote lock |
| Local CLI | Login, process lifecycle, deployment, and retrieval of the console URL and token |

The new menu uses single digits and four-item pages. Legacy multi-digit commands, natural-language control intents, and old menu state files have no compatibility path.

## Data flow

```text
bound WeChat owner
  → private-message and attachment validation
  → immediate per-session admission (busy → reject)
  → Codex App Server
  → frozen result and artifacts
  → WeChat text / reading images / files / MP3

browser
  → management token
  → 127.0.0.1:18120 management API
  → threads / workspaces / live sessions / settings
```

Inputs are persisted before acknowledgement. Execution uses the workspace, thread, and presentation preferences captured at acceptance. There is no fallback protocol or model when Codex App Server is unavailable.

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

Send `0`, `菜单`, `Codex`, or `Codex Link` to open the menu. Choose `1–6` on the home page; lists show up to four entries. Use `7/8` to page, `0` for home, and `9` to exit. Ordinary content exits the menu and becomes a request. Unlock input is handled separately and never sent to Codex.

## Security

The web API requires a 256-bit token, sets a strict CSP, rejects framing, and cannot bind to a public interface. The process still uses an owner-only Unix socket for local lifecycle operations. A bound owner can drive local Codex tools, so use a dedicated OS account and a minimal workspace allowlist.

## Development

```bash
make check-fast
make check
```

CI runs the same `make check` target. See the [development guide](docs/guides/development.md), [architecture](docs/architecture/overview.md), [refactoring decisions](docs/architecture/refactoring.md), and [acceptance checklist](docs/operations/acceptance.md).

## License

MIT.

## Personal workbench

Menus send one compact numbered image, with matching text as a fallback. Numbers bind to the displayed IDs, expire after ten minutes, and cannot silently select a different task when lists change. Long saved answers use 500-character pages. See the [menu design](docs/design/wechat-menu.md).

The first request establishes a persistent conversation intent, shared by subsequent messages. Each active request keeps its original workspace, target, and preferences. A second command to a busy session is rejected immediately with an action menu; it is never deferred. Users can interrupt that execution or switch to another session. Execution completion and WeChat delivery have separate outcomes; saved answers and artifacts remain accessible from the authenticated console even when delivery fails.

Inputs are retained for 24 hours after completion, results for 7 days, and records for 30 days. Explicit retry creates a linked request; redelivery sends the frozen result without rerunning Codex. Only one explicitly selected WeChat binding is active.

See the [workbench guide](docs/guides/workbench.md) and [offline migration instructions](docs/operations/migration.md). Existing installations must migrate to request index v4, input manifest v2 and result format v2 before startup.
