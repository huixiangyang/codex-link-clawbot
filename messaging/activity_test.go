package messaging

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestActivityStorePersistsOutcomesAndInterruptsRunningTasks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "task-history.json")
	store, err := NewActivityStore(path)
	if err != nil {
		t.Fatal(err)
	}
	current := time.Unix(1_700_000_000, 0)
	store.now = func() time.Time { return current }
	finishedID, err := store.Start("owner-1", "检查发布状态")
	if err != nil {
		t.Fatal(err)
	}
	current = current.Add(75 * time.Second)
	if err := store.Finish("owner-1", finishedID, ActivitySucceeded); err != nil {
		t.Fatal(err)
	}
	current = current.Add(time.Second)
	runningID, err := store.Start("owner-1", "仍在执行的任务")
	if err != nil {
		t.Fatal(err)
	}

	reloaded, err := NewActivityStore(path)
	if err != nil {
		t.Fatal(err)
	}
	finished, ok := reloaded.Find("owner-1", finishedID)
	if !ok || finished.Status != ActivitySucceeded || finished.FinishedAt-finished.StartedAt != 75 {
		t.Fatalf("finished activity = %#v, exists=%v", finished, ok)
	}
	interrupted, ok := reloaded.Find("owner-1", runningID)
	if !ok || interrupted.Status != ActivityInterrupted || interrupted.FinishedAt < interrupted.StartedAt {
		t.Fatalf("interrupted activity = %#v, exists=%v", interrupted, ok)
	}
	if _, ok := reloaded.Find("owner-2", finishedID); ok {
		t.Fatal("task history leaked across owners")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("task history mode = %o", info.Mode().Perm())
	}
}

func TestNewActivityStoreCreatesProtectedEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "task-history.json")
	store, err := NewActivityStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if records := store.List("owner-1"); len(records) != 0 {
		t.Fatalf("new activity records = %#v", records)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("empty task history mode = %o", info.Mode().Perm())
	}
}

func TestActivityStoreKeepsOnlyNewestRecords(t *testing.T) {
	store, err := NewActivityStore(filepath.Join(t.TempDir(), "task-history.json"))
	if err != nil {
		t.Fatal(err)
	}
	current := time.Unix(1_700_000_000, 0)
	store.now = func() time.Time { return current }
	var firstID, lastID string
	for index := 0; index < activityHistoryLimit+3; index++ {
		id, startErr := store.Start("owner-1", "任务记录")
		if startErr != nil {
			t.Fatal(startErr)
		}
		if index == 0 {
			firstID = id
		}
		lastID = id
		current = current.Add(time.Second)
	}
	records := store.List("owner-1")
	if len(records) != activityHistoryLimit || records[0].ID != lastID {
		t.Fatalf("records = %#v", records)
	}
	if _, exists := store.Find("owner-1", firstID); exists {
		t.Fatal("oldest task should have been evicted")
	}
}
