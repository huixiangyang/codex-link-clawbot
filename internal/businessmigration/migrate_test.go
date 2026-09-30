package businessmigration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/presentation"
	"github.com/huixiangyang/codex-link-clawbot/internal/request"
	"github.com/huixiangyang/codex-link-clawbot/internal/statefile"
	"github.com/huixiangyang/codex-link-clawbot/internal/target"
)

func TestBusinessMigrationPreservesRoutingResultsAndBackup(t *testing.T) {
	root := t.TempDir()
	write := func(name string, value any) {
		t.Helper()
		if err := statefile.WriteJSON(filepath.Join(root, name), value, statefile.Options{}); err != nil {
			t.Fatal(err)
		}
	}
	write("project-state.json", map[string]any{"version": 1, "owners": map[string]string{"owner": "beta"}})
	write("session-index.json", map[string]any{"version": 3, "owners": map[string]any{"owner": map[string]any{"active_threads": map[string]string{"alpha": "thread-a", "beta": "thread-b"}, "threads": map[string]any{}}}})
	store, err := request.NewStore(filepath.Join(root, "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	task, _, err := store.Start(request.StartInput{SourceMessageKey: "old-source", OwnerID: "owner", ProjectID: "alpha", ThreadID: "thread-a", Summary: "旧回答", Text: "旧输入", ContextToken: "private", ResponseMode: presentation.ResponseAdaptive, VisualStyle: presentation.DefaultStyle})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FreezeResult("owner", task.ID, request.FreezeResultInput{Reply: "已完成但未确认投递"}); err != nil {
		t.Fatal(err)
	}
	// 构造最后一个旧版本：结果尚未发送，索引没有结果元数据。
	indexPath := filepath.Join(root, "tasks/index.json")
	data, _ := os.ReadFile(indexPath)
	var index map[string]any
	if err := json.Unmarshal(data, &index); err != nil {
		t.Fatal(err)
	}
	index["version"] = 1
	delete(index, "cleared")
	owners := index["owners"].(map[string]any)
	tasks := owners["owner"].(map[string]any)["tasks"].([]any)
	legacy := tasks[0].(map[string]any)
	for _, key := range []string{"execution_completed_at", "result_expires_at", "result_bytes"} {
		delete(legacy, key)
	}
	write("tasks/index.json", index)
	resultPath := "tasks/" + task.ID + "/result.json"
	data, _ = os.ReadFile(filepath.Join(root, resultPath))
	var result map[string]any
	_ = json.Unmarshal(data, &result)
	result["version"] = 1
	write(resultPath, result)
	source := filepath.Join(root, "deliveries", "report.txt")
	if err := os.MkdirAll(filepath.Dir(source), 0700); err != nil {
		t.Fatal(err)
	}
	content := []byte("历史文件内容")
	if err := os.WriteFile(source, content, 0600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(content)
	write("library.json", map[string]any{"version": 3, "owners": map[string]any{"owner": []map[string]any{{"id": "legacy-file", "task_id": task.ID, "project_id": "alpha", "thread_id": "thread-a", "title": "report.txt", "file_path": source, "size": len(content), "sha256": hex.EncodeToString(hash[:]), "created_at": time.Now().Unix()}}}})
	if err := CheckReady(root); err == nil {
		t.Fatal("legacy state accepted at runtime")
	}
	backup, err := Run(root, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	var manifest statefile.Manifest
	if _, err := statefile.ReadJSON(filepath.Join(backup, "manifest.json"), &manifest, statefile.Options{}); err != nil {
		t.Fatal(err)
	}
	if err := statefile.VerifyManifest(backup, manifest, maxBackupBytes); err != nil {
		t.Fatal(err)
	}
	targets, err := target.Open(filepath.Join(root, "targets.json"), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if current := targets.Current("owner"); current.WorkspaceID != "beta" || current.ThreadID != "thread-b" {
		t.Fatalf("current target: %+v", current)
	}
	migrated, err := request.NewStore(filepath.Join(root, "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	found, err := migrated.LoadResult("owner", task.ID)
	if err != nil || found.Reply != "已完成但未确认投递" || found.Receipt.Outcome != request.DeliveryAmbiguous {
		t.Fatalf("result: %+v %v", found, err)
	}
	records := migrated.List("owner")
	if len(records) != 2 {
		t.Fatalf("records: %d", len(records))
	}
	for _, record := range records {
		if record.ID == task.ID {
			continue
		}
		file, _, err := migrated.OpenArtifact("owner", record.ID, 0)
		if err != nil {
			t.Fatal(err)
		}
		file.Close()
	}
	if again, err := Run(root, "alpha"); err != nil || again != "" {
		t.Fatalf("idempotency: %s %v", again, err)
	}
	// 模拟发布中断：下次运行必须先还原备份，再重新转换。
	if err := statefile.WriteJSON(filepath.Join(root, marker), journal{Backup: filepath.Base(backup), Manifest: manifest}, statefile.Options{}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, "tasks")); err != nil {
		t.Fatal(err)
	}
	if err := CheckReady(root); err == nil {
		t.Fatal("partial migration accepted")
	}
	if _, err := Run(root, "alpha"); err != nil {
		t.Fatal(err)
	}
	if err := CheckReady(root); err != nil {
		t.Fatal(err)
	}
}

func TestV2WaitingRequestsAreCancelledWithoutChangingCurrentTarget(t *testing.T) {
	root := t.TempDir()
	targets, err := target.Open(filepath.Join(root, "targets.json"), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	intent, err := targets.Capture("owner")
	if err != nil {
		t.Fatal(err)
	}
	store, err := request.NewStore(filepath.Join(root, "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	task, _, err := store.Start(request.StartInput{TargetID: intent.ID, OwnerID: "owner", ProjectID: "alpha", SourceMessageKey: "old-waiting", Summary: "原等待请求", Text: "不应自动执行", ResponseMode: presentation.ResponseAdaptive, VisualStyle: presentation.DefaultStyle})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "tasks/index.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var index map[string]any
	if err := json.Unmarshal(data, &index); err != nil {
		t.Fatal(err)
	}
	index["version"] = 2
	delete(index, "cleared")
	delete(index, "rejected")
	owner := index["owners"].(map[string]any)["owner"].(map[string]any)
	owner["paused"] = true
	old := owner["tasks"].([]any)[0].(map[string]any)
	old["state"], old["started_at"], old["awaiting_acknowledgement"] = "queued", 0, true
	if err := statefile.WriteJSON(path, index, statefile.Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := request.NewStore(filepath.Join(root, "tasks")); err == nil {
		t.Fatal("runtime accepted old queue schema")
	}
	backup, err := Run(root, "alpha")
	if err != nil || backup == "" {
		t.Fatalf("migration: %s %v", backup, err)
	}
	current, err := target.Open(filepath.Join(root, "targets.json"), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if current.Current("owner") != intent {
		t.Fatal("migration changed selected target")
	}
	migrated, err := request.NewStore(filepath.Join(root, "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	found, ok := migrated.Find("owner", task.ID)
	if !ok || found.State != request.StateCancelled || found.Reason != "removed_pending" || found.StartedAt != 0 {
		t.Fatalf("old waiting request: %+v", found)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var converted map[string]any
	if err := json.Unmarshal(data, &converted); err != nil {
		t.Fatal(err)
	}
	if converted["version"] != float64(4) {
		t.Fatal("schema not upgraded")
	}
	if _, exists := converted["owners"].(map[string]any)["owner"].(map[string]any)["paused"]; exists {
		t.Fatal("queue state retained")
	}
}

func TestV3MigrationPreservesRejectionAndDeliveryReceipts(t *testing.T) {
	root := t.TempDir()
	targets, err := target.Open(filepath.Join(root, "targets.json"), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	intent, err := targets.Capture("owner")
	if err != nil {
		t.Fatal(err)
	}
	store, err := request.NewStore(filepath.Join(root, "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	task, _, err := store.Start(request.StartInput{TargetID: intent.ID, OwnerID: "owner", ProjectID: "alpha", SourceMessageKey: "old-source", Summary: "已完成", Text: "原文", ResponseMode: presentation.ResponseAdaptive, VisualStyle: presentation.DefaultStyle})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FreezeResult("owner", task.ID, request.FreezeResultInput{Reply: "成果"}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordDelivery("owner", task.ID, request.DeliveryReceipt{Outcome: request.DeliveryExplicitFailure, FailureCode: request.ReasonDeliveryFailed, AttemptedAt: time.Now().Unix()}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Finish("owner", task.ID, request.StateSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.Reject(request.Rejection{Source: "rejected", OwnerID: "owner", TargetID: intent.ID}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "tasks/index.json")
	data, _ := os.ReadFile(path)
	var index map[string]any
	if err := json.Unmarshal(data, &index); err != nil {
		t.Fatal(err)
	}
	index["version"] = 3
	delete(index, "cleared")
	if err := statefile.WriteJSON(path, index, statefile.Options{}); err != nil {
		t.Fatal(err)
	}
	payloadPath := filepath.Join(root, "tasks", task.ID, "request.json")
	data, _ = os.ReadFile(payloadPath)
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	payload["version"] = 1
	if err := statefile.WriteJSON(payloadPath, payload, statefile.Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(root, "alpha"); err != nil {
		t.Fatal(err)
	}
	store, err = request.NewStore(filepath.Join(root, "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.FindRejection("rejected"); !ok {
		t.Fatal("rejected message lost deduplication")
	}
	result, err := store.LoadResult("owner", task.ID)
	if err != nil || len(result.Attempts) != 1 || result.Receipt.Outcome != request.DeliveryExplicitFailure {
		t.Fatalf("lost delivery receipt: %+v %v", result, err)
	}
	input, err := store.LoadRequest("owner", task.ID)
	if err != nil || input.Text != "原文" {
		t.Fatalf("input upgrade failed: %+v %v", input, err)
	}
}
