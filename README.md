# codex-link-clawbot

A personal, self-hosted bridge from WeChat to local Codex workspaces and conversations.

Send text to continue a conversation, stage attachments before submitting them, and use compact phone menus to manage sessions and retrieve results. The web console provides search, request details, authenticated downloads, and recovery controls.

[中文](README_CN.md) · [Documentation](docs/README.md)

## What It Does

- Runs one selected WeChat binding against explicitly configured workspaces.
- Keeps the target fixed for each accepted request. Busy sessions reject additional work; there is no waiting queue.
- Separates execution, result storage, and WeChat delivery. Redelivery does not rerun Codex.
- Stores configuration, credentials, and business metadata in SQLite; keeps attachments as private files and diagnostics as rotating text logs.

This is not a team platform, scheduler, web prompt interface, or general-purpose remote shell.

## Start From Source

Requires Go 1.25+ and an installed, authenticated `codex`. In this checkout:

```bash
go build -o codex-link-clawbot ./cmd/codex-link-clawbot
./codex-link-clawbot config set visual.enabled false
./codex-link-clawbot login
./codex-link-clawbot start
```

This minimal setup uses text menus and the default workspace at `~/.codex-link-clawbot/workspace`. In another terminal, run `./codex-link-clawbot console` to retrieve the URL and private management token. Configure real project directories before sending project work. See [setup](docs/guides/getting-started.md) and [configuration](docs/guides/configuration.md).

Existing JSON-based installations must complete the [offline migration](docs/operations/migration.md) before normal commands can run. The runtime does not read legacy state.

## Using WeChat

| Input | Action |
| --- | --- |
| Ordinary text, including bare numbers | Submit work, or append instructions to an attachment draft |
| `#` or `/` | Open the menu; choose with `#1` through `#6` |
| `草稿` / `提交` / `丢弃草稿` | Inspect, submit, or discard attachments |
| `停止` / `取消` | Request interruption of the observed current execution |
| `全文 ID` / `文件 ID` | Read saved text or retrieve files |
| `继续会话 ID` | Select the result's original conversation |

Attachments never execute automatically. New users receive full-text replies; reading images and MP3 are optional. The [workbench guide](docs/guides/workbench.md) defines menus, retention, and recovery behavior.

## Security And Operations

Codex execution currently uses `approvalPolicy=never` and full host access. The workspace allowlist controls conversation selection, not OS-level file access. Use a dedicated OS account and a trusted binding. The web API requires a management token and listens only on loopback; remote access needs an HTTPS reverse proxy. Read the [security model](docs/architecture/management-security.md) before exposing an instance.

Releases provide one Linux binary, `codex-link-clawbot`, plus `checksums.txt`, built natively on the fixed Ubuntu runner. There is no CPU release matrix or architecture-based asset selection. Linux service installation, transactional updates, and rollback boundaries are covered in [deployment](docs/operations/deployment.md).

## Development

```bash
make check
```

This checks documentation links, formatting, vet, race tests, and a native build. See [development](docs/guides/development.md), [architecture](docs/architecture/overview.md), and [acceptance](docs/operations/acceptance.md). Documentation describes the current source tree, not the version running on any particular server.

## Attribution

Derived from [WeClaw](https://github.com/fastclaw-ai/weclaw), retaining its [MIT license](LICENSE) and copyright notice. This independent project is not affiliated with WeChat, Tencent, OpenAI, or the upstream maintainers. See [provenance](docs/architecture/upstream.md).
