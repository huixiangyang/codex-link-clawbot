package workflow

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/huixiangyang/weclaw/statefile"
)

func TestParameterizedRunPersistsDeduplicatesAndRollsBackCompletion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workflows.json")
	store, err := NewStore(path, []string{"alpha"})
	if err != nil {
		t.Fatal(err)
	}
	definition, err := store.Create(CreateInput{
		OwnerID: "owner", ProjectID: "alpha", Name: "发布检查",
		PromptTemplate: "检查 {{slot_1}} 并输出 {{slot_2}}", Slots: []Slot{
			{Key: "slot_1", Label: "分支"}, {Key: "slot_2", Label: "格式"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	status, err := store.StartRun("owner", "alpha", definition.ID)
	if err != nil || status.Position != 1 || status.Slot.Label != "分支" {
		t.Fatalf("StartRun() = %#v, %v", status, err)
	}
	first, err := store.SubmitRunValue("owner", "account:message:1", "main")
	if err != nil || first.Completed || first.Next.Position != 2 || first.Next.Slot.Label != "格式" {
		t.Fatalf("first SubmitRunValue() = %#v, %v", first, err)
	}
	reopened, err := NewStore(path, []string{"alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if pending, exists, err := reopened.PendingRun("owner"); err != nil || !exists || pending.Position != 2 {
		t.Fatalf("PendingRun() = %#v, %v, %v", pending, exists, err)
	}
	duplicate, err := reopened.SubmitRunValue("owner", "account:message:1", "should-not-apply")
	if err != nil || !duplicate.Duplicate {
		t.Fatalf("duplicate SubmitRunValue() = %#v, %v", duplicate, err)
	}
	completed, err := reopened.SubmitRunValue("owner", "account:message:2", "简报")
	if err != nil || !completed.Completed || completed.Prompt != "检查 main 并输出 简报" || completed.Rollback() == nil {
		t.Fatalf("completed SubmitRunValue() = %#v, %v", completed, err)
	}
	if !reopened.HasRunReceipt("owner", "account:message:2") {
		t.Fatal("final workflow receipt is missing")
	}
	if err := reopened.RollbackSubmission(completed.Rollback()); err != nil {
		t.Fatal(err)
	}
	if reopened.HasRunReceipt("owner", "account:message:2") {
		t.Fatal("rollback kept final workflow receipt")
	}
	if pending, exists, err := reopened.PendingRun("owner"); err != nil || !exists || pending.Position != 2 {
		t.Fatalf("rolled back PendingRun() = %#v, %v, %v", pending, exists, err)
	}
	completed, err = reopened.SubmitRunValue("owner", "account:message:2", "简报")
	if err != nil || !completed.Completed || completed.Prompt != "检查 main 并输出 简报" {
		t.Fatalf("retried SubmitRunValue() = %#v, %v", completed, err)
	}
}

func TestRunStateIsOwnerIsolatedAndCancellable(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "workflows.json"), []string{"alpha"})
	if err != nil {
		t.Fatal(err)
	}
	definition, err := store.Create(CreateInput{
		OwnerID: "owner-a", ProjectID: "alpha", Name: "检查",
		PromptTemplate: "检查 {{slot_1}}", Slots: []Slot{{Key: "slot_1", Label: "目标"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartRun("owner-b", "alpha", definition.ID); err == nil {
		t.Fatal("another owner started a private workflow")
	}
	if _, err := store.StartRun("owner-a", "alpha", definition.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.CancelRun("owner-a"); err != nil {
		t.Fatal(err)
	}
	if _, exists, err := store.PendingRun("owner-a"); err != nil || exists {
		t.Fatalf("cancelled run still exists: %v, %v", exists, err)
	}
}

func TestRunWriteFailureKeepsPreviousParameterState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workflows.json")
	clock := time.Unix(1_800_000_000, 0)
	store, err := newStore(path, []string{"alpha"}, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	definition, err := store.Create(CreateInput{
		OwnerID: "owner", ProjectID: "alpha", Name: "检查",
		PromptTemplate: "检查 {{slot_1}}", Slots: []Slot{{Key: "slot_1", Label: "目标"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartRun("owner", "alpha", definition.ID); err != nil {
		t.Fatal(err)
	}
	store.fault = func(point statefile.FaultPoint) error {
		if point == statefile.FaultRename {
			return errors.New("injected rename failure")
		}
		return nil
	}
	if _, err := store.SubmitRunValue("owner", "account:message:1", "main"); err == nil {
		t.Fatal("injected parameter write failure was ignored")
	}
	if store.HasRunReceipt("owner", "account:message:1") {
		t.Fatal("failed parameter write kept a receipt")
	}
	if pending, exists, err := store.PendingRun("owner"); err != nil || !exists || pending.Position != 1 {
		t.Fatalf("memory changed after failed parameter write: %#v, exists=%v err=%v", pending, exists, err)
	}
	store.fault = nil
	reopened, err := newStore(path, []string{"alpha"}, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	if pending, exists, err := reopened.PendingRun("owner"); err != nil || !exists || pending.Position != 1 {
		t.Fatalf("disk changed after failed parameter write: %#v, exists=%v err=%v", pending, exists, err)
	}
}
