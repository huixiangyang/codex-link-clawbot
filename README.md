# WeClaw

A focused bridge from a personal WeChat account to a local Codex App Server.

WeClaw is Codex-only. It is not a general bot framework and does not provide Claude, Gemini, Kimi, multi-agent routing, a remote shell, arbitrary working directories, or legacy protocol fallbacks.

> This is not an official WeChat, Tencent, or OpenAI project. The WeChat integration is based on public iLink implementations and is intended for personal learning and self-hosted use.

[中文](README_CN.md) · [Documentation](docs/README.md) · [Roadmap](docs/roadmap.md)

## Capabilities

| Area | Current scope |
| --- | --- |
| Input | WeChat text, up to four images, restricted files, and image annotation requests |
| Codex | One App Server runtime, project allowlist, persistent threads and turns |
| Sessions | Create, search, inspect, switch, rename, archive, and restore |
| Tasks | Persistent global FIFO, pause, reorder, cancel, retry, and restart recovery |
| Workflows | Owner- and project-scoped reusable prompts with sequential parameters |
| Delivery | Adaptive text, five reading-card systems, artifacts, and paired image + MP3 voice |
| Operations | Deterministic checks, remote lock, no-reply diagnosis, draining, and transactional deployment |

Ordinary messages go to Codex. Projects, sessions, tasks, preferences, and operational controls are handled by deterministic code. Sending `/` returns one visual directory with six domains and 40 stable numeric actions.

## Data flow

```text
bound WeChat owner
  → iLink polling and attachment validation
  → persistent task queue
  → codex app-server --listen stdio://
  → frozen result
  → text / reading PNG / image / file / MP3
```

Inputs are persisted before acknowledgement. Execution uses the project, session, response mode, and visual style frozen at enqueue time. Media batches are fully staged on the WeChat CDN before the first visible send. There is no echo mode or fallback model when the App Server is unavailable.

See the [architecture overview](docs/architecture/overview.md) for package boundaries and invariants.

## Quick start

Requirements: Go 1.25+, an installed and authenticated `codex`, and a non-Snap Chromium when visual delivery is enabled.

```bash
npx playwright install chromium
go install github.com/huixiangyang/weclaw/cmd/weclaw@main
weclaw login
weclaw start
```

Configuration lives at `~/.weclaw/config.json`. At minimum, define an absolute project path Codex may enter. See the Chinese [configuration reference](docs/guides/configuration.md) for projects, visual rendering, Piper, MiMo, automations, and proactive sending.

Production runs as a systemd user service. Runtime control uses an owner-only Unix socket:

```bash
weclaw status
weclaw restart
weclaw stop
```

See [getting started](docs/guides/getting-started.md) and [deployment](docs/operations/deployment.md).

## WeChat interaction

Send `/` to open the context-aware home card. Direct Chinese phrases cover sessions, projects, the task center, workflows, response mode, visual style, automations, deliveries, voice briefings, deterministic diagnosis, and remote locking.

All legacy slash commands are removed. Any slash-prefixed text other than the single `/` is rejected and never forwarded to Codex.

Reading replies use five independent design systems: Editorial, Atelier, Noir, Cute, and Minimal. Markdown is parsed into a restricted document model with headings, paragraphs, task lists, tables, quotes, and code. Codex-provided HTML and scripts are never executed. Reply with `文字版` within 30 minutes to retrieve the full copyable answer.

Native outbound voice bubbles are not reliable on the channel, so voice delivery is explicitly a reading image plus an MP3 file, with ordered Piper and MiMo provider fallback.

## Repository layout

```text
cmd/weclaw/        the only binary entry point
internal/          all non-public implementation packages
docs/guides/       usage and configuration
docs/architecture/ code, state, and transaction boundaries
docs/operations/   deployment, migration, and acceptance
scripts/           focused helper installers
service/           systemd and launchd definitions
```

This repository is an application, not a Go library; implementation packages are intentionally internal.

## Development

```bash
make check
```

The core checks are `go test ./...`, the race detector, `go vet ./...`, and `go build ./cmd/weclaw`. CI also runs fuzz smoke tests and builds Linux amd64 and arm64 binaries.

## Security

Codex runs with `approvalPolicy: never` and `dangerFullAccess` inside allowlisted projects. A bound owner can drive local coding tools through WeChat, so bind only a trusted personal account and use a dedicated OS user with a minimal project list.

State files use strict schemas and private permissions. Runtime management is Unix-socket-only; the proactive TCP send API is disabled by default. Logs exclude message bodies, answer previews, raw owner IDs, private paths, tokens, command output, and diffs.

## License

MIT. Original copyright and Git history are retained.
