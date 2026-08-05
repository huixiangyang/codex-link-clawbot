package taskqueue

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huixiangyang/weclaw/preference"
	"github.com/huixiangyang/weclaw/visual"
)

func TestStoreEnqueuesPrivatePayloadPersistsAndDeduplicates(t *testing.T) {
	root := filepath.Join(t.TempDir(), "tasks")
	now := time.Date(2026, 8, 5, 16, 30, 0, 0, time.Local)
	store, err := newStore(root, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	input := testEnqueueInput("account-1:message-42", "owner-1", "project")
	input.Text = "这是只应出现在私有请求中的完整问题"
	input.ContextToken = "secret-context-token"
	input.Images = []InputAttachment{{Name: "screen.png", ContentType: "image/png", Data: []byte("private-image")}}
	input.Files = []InputAttachment{{Name: "report.pdf", ContentType: "application/pdf", Data: []byte("private-file")}}
	task, existed, err := store.Enqueue(input)
	if err != nil || existed {
		t.Fatalf("enqueue task = %#v existed=%v err=%v", task, existed, err)
	}
	if task.State != StateQueued || task.ProjectID != "project" || task.ResponseMode != preference.ResponseAdaptive || task.VisualStyle != visual.StyleEditorial {
		t.Fatalf("queued task = %#v", task)
	}
	duplicateInput := input
	duplicateInput.Text = "不得覆盖原任务"
	duplicate, existed, err := store.Enqueue(duplicateInput)
	if err != nil || !existed || duplicate.ID != task.ID || len(store.List("owner-1")) != 1 {
		t.Fatalf("deduplicated task = %#v existed=%v err=%v", duplicate, existed, err)
	}

	loaded, err := store.LoadRequest("owner-1", task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Text != input.Text || loaded.ContextToken != input.ContextToken || len(loaded.Images) != 1 || len(loaded.Files) != 1 {
		t.Fatalf("loaded request = %#v", loaded)
	}
	for _, attachment := range append(loaded.Images, loaded.Files...) {
		if !strings.HasPrefix(attachment.AbsolutePath, filepath.Join(root, task.ID, "inbox")+string(filepath.Separator)) {
			t.Fatalf("attachment escaped private root: %q", attachment.AbsolutePath)
		}
		assertMode(t, attachment.AbsolutePath, 0o600)
	}
	assertMode(t, root, 0o700)
	assertMode(t, filepath.Join(root, "index.json"), 0o600)
	assertMode(t, filepath.Join(root, task.ID), 0o700)
	assertMode(t, filepath.Join(root, task.ID, "request.json"), 0o600)

	indexData, err := os.ReadFile(filepath.Join(root, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{input.Text, input.ContextToken, "screen.png", "report.pdf"} {
		if strings.Contains(string(indexData), secret) {
			t.Fatalf("task index leaked %q", secret)
		}
	}
	reloaded, err := newStore(root, func() time.Time { return now.Add(time.Minute) })
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := reloaded.Find("owner-1", task.ID); !ok || got.SourceMessageKey != input.SourceMessageKey {
		t.Fatalf("reloaded task = %#v ok=%v", got, ok)
	}
	if _, err := reloaded.LoadRequest("owner-1", task.ID); err != nil {
		t.Fatalf("reloaded request: %v", err)
	}
}

func TestStoreSerialFIFORespectsPauseMoveAndLifecycle(t *testing.T) {
	root := filepath.Join(t.TempDir(), "tasks")
	clock := time.Date(2026, 8, 5, 17, 0, 0, 0, time.Local)
	store, err := newStore(root, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	first := mustEnqueue(t, store, testEnqueueInput("a:1", "owner-a", "alpha"))
	second := mustEnqueue(t, store, testEnqueueInput("b:1", "owner-b", "beta"))
	third := mustEnqueue(t, store, testEnqueueInput("a:2", "owner-a", "alpha"))
	if err := store.SetPaused("owner-a", true); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.ClaimNext(nil)
	if err != nil || !ok || claimed.ID != second.ID {
		t.Fatalf("first claim = %#v ok=%v err=%v", claimed, ok, err)
	}
	if _, ok, err := store.ClaimNext(nil); err != nil || ok {
		t.Fatalf("second active claim ok=%v err=%v", ok, err)
	}
	if _, err := store.BeginDelivery("owner-b", second.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Finish("owner-b", second.ID, StateSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, second.ID)); !os.IsNotExist(err) {
		t.Fatalf("successful task payload still exists: %v", err)
	}
	if err := store.SetPaused("owner-a", false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MoveToFront("owner-a", third.ID); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err = store.ClaimNext(nil)
	if err != nil || !ok || claimed.ID != third.ID {
		t.Fatalf("moved claim = %#v ok=%v err=%v", claimed, ok, err)
	}
	if err := store.AttachThread("owner-a", third.ID, "thread-fixed"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateStage("owner-a", third.ID, "正在运行测试"); err != nil {
		t.Fatal(err)
	}
	if err := store.AttachUsage("owner-a", third.ID, 10, 20, 30); err != nil {
		t.Fatal(err)
	}
	failed, err := store.Finish("owner-a", third.ID, StateFailed, ReasonCodexFailed)
	if err != nil || failed.ThreadID != "thread-fixed" || failed.TotalTokens != 30 || failed.PayloadExpiresAt <= clock.Unix() {
		t.Fatalf("failed task = %#v err=%v", failed, err)
	}
	if _, err := store.LoadRequest("owner-a", third.ID); err != nil {
		t.Fatalf("failed task request should remain: %v", err)
	}
	claimed, ok, err = store.ClaimNext(nil)
	if err != nil || !ok || claimed.ID != first.ID {
		t.Fatalf("remaining claim = %#v ok=%v err=%v", claimed, ok, err)
	}
	if _, err := store.Finish("owner-a", first.ID, StateCancelled, ReasonUserCancelled); err != nil {
		t.Fatal(err)
	}
	if status := store.Status("owner-a"); status.Queued != 0 || status.Running != 0 || status.Failed != 1 || status.Cancelled != 1 {
		t.Fatalf("owner status = %#v", status)
	}
}

func TestStoreRecoversRunningAndDeliveryWithoutAutomaticRetry(t *testing.T) {
	base := time.Date(2026, 8, 5, 18, 0, 0, 0, time.Local)
	for _, test := range []struct {
		name       string
		delivering bool
		reason     string
	}{
		{name: "running", reason: ReasonRestartRunning},
		{name: "delivering", delivering: true, reason: ReasonRestartDelivery},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "tasks")
			store, err := newStore(root, func() time.Time { return base })
			if err != nil {
				t.Fatal(err)
			}
			task := mustEnqueue(t, store, testEnqueueInput("account:1", "owner", "project"))
			if _, ok, err := store.ClaimNext(nil); err != nil || !ok {
				t.Fatalf("claim ok=%v err=%v", ok, err)
			}
			if test.delivering {
				if _, err := store.BeginDelivery("owner", task.ID); err != nil {
					t.Fatal(err)
				}
			}
			recovered, err := newStore(root, func() time.Time { return base.Add(time.Minute) })
			if err != nil {
				t.Fatal(err)
			}
			got, ok := recovered.Find("owner", task.ID)
			if !ok || got.State != StateInterrupted || got.Reason != test.reason || got.PayloadExpiresAt <= base.Unix() {
				t.Fatalf("recovered task = %#v ok=%v", got, ok)
			}
			if _, ok, err := recovered.ClaimNext(nil); err != nil || ok {
				t.Fatalf("interrupted task retried automatically: ok=%v err=%v", ok, err)
			}
			if _, err := recovered.LoadRequest("owner", task.ID); err != nil {
				t.Fatalf("interrupted request should remain: %v", err)
			}
			expired, err := newStore(root, func() time.Time { return base.Add(25 * time.Hour) })
			if err != nil {
				t.Fatal(err)
			}
			if _, err := expired.LoadRequest("owner", task.ID); err == nil {
				t.Fatal("expired interrupted payload was still readable")
			}
			if _, err := os.Stat(filepath.Join(root, task.ID)); !os.IsNotExist(err) {
				t.Fatalf("expired payload still exists: %v", err)
			}
		})
	}
}

func TestStoreRejectsStrictIndexAndTamperedRequest(t *testing.T) {
	t.Run("unknown index field", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "tasks")
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		data := `{"version":1,"next_order":1,"owners":{},"extra":true}`
		if err := os.WriteFile(filepath.Join(root, "index.json"), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewStore(root); err == nil {
			t.Fatal("task store accepted an unknown index field")
		}
	})

	t.Run("tampered request", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "tasks")
		store, err := NewStore(root)
		if err != nil {
			t.Fatal(err)
		}
		input := testEnqueueInput("account:1", "owner", "project")
		input.Files = []InputAttachment{{Name: "data.json", ContentType: "application/json", Data: []byte(`{"safe":true}`)}}
		task := mustEnqueue(t, store, input)
		loaded, err := store.LoadRequest("owner", task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(loaded.Files[0].AbsolutePath, []byte(`{"safe":false}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.LoadRequest("owner", task.ID); err == nil || !strings.Contains(err.Error(), "changed") {
			t.Fatalf("tampered payload error = %v", err)
		}

		requestPath := filepath.Join(root, task.ID, "request.json")
		requestData, err := os.ReadFile(requestPath)
		if err != nil {
			t.Fatal(err)
		}
		var request map[string]any
		if err := json.Unmarshal(requestData, &request); err != nil {
			t.Fatal(err)
		}
		request["unknown"] = true
		requestData, _ = json.Marshal(request)
		if err := os.WriteFile(requestPath, requestData, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.LoadRequest("owner", task.ID); err == nil {
			t.Fatal("task store accepted an unknown request field")
		}
	})
}

func TestStoreConcurrentSourceDeduplicationAndQueueLimit(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	input := testEnqueueInput("account:same", "owner", "project")
	var wait sync.WaitGroup
	ids := make(chan string, 12)
	errors := make(chan error, 12)
	for range 12 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			task, _, enqueueErr := store.Enqueue(input)
			if enqueueErr != nil {
				errors <- enqueueErr
				return
			}
			ids <- task.ID
		}()
	}
	wait.Wait()
	close(ids)
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	unique := make(map[string]struct{})
	for id := range ids {
		unique[id] = struct{}{}
	}
	if len(unique) != 1 || len(store.List("owner")) != 1 {
		t.Fatalf("deduplicated ids=%d tasks=%d", len(unique), len(store.List("owner")))
	}
	for index := 1; index < MaxQueuedPerOwner; index++ {
		mustEnqueue(t, store, testEnqueueInput("account:"+string(rune('a'+index)), "owner", "project"))
	}
	if _, _, err := store.Enqueue(testEnqueueInput("account:overflow", "owner", "project")); err == nil {
		t.Fatal("queue accepted more than the owner limit")
	}
	cleared, err := store.ClearQueued("owner")
	if err != nil || cleared != MaxQueuedPerOwner {
		t.Fatalf("cleared=%d err=%v", cleared, err)
	}
	if status := store.Status("owner"); status.Queued != 0 || status.Cancelled != MaxTerminalPerOwner {
		t.Fatalf("status after clear = %#v", status)
	}
}

func TestStoreCleansAbandonedStagingAndOrphanTaskDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "tasks")
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	_ = store
	staging := filepath.Join(root, ".staging-abandoned")
	orphan := filepath.Join(root, "task-00000000000000000000000000000000")
	if err := os.Mkdir(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(orphan, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(root); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{staging, orphan} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("abandoned path still exists: %s err=%v", path, err)
		}
	}
}

func TestStoreRejectsMissingQueuedPayload(t *testing.T) {
	root := filepath.Join(t.TempDir(), "tasks")
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	task := mustEnqueue(t, store, testEnqueueInput("account:missing", "owner", "project"))
	if err := os.RemoveAll(filepath.Join(root, task.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(root); err == nil || !strings.Contains(err.Error(), "payload directory") {
		t.Fatalf("missing payload startup error = %v", err)
	}
}

func TestStoreCleansExpiredPayloadWhileRunning(t *testing.T) {
	root := filepath.Join(t.TempDir(), "tasks")
	clock := time.Date(2026, 8, 5, 20, 0, 0, 0, time.Local)
	store, err := newStore(root, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	task := mustEnqueue(t, store, testEnqueueInput("account:old", "owner", "project"))
	if _, ok, err := store.ClaimNext(nil); err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	if _, err := store.Finish("owner", task.ID, StateFailed, ReasonCodexFailed); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(25 * time.Hour)
	if err := store.CleanupExpired(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, task.ID)); !os.IsNotExist(err) {
		t.Fatalf("expired payload still exists: %v", err)
	}
	if _, err := store.LoadRequest("owner", task.ID); err == nil {
		t.Fatal("expired payload remained readable")
	}
}

func testEnqueueInput(source, owner, project string) EnqueueInput {
	return EnqueueInput{
		SourceMessageKey: source, OwnerID: owner, ProjectID: project,
		Summary: "测试任务", Text: "执行测试任务",
		ResponseMode: preference.ResponseAdaptive, VisualStyle: visual.StyleEditorial,
	}
}

func mustEnqueue(t *testing.T, store *Store, input EnqueueInput) Task {
	t.Helper()
	task, existed, err := store.Enqueue(input)
	if err != nil || existed {
		t.Fatalf("enqueue = %#v existed=%v err=%v", task, existed, err)
	}
	return task
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode for %s = %v, want %v", path, got, want)
	}
}
