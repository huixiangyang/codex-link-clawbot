# codex-link-clawbot

A global Codex remote workbench for a personal WeChat account—the mobile continuation of local Codex clients.

codex-link-clawbot is Codex-only. It is not a general bot framework and does not provide Claude, Gemini, Kimi, multi-agent routing, a remote shell, arbitrary working directories, or legacy protocol fallbacks.

> This is not an official WeChat, Tencent, or OpenAI project. The WeChat integration is based on public iLink implementations and is intended for personal learning and self-hosted use.

> Upstream notice: this project is developed from [WeClaw](https://github.com/fastclaw-ai/weclaw) and retains its MIT license, original copyright notice, and Git history. `codex-link-clawbot` is an independent derivative focused on global Codex management through a WeChat Clawbot; it is not an official WeClaw release and does not represent its maintainers.

[中文](README_CN.md) · [Documentation](docs/README.md) · [Roadmap](docs/roadmap.md)

## Capabilities

| Owner | Current scope |
| --- | --- |
| Codex | Global threads and one-level fork relations across Desktop, CLI, IDE, and App Server clients; actual turns, account, models, review, skills, and tools |
| codex-link-clawbot | WeChat text, up to four images, restricted files, and image annotation requests |
| codex-link-clawbot | Allowlisted workspaces, a remote target-thread focus, durable request queue, real turn-phase progress, cancellation, retry, and restart recovery |
| codex-link-clawbot | Successful-request continuation, recent results, source-bound and checksum-verified private deliveries, explicit expiry, and resend |
| codex-link-clawbot | Adaptive text, five visual systems, mobile review packages, artifacts, and paired image + MP3 voice |
| codex-link-clawbot | Deployment and recovery notices, remote lock, no-reply diagnosis, draining, and transactional deployment |

Codex owns the global thread catalog, turns, account, models, review, skills, and external tools. codex-link-clawbot adds the WeChat transport, workspace allowlist, target-thread focus, request queue, presentation, deliveries, and operations. The `/` home card provides 19 stable entries across Codex Global, Codex Workspaces, Codex Execution, and codex-link-clawbot Remote. Every mapped Codex action is rendered as a Chinese label together with its canonical slash command.

`/review` keeps Codex's native inline verdict while adding deterministic mobile facts: aggregate worktree counts, structured verification outcomes from the latest relevant turn, and delivery availability for the same thread. Raw diffs, commands, terminal output, filenames, and private paths never enter the card. Archived files are rebound to their workspace, Codex thread, and codex-link-clawbot request, and their SHA-256 is verified before resend; an invalid copy is shown as unavailable and never triggers an implicit rerun.

Codex App Server has no project object: a codex-link-clawbot workspace is an allowlisted directory boundary. Thread visibility comes directly from Codex and is then filtered by real working directory; the selected target thread is only a remote-operation focus. A WeChat message is first a codex-link-clawbot request and becomes a Codex turn only after dispatch. See the [capability boundary](docs/guides/capability-boundary.md).

## Data flow

```text
bound WeChat owner
  → iLink polling and attachment validation
  → codex-link-clawbot persistent request queue
  → codex app-server --listen stdio://
  → frozen result
  → text / reading PNG / image / file / MP3
```

Inputs are persisted before acknowledgement. Execution uses the project, thread, response mode, and visual style frozen at enqueue time. Thread-scoped model and reasoning settings are resolved before the turn starts. Media batches are fully staged on the WeChat CDN before the first visible send. There is no echo mode or fallback model when the App Server is unavailable.

Deployment completion events and long-task recovery reminders are stored as private deferred notices instead of being sent with stale message tokens. Up to four are delivered on the owner's next valid interaction; no WeChat context token is persisted.

See the [architecture overview](docs/architecture/overview.md) for package boundaries and invariants.

## Quick start

Requirements: Go 1.25+, an installed and authenticated `codex`, and a non-Snap Chromium when visual delivery is enabled.

```bash
npx playwright install chromium
go install github.com/huixiangyang/codex-link-clawbot/cmd/codex-link-clawbot@main
codex-link-clawbot login
codex-link-clawbot start
```

Configuration lives at `~/.codex-link-clawbot/config.json`, uses schema version 6, and separates `codex` from `codex-link-clawbot`. Long-task progress follows structured App Server turn and item notifications; timed “still running” repeats are not generated. At minimum, define an absolute workspace path Codex may enter. Run `codex-link-clawbot config` for a redacted effective summary. See the Chinese [configuration and settings reference](docs/guides/configuration.md) for workspaces, visual rendering, Piper, and MiMo.

Production runs as a systemd user service. Runtime control uses an owner-only Unix socket:

```bash
codex-link-clawbot status
codex-link-clawbot restart
codex-link-clawbot stop
```

See [getting started](docs/guides/getting-started.md) and [deployment](docs/operations/deployment.md).

## WeChat interaction

Send `/` to open the 1080×780 Codex global workbench. Recent threads appear on the left; workspace, total-thread, running-thread, and request-queue telemetry fills the upper right; the lower area directly exposes all 17 WeChat-usable commands. `1`–`4` adopt recent threads, `5` opens `/resume`, `6` starts `/new`, `7` opens execution and queue state, `8` opens workspaces, and `9` refreshes. Stable `11`–`43` actions are also available directly from the home state, with no secondary function directory. “Current target” means where the next WeChat input goes and is deliberately separate from Codex running state.

Send `线程关系图` to render the current target's native parent and direct children as a single 1080×1180 map. It never reads conversation bodies or caches a second thread tree. Numbered nodes are frozen for ten minutes, and every adoption re-reads the thread and revalidates its trusted workspace.

codex-link-clawbot recognizes the complete current [official Codex CLI slash-command catalog](https://learn.chatgpt.com/docs/developer-commands.md?surface=cli): 49 canonical commands plus five aliases. The WeChat interface only displays the 17 commands with stable App Server or remote-adapter implementations. TUI-, Windows-, and experimental-only commands are excluded from the workbench, available-command catalog, and text fallback. Unknown slash-prefixed text is rejected and never forwarded as a Codex prompt.

Reading replies use five independent design systems: Editorial, Atelier, Noir, Cute, and Minimal. Markdown is parsed into a restricted document model with headings, paragraphs, task lists, tables, quotes, and code. Codex-provided HTML and scripts are never executed. Reply with `文字版` within 30 minutes to retrieve the full copyable answer.

Native outbound voice bubbles are not reliable on the channel, so voice delivery is explicitly a reading image plus an MP3 file, with ordered Piper and MiMo provider fallback.

## Repository layout

```text
cmd/codex-link-clawbot/        the only binary entry point
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

The core checks are `go test ./...`, the race detector, `go vet ./...`, and `go build ./cmd/codex-link-clawbot`. CI also runs fuzz smoke tests and builds Linux amd64 and arm64 binaries.

## Security

Codex runs with `approvalPolicy: never` and `dangerFullAccess` inside allowlisted projects. A bound owner can drive local coding tools through WeChat, so bind only a trusted personal account and use a dedicated OS user with a minimal project list.

State files use strict schemas and private permissions. Runtime management is Unix-socket-only and there is no general-purpose proactive send API; system notices are deferred until the owner's next valid interaction. Logs exclude message bodies, answer previews, raw owner IDs, private paths, tokens, command output, and diffs.

## License

MIT. The original WeClaw copyright, license notice, and Git history are retained. See the [upstream relationship and modification boundary](docs/architecture/upstream.md).
