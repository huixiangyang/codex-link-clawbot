package migration

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/adapters/wechat/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/app/config"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/preference"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/presentation"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/request"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/target"
	"github.com/huixiangyang/codex-link-clawbot/internal/platform/statefile"
	"github.com/huixiangyang/codex-link-clawbot/internal/platform/storage"
)

const taskID = "task-01234567890123456789012345678901"

func legacyFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	now := time.Now().Unix()
	style := presentation.DefaultStyle
	write := func(path string, value any) {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := statefile.Write(filepath.Join(root, path), data, statefile.Options{MaxBytes: 16 << 20}); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.DefaultConfig()
	cfg.Clawbot.ProjectEntries = []config.ProjectConfig{{ID: "project", Name: "项目", Root: t.TempDir()}}
	cfg.Clawbot.Security.RemoteLockCode = "fixture-lock"
	write("config.json", cfg)
	write("accounts/bot.json", ilink.Credentials{Version: 1, BotToken: "fixture-secret", ILinkBotID: "bot", BaseURL: "https://example.com", ILinkUserID: "owner"})
	write("accounts/bot.sync.json", map[string]any{"version": 1, "get_updates_buf": "old", "pending_cursor": "next", "consumed": []string{"message:1"}})
	write("binding.json", map[string]any{"version": 1, "bot_id": "bot"})
	write("preferences.json", map[string]any{"version": 1, "owners": map[string]any{"owner": preference.OwnerPreferences{Style: style, ResponseMode: presentation.ResponseText}}})
	id := "01234567-89ab-4cde-8123-456789abcdef"
	write("targets.json", map[string]any{"version": 1, "owners": map[string]any{"owner": map[string]any{"workspace_id": "project", "current": map[string]string{"project": id}, "intents": map[string]target.Intent{id: {ID: id, WorkspaceID: "project", ThreadID: "thread"}}}}})
	write("remote-lock.json", map[string]any{"version": 1, "owners": map[string]bool{"owner": true}})
	write("pending-notices.json", map[string]any{"version": 1, "owners": map[string]any{"owner": []any{
		map[string]any{"id": "0123456789abcdef", "kind": "deployment", "dedup_key": "deployment:fixture", "title": "已完成", "body": "部署完成", "created_at": now, "expires_at": now + 3600},
		map[string]any{"id": "1123456789abcdef", "kind": "task_recovery", "dedup_key": "recovery:fixture", "title": "需要恢复", "body": "结果未送达", "created_at": now, "expires_at": now + 3600},
	}}})
	write("menu-receipts.json", map[string]any{"version": 1, "owners": map[string]any{"owner": map[string]int64{"message:menu": now}}})
	write("conversation-drafts.json", map[string]any{"version": 1, "owners": map[string]any{"owner": map[string]any{"target": target.Intent{ID: id, WorkspaceID: "project", ThreadID: "thread"}, "text": "待提交", "expires_at": now + 1800, "attachments": map[string]any{"images": []ilink.ImageItem{{URL: "https://example.com/image.png"}}, "files": []any{}}}}, "receipts": map[string]int64{"message:draft": now}})
	content := []byte("artifact-content")
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])
	for _, path := range []string{"inbox/source.txt", "outbox/report.txt"} {
		if err := statefile.Write(filepath.Join(root, "tasks", taskID, path), content, statefile.Options{}); err != nil {
			t.Fatal(err)
		}
	}
	input := request.Request{Version: 2, SourceMessageKey: "source:1", Text: "question", ContextToken: "fixture-context", Images: []request.Attachment{}, Files: []request.Attachment{{Name: "source.txt", Path: "inbox/source.txt", ContentType: "text/plain", Size: int64(len(content)), SHA256: hash}}}
	write(filepath.Join("tasks", taskID, "request.json"), input)
	result := request.Result{Version: 2, Reply: "answer", ResponseMode: presentation.ResponseText, VisualStyle: style, FrozenAt: now, Artifacts: []request.ResultArtifact{{Name: "report.txt", Path: "outbox/report.txt", Size: int64(len(content)), SHA256: hash}}, ImageURLs: []string{}, Receipt: request.DeliveryReceipt{Outcome: request.DeliverySucceeded, AttemptedAt: now, TextSent: true}}
	write(filepath.Join("tasks", taskID, "result.json"), result)
	task := request.Task{ID: taskID, SourceMessageKey: input.SourceMessageKey, OwnerID: "owner", ProjectID: "project", ThreadID: "thread", TargetID: id, Summary: "fixture", State: request.StateSucceeded, Stage: "已完成", ResponseMode: presentation.ResponseText, VisualStyle: style, Order: 1, CreatedAt: now - 10, StartedAt: now - 10, FinishedAt: now, ExecutionCompletedAt: now, PayloadExpiresAt: now + 86400, ResultExpiresAt: now + 604800, PayloadBytes: int64(len(input.Text) + len(input.ContextToken) + len(content)), ResultBytes: int64(len(result.Reply) + len(content)), FileCount: 1}
	write("tasks/index.json", map[string]any{"version": 4, "next_order": 2, "owners": map[string]request.OwnerRecords{"owner": {Tasks: []request.Task{task}}}, "rejected": map[string]any{}, "cleared": map[string]any{}})
	if err := statefile.Write(filepath.Join(root, "management-token"), []byte(strings.Repeat("a", 64)), statefile.Options{}); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestSQLiteImportPreservesAllDomainsAndResumesFinalization(t *testing.T) {
	root := legacyFixture(t)
	backup, err := Run(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.CheckReady(root); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadRoot(root)
	if err != nil || cfg.Clawbot.Security.RemoteLockCode != "fixture-lock" {
		t.Fatal("configuration lost", err)
	}
	accounts, err := ilink.LoadCredentialsRoot(root)
	if err != nil || len(accounts) != 1 || accounts[0].BotToken != "fixture-secret" {
		t.Fatal("credentials lost", err)
	}
	targets, err := target.Open(root, "project")
	if err != nil || targets.Current("owner").ThreadID != "thread" {
		t.Fatal("target lost", err)
	}
	store, err := request.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if input, err := store.LoadRequest("owner", taskID); err != nil || input.ContextToken != "fixture-context" || len(input.Files) != 1 {
		t.Fatal("input lost", err)
	}
	if result, err := store.LoadResult("owner", taskID); err != nil || result.Reply != "answer" || len(result.Artifacts) != 1 || result.Receipt.Outcome != request.DeliverySucceeded {
		t.Fatal("result lost", err)
	}
	if items, err := request.Catalogue(root, "owner", 20); err != nil || len(items) != 1 || items[0].Name != "report.txt" {
		t.Fatal("catalogue lost", err)
	}
	if count, err := request.VerifyArtifacts(root, "owner"); err != nil || count != 1 {
		t.Fatal("artifact corrupt", err)
	}
	if err := storage.View(root, func(tx *sql.Tx) error {
		for _, table := range []string{"sync_receipts", "remote_locks", "notices", "menu_receipts", "drafts", "draft_receipts", "attachment_refs"} {
			var n int
			if err := tx.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
				return err
			}
			if n != 1 {
				t.Fatalf("%s: %d", table, n)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if again, err := Run(root); err != nil || again != backup {
		t.Fatal("import is not idempotent", err)
	}
	if err := filepath.WalkDir(filepath.Join(root, "data"), func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".json") {
			t.Fatal("runtime JSON remained", path)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// 模拟 data 已发布、旧文件仅清理了一部分时进程退出。
	if err := storage.SetSetting(root, "import.complete", "false"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".sqlite-import"), 0700); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(backup, "original", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := storage.CheckReady(root); err == nil {
		t.Fatal("partial import accepted")
	}
	if _, err := Run(root); err != nil {
		t.Fatal(err)
	}
	if err := storage.CheckReady(root); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteImportRejectsCorruptionAndRunningService(t *testing.T) {
	root := legacyFixture(t)
	lease, err := statefile.Acquire(root, statefile.LeaseRuntime)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(root); err == nil {
		t.Fatal("migrated running state")
	}
	lease.Close()
	path := filepath.Join(root, "tasks", taskID, "outbox", "report.txt")
	if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(root); err == nil {
		t.Fatal("imported corrupt artifact")
	}
	if _, err := os.Stat(filepath.Join(root, "config.json")); err != nil {
		t.Fatal("source deleted after failed import")
	}
	if _, err := os.Stat(filepath.Join(root, "data")); !os.IsNotExist(err) {
		t.Fatal("failed import was published", err)
	}
}
