package request

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

// 回归业务留存约定：输入过期不删除结果，投递失败不推翻已完成的执行。
func TestResultSurvivesInputExpiryAndDeliveryFailure(t *testing.T) {
	clock := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	store, err := newStore(t.TempDir(), func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	task := mustStart(t, store, testStartInput("source", "owner", "project"))
	outbox, err := store.PrepareOutbox("owner", task.ID)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(outbox, "report.txt")
	if err := os.WriteFile(path, []byte("report"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FreezeResult("owner", task.ID, FreezeResultInput{Reply: "已完成的回答", ArtifactPaths: []string{path}}); err != nil {
		t.Fatal(err)
	}
	checkSummary := func(s *Store, available bool, outcome DeliveryOutcome) {
		t.Helper()
		items, err := s.ListSummaries(" owner ")
		if err != nil || len(items) != 1 || items[0].Task.ID != task.ID || items[0].ResultAvailable != available || items[0].Receipt.Outcome != outcome {
			t.Fatalf("batch summary: %+v err=%v", items, err)
		}
		if foreign, err := s.ListSummaries("another-owner"); err != nil || foreign == nil || len(foreign) != 0 {
			t.Fatalf("foreign summary: %+v err=%v", foreign, err)
		}
	}
	checkSummary(store, true, DeliveryPending)
	if _, err := store.BeginDelivery("owner", task.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordDelivery("owner", task.ID, DeliveryReceipt{Outcome: DeliveryExplicitFailure, AttemptedAt: clock.Unix(), FailureCode: ReasonDeliveryFailed}); err != nil {
		t.Fatal(err)
	}
	finished, err := store.Finish("owner", task.ID, StateFailed, ReasonDeliveryFailed)
	if err != nil || finished.ExecutionStatus() != "succeeded" || finished.State != StateSucceeded {
		t.Fatalf("finished=%+v err=%v", finished, err)
	}
	if _, err := store.Retry("owner", task.ID, "bad-retry", "token"); err == nil {
		t.Fatal("completed execution was retried")
	}
	if receipt, err := store.SummarizeResult("owner", task.ID); err != nil || receipt.Outcome != DeliveryExplicitFailure {
		t.Fatalf("summary: %+v %v", receipt, err)
	}
	checkSummary(store, true, DeliveryExplicitFailure)
	operation := uuid.NewString()
	if _, duplicate, err := store.BeginRedelivery("owner", task.ID, operation); err != nil || duplicate {
		t.Fatalf("begin: %v %v", duplicate, err)
	}
	if _, duplicate, err := store.BeginRedelivery("owner", task.ID, operation); err != nil || !duplicate {
		t.Fatalf("duplicate: %v %v", duplicate, err)
	}
	if receipt, err := store.SummarizeResult("owner", task.ID); err != nil || receipt.Outcome != DeliveryAmbiguous {
		t.Fatalf("stale summary after recovery: %+v %v", receipt, err)
	}
	checkSummary(store, true, DeliveryAmbiguous)
	clock = clock.Add(25 * time.Hour)
	if err := store.CleanupExpired(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadRequest("owner", task.ID); err == nil {
		t.Fatal("input survived expiry")
	}
	if _, err := store.InspectRequest("owner", task.ID); err == nil {
		t.Fatal("input metadata survived expiry")
	}
	if _, err := os.Stat(filepath.Join(store.Root(), task.ID, "input")); !os.IsNotExist(err) {
		t.Fatalf("input on disk: %v", err)
	}
	reopened, err := newStore(store.StateRoot(), func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	result, err := reopened.LoadResult("owner", task.ID)
	if err != nil || result.Reply != "已完成的回答" {
		t.Fatalf("retained result: %+v %v", result, err)
	}
	file, _, err := reopened.OpenArtifact("owner", task.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	if _, _, err := reopened.OpenArtifact("another-owner", task.ID, 0); err == nil {
		t.Fatal("foreign owner downloaded artifact")
	}
	clock = clock.Add(7 * 24 * time.Hour)
	checkSummary(reopened, false, "")
	if _, err := reopened.InspectResult("owner", task.ID); err == nil {
		t.Fatal("result metadata survived expiry")
	}
	if file, _, err := reopened.OpenArtifact("owner", task.ID, 0); err == nil {
		file.Close()
		t.Fatal("expired file opened before cleanup")
	}
	if err := reopened.CleanupExpired(); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.LoadResult("owner", task.ID); err == nil {
		t.Fatal("result survived expiry")
	}
}

func TestReleaseFreesCapacityAndKeepsDeduplicationAcrossRestart(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	input := testStartInput("cleared-source", "owner", "project")
	task, _, err := store.Start(input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FreezeResult("owner", task.ID, FreezeResultInput{Reply: "completed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Finish("owner", task.ID, StateSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.Release("owner", task.ID); err != nil {
		t.Fatal(err)
	}
	capacity := store.Capacity("owner")
	if capacity.Records != 0 || capacity.InputBytes != 0 || capacity.ResultBytes != 0 {
		t.Fatalf("capacity not released: %+v", capacity)
	}
	store, err = NewStore(store.StateRoot())
	if err != nil {
		t.Fatal(err)
	}
	if !store.WasCleared(input.SourceMessageKey) {
		t.Fatal("source receipt lost")
	}
	if _, _, err := store.Start(input); err != ErrCleared {
		t.Fatalf("cleared source executed again: %v", err)
	}
	input.SourceMessageKey = "new-source"
	if _, _, err := store.Start(input); err != nil {
		t.Fatalf("new work blocked: %v", err)
	}
}
