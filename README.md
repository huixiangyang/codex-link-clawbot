# WeClaw

A dedicated bridge from a personal WeChat account to local Codex.

The current product boundary, target architecture, and staged roadmap are documented in [`docs/product-plan.md`](docs/product-plan.md).

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

Production lifecycle is owned exclusively by a systemd user service. `start` always runs in the foreground; there is no daemon or PID-file path. A running service is managed exclusively through the current user's private `~/.weclaw/control.sock`:

```bash
weclaw status
weclaw stop
weclaw restart
```

`update`, `upgrade`, and host-specific cutover scripts are removed. Immutable releases and local builds use the same rollback-capable transaction:

```bash
weclaw deploy v2.5.0
weclaw deploy --binary /absolute/path/to/weclaw --expect-version v2.5.0-local.1
```

Deployment verifies candidate version, platform, and SHA-256; drains the old runtime; snapshots state after shutdown; runs an offline migration; atomically installs; and starts the candidate in drain mode. The queue is resumed only after the expected version, Codex, WeChat monitors, and sync cursors are healthy. Failure restores the binary, systemd unit, configuration, and task state together. Success writes a receipt under `~/.weclaw/deployments/` and sends a plain-text WeChat notice.

The first v2.5-to-v2.6 cutover is intentionally a maintenance-window migration because the management transport changes from loopback TCP to an owner-only Unix socket and no compatibility client remains. Stop v2.5 with the installed v2.5 binary, take a complete binary/unit/state backup, then migrate and start v2.6. After that one cutover, transactional `weclaw deploy` is used again. See [the breaking migration guide](docs/migration-v2.md).

## Configuration

`~/.weclaw/config.json`:

```json
{
  "send_api": {"enabled": false},
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
      "health_url": "http://127.0.0.1:18011/health"
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
    "enabled": true,
    "ffmpeg_command": "/usr/bin/ffmpeg",
    "providers": [
      {
        "id": "local",
        "type": "piper",
        "timeout_seconds": 30,
        "piper": {
          "command": "/opt/weclaw/piper/venv/bin/piper",
          "model": "/opt/weclaw/piper/voices/zh_CN-huayan-medium.onnx",
          "model_config": "/opt/weclaw/piper/voices/zh_CN-huayan-medium.onnx.json",
          "length_scale": 1
        }
      },
      {
        "id": "mimo",
        "type": "mimo",
        "timeout_seconds": 90,
        "mimo": {
          "base_url": "https://api.xiaomimimo.com/v1",
          "api_key": "",
          "model": "mimo-v2.5-tts",
          "voice": "茉莉",
          "style_prompt": "Read naturally and clearly at a slightly slower pace."
        }
      }
    ]
  }
}
```

`projects` is the only working-directory allowlist. Sessions and quick workflows are isolated per project, and roots must already exist. Quick workflows no longer live in configuration: strict v1 `~/.weclaw/workflows.json` is the only runtime source and is isolated by bound owner. Removed `projects[].quick_tasks` entries are accepted only by the one-shot offline migration and are rejected at runtime. WeClaw always appends `app-server --listen stdio://` to `codex.command`; the removed `codex.cwd` field is rejected. An empty `model` preserves the user's Codex default. Automations support either `daily_at` or `every_minutes`, deterministic Git/service/health checks, and `always`, `anomaly`, `change`, or `anomaly_or_change` notification policies.

Voice briefings and persistent voice replies share a strict ordered chain of one to four `piper` or `mimo` providers. Each provider has its own timeout; failures automatically fall through to the next entry, and the result identifies the provider actually used. Piper returns WAV and MiMo returns MP3; the shared delivery layer uses `voice.ffmpeg_command` to normalize either format into a compact 24 kHz mono MP3, then sends it through WeChat's supported file-message path. Every successful voice request first sends a styled reading-card image containing the exact spoken script, project, and provider, followed by the matching MP3; no redundant status card is appended. The dedicated single-card layout omits one-page pagination and progress decoration. Both media items are fully staged on WeChat CDN before either becomes visible, so a second upload failure cannot leave an orphan image. This restores glanceable text because file attachments do not receive WeChat's native voice transcription. Image rendering is mandatory for this paired delivery, so `voice.enabled` requires `visual.enabled`. Personal iLink bots silently discard outbound native `VOICE` items even when the API acknowledges them, so the removed SILK/native-voice path is intentionally rejected instead of claiming delivery. A current conversation context token remains mandatory. `WECLAW_MIMO_API_KEY` can inject the key into every configured MiMo provider. Removed flat MiMo fields, `voice.silk_command`, provider-level `piper.ffmpeg_command`, and legacy `voice.command` are rejected.

Run `./scripts/install-piper.sh` to create an isolated Piper 1.4.1 environment and download `zh_CN-huayan-medium` under `~/.weclaw/tts/piper`. It requires `uv` and FFmpeg, does not modify the system Python, and prints the shared FFmpeg path plus the three Piper paths required by configuration. WeClaw does not redistribute voice models; operators must review each model card and dataset license. The upstream model card currently marks the example Huayan dataset license as unknown.

Visual controls are enabled by default and include five complete template systems: `刊物` uses paper, Chinese serif typography, and restrained red; `构筑` uses flat mineral surfaces and architectural order; `黑标` uses high-contrast black and white with champagne metal accents; `可爱` uses cream paper, rounded forms, and soft color blocks; `简洁` uses generous whitespace, hairlines, and pure information hierarchy. Each has an independent control-card and reading-card layout rather than a color swap. Send `回答方式` to open the unified preference center, or send `视觉风格` directly. Response mode and style are isolated per WeChat owner, persisted together in strict v1 `~/.weclaw/preferences.json`, and restored after restart. The removed `visual-styles.json` is not read.

Every template follows the service's local timezone automatically: a bright theme is used from 07:00 through 18:59, and a low-glare dark theme from 19:00 through 06:59. Cards arrange short facts in two or three columns and compact actions in two columns. The interface exposes only the brand, actual content, required state, numbered actions, and useful instructions; theme labels, render time, decorative counters, watermarks, and click-like arrows are deliberately omitted. WeClaw discovers Playwright-managed Chromium or a system Google Chrome. `visual.browser_command` can select an executable by absolute path. Snap Chromium is rejected because its private mount cannot reliably access the protected render directory. Startup fails with an installation hint when visual controls are enabled but no supported browser exists.

Environment overrides:

- `WECLAW_SAVE_DIR`
- `WECLAW_CODEX_COMMAND`
- `WECLAW_CODEX_MODEL`
- `WECLAW_VISUAL_BROWSER`
- `WECLAW_MIMO_API_KEY`

Configuration decoding is strict. Legacy `default_agent`, `agents`, `type`, `args`, `endpoint`, and alias fields fail startup and are not migrated at runtime.

## WeChat interaction

The only public slash entry is `/`. It opens a context-aware numbered menu rendered as a mobile-first visual card; reply with a number to continue. Actionable cards are followed by a short text instruction so input remains convenient in WeChat. Session and scheduled-report lists use six items per page and accept both numbered navigation and the natural phrases `下一页` / `上一页`. Selecting a session from the browsing list opens its status and sanitized prompt summary before any switch or archive operation. The original search query and page survive detail navigation and process restarts. Menu revisions expire after ten minutes; expired numbers and navigation phrases are explicitly rejected and never become Codex prompts.

The home card always stays at four actions or fewer. Running and queued work has priority; while idle it recommends the latest successful result and current-project quick workflows, fills remaining positions from project, session, and task center, then keeps more as the final entry. Direct phrases include `切换项目 weclaw`, `快捷任务`, `新建快捷任务`, `继续处理`, `重试上次任务`, `在新会话继续`, `保存为快捷任务`, `新建会话 叫登录排障`, `搜索会话 登录`, `任务中心`, `暂停队列`, `继续队列`, `回答方式`, `开启语音模式`, `阅读模式`, `自适应模式`, `自动化`, `素材箱`, `交付记录`, `语音简报` or the shorter `发语音`, `为什么没回复`, `远程锁定`, `状态`, and `取消`. `发语音` creates one briefing; `开启语音模式` persists paired image-and-MP3 delivery for subsequent Codex replies. Audio delivery requires the current inbound `context_token`; out-of-conversation delivery is rejected instead of reporting a false success.

`快捷任务` opens the persistent quick-workflow center for the current project, including an empty-state creation entry. Definitions open a sanitized detail before run, rename, edit, or confirmed deletion. `新建快捷任务` accepts one two-field message (`名称：…` and `内容：…`); readable markers such as `「分支」` become sequential parameters without exposing internal keys. Parameter collection survives a restart for ten minutes, deduplicates each WeChat message, restores the final field if queue persistence fails, and always enqueues into the workflow's frozen project. Templates and parameter values never enter the generic menu state.

The main card always identifies its bridge version. The runtime center adds bridge uptime and whether proactive sending is enabled alongside the Codex App Server protocol, model, working directory, and process ID. Its `为什么没回复` action reads deterministic lock, runtime, Codex, WeChat polling, persistence, queue, and recent-delivery signals without invoking Codex. If no blocker is proven, it says so instead of guessing and never exposes owner IDs, message bodies, paths, or raw errors.

The task center is backed by a persistent global FIFO. Text, image, and file requests are fully stored before acknowledgement, then executed by one Coordinator using the project, thread, response mode, and visual style captured at enqueue time. Owners can pause, resume, reorder, delete, cancel, retry, or inspect tasks without a command wall. Queued tasks survive restart; interrupted execution and ambiguous delivery require explicit recovery and never repeat Codex automatically. The visible index contains only sanitized metadata, never answer bodies, context tokens, attachment names, terminal output, account IDs, or private paths.

A successful text-only task can return to its frozen session, run again in that session, run in an explicitly new session, or be named and saved as a quick workflow. The full request, token, answer, and delivery directory are still removed immediately after success. Only a strict private `reusable.json` containing the original text remains for up to 24 hours; successful image or file tasks retain no reusable payload. Saving never copies answers, attachment metadata, paths, or command output into `workflows.json`.

Successful create, switch, rename, archive, and restore results remain actionable: they link back to the current detail, list, or session center without requiring another `/`. Any ordinary text still leaves that short-lived result state and goes directly to Codex.

The automation center shows schedules, checks, notification policy, last run and delivery, and allows a deterministic manual check. Pure links saved to Linkhoard are indexed in the material library. Successful Codex artifacts are copied into a private delivery archive and can be sent again from WeChat. Image messages containing an annotation intent request a new annotated PNG artifact.

Owners can persist one of three response modes. `adaptive` sends short replies as text and switches to reading cards at `visual.long_reply_min_runes`; `reading` always prefers cards and ignores the adaptive threshold and `visual.long_replies` switch; `voice` pairs an exact short-answer card with MP3, while answers over 2,200 characters first deliver complete reading cards and then an explicitly marked spoken excerpt card plus MP3. Reply with `文字版` within 30 minutes to retrieve the complete copyable answer. Before any media becomes visible, synthesis, rendering, or staging failure falls back to complete reading cards and then full text. An ambiguous send-phase response is treated conservatively to avoid duplicate bodies.

All legacy slash commands are removed. Any slash-prefixed text other than the single `/` is rejected by the control layer and is never forwarded to Codex.

Natural controls are resolved by a deterministic Intent Registry before entering one of eight domain controllers. Normalized phrase and prefix conflicts fail during registry construction; unmatched text remains a Codex prompt. Controllers return only validated `ActionResult` values, and the Presenter is the sole boundary for WeChat delivery, queueing, retry, frozen-text recovery, archived media, and voice briefings. The former cross-message side-effect maps and cross-domain routing switches are removed. Numbered menus and pending inputs now use strict persistent revisions; mutating or externally visible actions require a stable WeChat source receipt and use conservative at-most-once replay semantics.

## Media and proactive delivery

- WeChat images are persisted before queue acknowledgement and passed to Codex as `localImage` inputs at execution time. Limits: four images, 20 MiB each, JPEG/PNG/GIF/WebP.
- PDFs, logs, patches, archives, and common source files are treated as untrusted input. The bridge never executes or extracts them.
- Files Codex writes into the turn-specific outbox are uploaded to WeChat automatically.
- Private request/result payloads are deleted immediately after success or cancellation. Failed or interrupted payloads expire after at most 24 hours for explicit recovery.
- Control cards are rendered from fixed, escaped local templates and uploaded as PNG images. Arbitrary HTML from Codex is never executed.

```bash
weclaw send-token --caller local-cli
weclaw send --caller local-cli --to "bound-owner-id" --text "Build complete"

curl -X POST http://127.0.0.1:18011/api/send \
  -H "Authorization: Bearer $WECLAW_SEND_TOKEN" \
  -H 'Idempotency-Key: release-2026-08-05' \
  -H 'Content-Type: application/json' \
  -d '{"caller_id":"local-cli","target_owner":"bound-owner-id","text":"Build complete"}'
```

The send API is disabled by default and opens no TCP listener. `weclaw send-token` generates 256 random bits offline and prints the plaintext once together with a config entry containing only its SHA-256 hash; `weclaw send` reads plaintext only from a `WECLAW_SEND_TOKEN` environment value preloaded by the caller's secret manager. To enable loopback delivery, set `send_api.enabled`, an explicit loopback `listen_addr`, and one or more hashed token entries with `send:text` and/or `send:media` scopes. Non-loopback listeners require `proxy_mode`, canonical trusted-proxy CIDRs, and a user-managed TLS reverse proxy; WeClaw verifies the direct peer and never trusts forwarded client addresses. Requests are limited to a configured caller, a bound WeChat owner, an idempotency key, 8,000 text characters, and one public HTTPS media URL. Media downloads ignore environment proxies, reject private/link-local addresses and DNS rebinding, and stop at 25 MiB. The strict 24-hour `~/.weclaw/send-api-state.json` receipt contains only hashes, caller, timestamps, and outcome.

## Security

- Turns use `approvalPolicy: never` and `dangerFullAccess`. The bound owner can drive Codex on the host; bind only a trusted personal account.
- `~/.weclaw/session-index.json` uses strict v3 project-scoped active threads. The strict v1 task tree under `~/.weclaw/tasks/` replaces `task-history.json` and separates sanitized index metadata from private requests and frozen delivery results.
- All domain JSON state uses the shared `statefile` transaction kernel: files are `0600`, directories are `0700`, symlinks and oversized input are rejected, and a failed directory sync restores the prior file. Runtime and offline migration hold mutually exclusive state leases.
- Account credentials use strict v1 JSON. Offline deployment migration adds `version: 1` once; unknown fields, mismatched filenames, and damaged credentials now fail startup instead of being silently skipped.
- `~/.weclaw/control-state.json` uses strict v1 revisions for restart-safe menus and 24-hour minimal control receipts. Display text, prompts, paths, attachment names, tokens, and context tokens are never persisted there.
- Runtime health, drain, resume, and deployment notices exist only on the owner-only `0600` Unix socket. TCP has no `/health` or `/admin/*`; proactive TCP sending is absent unless explicitly enabled.
- Proactive send tokens are stored only as SHA-256 hashes with caller-specific text/media scopes. At-most-once receipts never store message bodies, targets, URLs, idempotency keys, or plaintext tokens.
- `~/.weclaw/preferences.json` uses strict v1 JSON, atomic replacement, mode `0600`, and per-owner response-mode and style isolation.
- Global Codex thread results are intersected with the local ownership index before display.
- Raw terminal output, commands, diffs, and environment variables are never forwarded as progress messages.
- Runtime logs use stable SHA-256-derived labels instead of raw WeChat user/bot IDs and record only message lengths, never prompt or reply previews.
- Visual rendering disables page networking and scripts, enforces a restrictive CSP, and deletes each protected HTML/profile/PNG directory after delivery.

## Development

```bash
go test ./...
go test -race ./statefile ./taskqueue ./messaging ./ilink ./api ./cmd
go vet ./...
```

More details:

- [Session management](docs/session-management.md)
- [Long-running task progress](docs/wechat-progress.md)
- [Attachments, artifacts, and reports](docs/attachments-and-reports.md)
- [Visual control cards](docs/visual-controls.md)
- [v2 breaking migration](docs/migration-v2.md)
- [v2.5 persistent task queue and safe deployment specification](docs/v2.5-task-queue.md)
- [v2.6 reliable control plane and state-kernel plan](docs/v2.6-control-plane.md)
- [v2.6 unified state-kernel implementation](docs/v2.6-state-kernel.md)
- [v2.6 typed control routing implementation](docs/v2.6-control-routing.md)
- [v2.6 persistent interaction and control receipts](docs/v2.6-persistent-control.md)
- [v2.6 local management and proactive-send security](docs/v2.6-api-security.md)
- [v2.6 deterministic no-reply diagnostics](docs/v2.6-diagnostics.md)
- [v2.7 persistent quick workflows](docs/v2.7-workflows.md)
- [Acceptance checklist](docs/acceptance.md)

## License

MIT. The original copyright and Git history are retained.
