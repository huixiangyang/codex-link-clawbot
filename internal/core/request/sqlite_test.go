package request

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/platform/storage"
)

func TestPreparedInputRollsBackAndCanRetry(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	input := testStartInput("account:prepare", "owner", "project")
	input.SourceData = []byte(`{"images":[],"files":[{"url":"https://example.com/file","file_name":"note.txt","len":"4"}]}`)
	task := mustStart(t, store, input)
	exec := func(query string) {
		t.Helper()
		if err := storage.Update(root, func(tx *sql.Tx) error { _, err := tx.Exec(query); return err }); err != nil {
			t.Fatal(err)
		}
	}
	exec(`CREATE TRIGGER reject_input BEFORE INSERT ON artifacts BEGIN SELECT RAISE(ABORT,'simulated disk failure'); END`)
	files := []InputAttachment{{Name: "note.txt", ContentType: "text/plain", Data: []byte("note")}}
	if err := store.AttachInput("owner", task.ID, "prepared", nil, files); err == nil {
		t.Fatal("expected transaction failure")
	}
	current, _ := store.Find("owner", task.ID)
	loaded, err := store.LoadRequest("owner", task.ID)
	if err != nil || !current.InputPending || len(loaded.SourceData) == 0 || len(loaded.Files) != 0 {
		t.Fatalf("input was partly committed: pending=%v err=%v", current.InputPending, err)
	}
	entries, err := os.ReadDir(filepath.Join(store.Root(), task.ID, "input"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed preparation left files: %d err=%v", len(entries), err)
	}
	exec("DROP TRIGGER reject_input")
	if err := store.AttachInput("owner", task.ID, "prepared", nil, files); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err = reopened.LoadRequest("owner", task.ID)
	if err != nil || len(loaded.Files) != 1 || len(loaded.SourceData) != 0 || loaded.Text != "prepared" {
		t.Fatalf("prepared input not durable: %v", err)
	}
}

func TestDeliveryCommitIsAtomicAndDoesNotRewriteUnrelatedData(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	other := mustStart(t, store, testStartInput("source:other", "another-owner", "project"))
	if _, err := store.Finish(other.OwnerID, other.ID, StateFailed, ReasonCodexFailed); err != nil {
		t.Fatal(err)
	}
	if err := store.Reject(Rejection{Source: "source:rejected", OwnerID: "owner"}); err != nil {
		t.Fatal(err)
	}
	cleared := mustStart(t, store, testStartInput("source:cleared", "owner", "project"))
	if _, err := store.Finish(cleared.OwnerID, cleared.ID, StateCancelled, ReasonUserCancelled); err != nil {
		t.Fatal(err)
	}
	if err := store.Release(cleared.OwnerID, cleared.ID); err != nil {
		t.Fatal(err)
	}
	task := mustStart(t, store, testStartInput("source:delivery", "owner", "project"))
	outbox, err := store.PrepareOutbox(task.OwnerID, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(outbox, "report.txt")
	if err := os.WriteFile(file, []byte("report"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := store.FreezeResult(task.OwnerID, task.ID, FreezeResultInput{Reply: "done", ArtifactPaths: []string{file}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginDelivery(task.OwnerID, task.ID); err != nil {
		t.Fatal(err)
	}
	exec := func(query string) {
		t.Helper()
		if err := storage.Update(root, func(tx *sql.Tx) error { _, err := tx.Exec(query); return err }); err != nil {
			t.Fatal(err)
		}
	}
	// 触发器既模拟磁盘写入失败，也断言热路径没有重写正文、附件或其他请求。
	for _, table := range []string{"results", "artifacts", "rejected_sources", "cleared_sources"} {
		for _, action := range []string{"INSERT", "UPDATE", "DELETE"} {
			exec(fmt.Sprintf("CREATE TRIGGER guard_%s_%s BEFORE %s ON %s BEGIN SELECT RAISE(ABORT,'unrelated write'); END", table, action, action, table))
		}
	}
	exec(fmt.Sprintf("CREATE TRIGGER guard_other BEFORE UPDATE ON requests WHEN OLD.id<>'%s' BEGIN SELECT RAISE(ABORT,'unrelated task'); END", task.ID))
	exec("CREATE TRIGGER reject_receipt BEFORE INSERT ON delivery_receipts BEGIN SELECT RAISE(ABORT,'simulated disk failure'); END")
	receipt := DeliveryReceipt{Outcome: DeliverySucceeded, AttemptedAt: max(time.Now().Unix(), result.FrozenAt), TextSent: true}
	if err := store.CompleteDelivery(task.OwnerID, task.ID, receipt); err == nil {
		t.Fatal("expected transaction failure")
	}
	current, _ := store.Find(task.OwnerID, task.ID)
	if current.State != StateDelivering {
		t.Fatal("failed transaction advanced in-memory state")
	}
	if err := storage.View(root, func(tx *sql.Tx) error {
		var state string
		if err := tx.QueryRow("SELECT state FROM requests WHERE id=?", task.ID).Scan(&state); err != nil {
			return err
		}
		if state != string(StateDelivering) {
			t.Fatalf("failed transaction advanced persisted state: %s", state)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadResult(task.OwnerID, task.ID)
	if err != nil || loaded.Receipt.Outcome != DeliveryPending {
		t.Fatalf("receipt partly committed: %+v, %v", loaded.Receipt, err)
	}
	exec("DROP TRIGGER reject_receipt")
	if err := store.UpdateStage(task.OwnerID, task.ID, "sending"); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteDelivery(task.OwnerID, task.ID, receipt); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	current, _ = reopened.Find(task.OwnerID, task.ID)
	loaded, err = reopened.LoadResult(task.OwnerID, task.ID)
	if err != nil || current.State != StateSucceeded || loaded.Receipt != receipt || len(loaded.Attempts) != 1 {
		t.Fatalf("delivery commit not durable: state=%s receipt=%+v err=%v", current.State, loaded.Receipt, err)
	}
}
