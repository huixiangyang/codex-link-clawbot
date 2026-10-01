CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL) STRICT;
CREATE TABLE IF NOT EXISTS workspaces (id TEXT PRIMARY KEY, name TEXT NOT NULL, root TEXT NOT NULL, position INTEGER NOT NULL) STRICT;
CREATE TABLE IF NOT EXISTS codex_env (key TEXT PRIMARY KEY, value TEXT NOT NULL) STRICT;
CREATE TABLE IF NOT EXISTS voice_providers (
 id TEXT PRIMARY KEY, position INTEGER NOT NULL, type TEXT NOT NULL, timeout_seconds INTEGER NOT NULL,
 command TEXT NOT NULL, model TEXT NOT NULL, model_config TEXT NOT NULL, length_scale REAL NOT NULL,
 base_url TEXT NOT NULL, api_key TEXT NOT NULL, voice TEXT NOT NULL, style_prompt TEXT NOT NULL
) STRICT;
CREATE TABLE IF NOT EXISTS credentials (version INTEGER NOT NULL, bot_token TEXT NOT NULL, ilink_bot_id TEXT PRIMARY KEY, baseurl TEXT NOT NULL, ilink_user_id TEXT NOT NULL) STRICT;
CREATE TABLE IF NOT EXISTS sync_cursors (bot_id TEXT PRIMARY KEY, get_updates_buf TEXT NOT NULL, pending_cursor TEXT NOT NULL) STRICT;
CREATE TABLE IF NOT EXISTS sync_receipts (bot_id TEXT NOT NULL REFERENCES sync_cursors(bot_id) ON DELETE CASCADE, source TEXT NOT NULL, PRIMARY KEY(bot_id,source)) STRICT;
CREATE TABLE IF NOT EXISTS preferences (owner_id TEXT PRIMARY KEY, style TEXT NOT NULL, response_mode TEXT NOT NULL) STRICT;
CREATE TABLE IF NOT EXISTS remote_locks (owner_id TEXT PRIMARY KEY) STRICT;
CREATE TABLE IF NOT EXISTS target_owners (owner_id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL) STRICT;
CREATE TABLE IF NOT EXISTS target_intents (owner_id TEXT NOT NULL REFERENCES target_owners(owner_id) ON DELETE CASCADE, id TEXT NOT NULL, workspace_id TEXT NOT NULL, thread_id TEXT NOT NULL, PRIMARY KEY(owner_id,id)) STRICT;
CREATE TABLE IF NOT EXISTS target_selections (owner_id TEXT NOT NULL, workspace_id TEXT NOT NULL, target_id TEXT NOT NULL, PRIMARY KEY(owner_id,workspace_id), FOREIGN KEY(owner_id,target_id) REFERENCES target_intents(owner_id,id) ON DELETE CASCADE) STRICT;
CREATE TABLE IF NOT EXISTS notices (owner_id TEXT NOT NULL, id TEXT PRIMARY KEY, kind TEXT NOT NULL, dedup_key TEXT NOT NULL, reference_id TEXT NOT NULL, title TEXT NOT NULL, body TEXT NOT NULL, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, UNIQUE(owner_id,dedup_key)) STRICT;
CREATE TABLE IF NOT EXISTS menu_receipts (owner_id TEXT NOT NULL, source TEXT NOT NULL, at INTEGER NOT NULL, PRIMARY KEY(owner_id,source)) STRICT;
CREATE TABLE IF NOT EXISTS drafts (owner_id TEXT PRIMARY KEY, target_id TEXT NOT NULL, workspace_id TEXT NOT NULL, thread_id TEXT NOT NULL, text TEXT NOT NULL, expires_at INTEGER NOT NULL, submit_source TEXT NOT NULL) STRICT;
CREATE TABLE IF NOT EXISTS draft_receipts (source TEXT PRIMARY KEY, at INTEGER NOT NULL) STRICT;
CREATE TABLE IF NOT EXISTS requests (
 input_pending INTEGER NOT NULL, archive_failed INTEGER NOT NULL, turn_id TEXT NOT NULL, target_id TEXT NOT NULL,
 execution_completed_at INTEGER NOT NULL, result_expires_at INTEGER NOT NULL, result_bytes INTEGER NOT NULL,
 id TEXT PRIMARY KEY, source_message_key TEXT NOT NULL UNIQUE, owner_id TEXT NOT NULL, project_id TEXT NOT NULL, thread_id TEXT NOT NULL,
 summary TEXT NOT NULL, state TEXT NOT NULL CHECK(state IN ('running','delivering','succeeded','failed','interrupted','cancelled')),
 stage TEXT NOT NULL, reason TEXT NOT NULL, response_mode TEXT NOT NULL, visual_style TEXT NOT NULL,
 "order" INTEGER NOT NULL UNIQUE, created_at INTEGER NOT NULL, started_at INTEGER NOT NULL, finished_at INTEGER NOT NULL,
 payload_expires_at INTEGER NOT NULL, retry_of TEXT NOT NULL, image_count INTEGER NOT NULL, file_count INTEGER NOT NULL,
 payload_bytes INTEGER NOT NULL, input_tokens INTEGER NOT NULL, output_tokens INTEGER NOT NULL, total_tokens INTEGER NOT NULL
) STRICT;
CREATE INDEX IF NOT EXISTS requests_owner_time ON requests(owner_id,created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS requests_active_thread ON requests(thread_id) WHERE thread_id<>'' AND state IN ('running','delivering');
CREATE UNIQUE INDEX IF NOT EXISTS requests_active_intent ON requests(owner_id,target_id) WHERE thread_id='' AND state IN ('running','delivering');
CREATE TABLE IF NOT EXISTS rejected_sources (source TEXT PRIMARY KEY, owner_id TEXT NOT NULL, target_id TEXT NOT NULL, thread_id TEXT NOT NULL, task_id TEXT NOT NULL, turn_id TEXT NOT NULL, at INTEGER NOT NULL) STRICT;
CREATE TABLE IF NOT EXISTS cleared_sources (source TEXT PRIMARY KEY, owner_id TEXT NOT NULL, task_id TEXT NOT NULL, state TEXT NOT NULL, execution_completed_at INTEGER NOT NULL, at INTEGER NOT NULL) STRICT;
CREATE TABLE IF NOT EXISTS request_inputs (request_id TEXT PRIMARY KEY REFERENCES requests(id) ON DELETE CASCADE, source_message_key TEXT NOT NULL, text TEXT NOT NULL, context_token TEXT NOT NULL) STRICT;
CREATE TABLE IF NOT EXISTS attachment_refs (scope TEXT NOT NULL CHECK(scope IN ('request','draft')), parent TEXT NOT NULL, kind TEXT NOT NULL CHECK(kind IN ('image','file')), position INTEGER NOT NULL, url TEXT NOT NULL, name TEXT NOT NULL, length TEXT NOT NULL, size INTEGER NOT NULL, has_media INTEGER NOT NULL, query TEXT NOT NULL, key TEXT NOT NULL, encryption INTEGER NOT NULL, PRIMARY KEY(scope,parent,kind,position)) STRICT;
CREATE TRIGGER IF NOT EXISTS delete_request_refs AFTER DELETE ON request_inputs BEGIN DELETE FROM attachment_refs WHERE scope='request' AND parent=OLD.request_id; END;
CREATE TRIGGER IF NOT EXISTS delete_draft_refs AFTER DELETE ON drafts BEGIN DELETE FROM attachment_refs WHERE scope='draft' AND parent=OLD.owner_id; END;
CREATE TABLE IF NOT EXISTS artifacts (
 request_id TEXT NOT NULL REFERENCES requests(id) ON DELETE CASCADE, role TEXT NOT NULL CHECK(role IN ('input_image','input_file','output')),
 position INTEGER NOT NULL, name TEXT NOT NULL, path TEXT NOT NULL, content_type TEXT NOT NULL, size INTEGER NOT NULL CHECK(size>0), sha256 TEXT NOT NULL,
 PRIMARY KEY(request_id,role,position), UNIQUE(request_id,path)
) STRICT;
CREATE INDEX IF NOT EXISTS artifacts_role ON artifacts(role,request_id);
CREATE TABLE IF NOT EXISTS completions (request_id TEXT PRIMARY KEY REFERENCES requests(id) ON DELETE CASCADE, version INTEGER NOT NULL, reply TEXT NOT NULL, at INTEGER NOT NULL) STRICT;
CREATE TABLE IF NOT EXISTS results (request_id TEXT PRIMARY KEY REFERENCES requests(id) ON DELETE CASCADE, version INTEGER NOT NULL, reply TEXT NOT NULL, response_mode TEXT NOT NULL, visual_style TEXT NOT NULL, frozen_at INTEGER NOT NULL) STRICT;
CREATE TABLE IF NOT EXISTS result_urls (request_id TEXT NOT NULL REFERENCES results(request_id) ON DELETE CASCADE, position INTEGER NOT NULL, url TEXT NOT NULL, PRIMARY KEY(request_id,position)) STRICT;
CREATE TABLE IF NOT EXISTS delivery_receipts (request_id TEXT NOT NULL REFERENCES results(request_id) ON DELETE CASCADE, position INTEGER NOT NULL, operation_id TEXT NOT NULL, outcome TEXT NOT NULL, attempted_at INTEGER NOT NULL, media_sent INTEGER NOT NULL, text_sent INTEGER NOT NULL, failure_code TEXT NOT NULL, PRIMARY KEY(request_id,position)) STRICT;
CREATE TABLE IF NOT EXISTS import_files (path TEXT PRIMARY KEY, size INTEGER NOT NULL, sha256 TEXT NOT NULL) STRICT;
CREATE TABLE IF NOT EXISTS import_sources (name TEXT PRIMARY KEY) STRICT;
CREATE TABLE IF NOT EXISTS snapshot_entries (path TEXT PRIMARY KEY, kind TEXT NOT NULL, mode INTEGER NOT NULL, size INTEGER NOT NULL, sha256 TEXT NOT NULL) STRICT;
CREATE TABLE IF NOT EXISTS deployment_receipts (version INTEGER NOT NULL, id TEXT PRIMARY KEY, status TEXT NOT NULL, phase TEXT NOT NULL, service TEXT NOT NULL, from_version TEXT NOT NULL, to_version TEXT NOT NULL, binary_sha256 TEXT NOT NULL, started_at INTEGER NOT NULL, finished_at INTEGER NOT NULL, notification_status TEXT NOT NULL, failure TEXT NOT NULL) STRICT;
