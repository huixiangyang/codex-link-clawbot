package taskqueue

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func finishSuccessfulTask(t *testing.T, store *Store, ownerID, taskID string) {
	t.Helper()
	if _, claimed, err := store.ClaimNext(nil); err != nil || !claimed {
		t.Fatalf("ClaimNext() claimed=%v err=%v", claimed, err)
	}
	if _, err := store.FreezeResult(ownerID, taskID, FreezeResultInput{Reply: "不得保留的回答正文"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginDelivery(ownerID, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Finish(ownerID, taskID, StateSucceeded, ""); err != nil {
		t.Fatal(err)
	}
}

func TestSuccessfulTextTaskRetainsOnlyReusablePromptForOneDay(t *testing.T) {
	root := filepath.Join(t.TempDir(), "tasks")
	clock := time.Date(2026, 8, 5, 21, 0, 0, 0, time.Local)
	store, err := newStore(root, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	input := testEnqueueInput("source:reusable", "owner", "project")
	input.Text = "继续检查这个发布流程"
	input.ContextToken = "不得保留的会话令牌"
	task := mustEnqueue(t, store, input)
	finishSuccessfulTask(t, store, "owner", task.ID)

	prompt, err := store.LoadReusablePrompt("owner", task.ID)
	if err != nil || prompt != input.Text {
		t.Fatalf("LoadReusablePrompt() = %q, %v", prompt, err)
	}
	raw, err := os.ReadFile(filepath.Join(root, task.ID, reusablePromptFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{input.ContextToken, "不得保留的回答正文", input.SourceMessageKey} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("reusable prompt leaked %q: %s", forbidden, raw)
		}
	}

	reopened, err := newStore(root, func() time.Time { return clock.Add(time.Hour) })
	if err != nil {
		t.Fatal(err)
	}
	if prompt, err := reopened.LoadReusablePrompt("owner", task.ID); err != nil || prompt != input.Text {
		t.Fatalf("reopened reusable prompt = %q, %v", prompt, err)
	}
	clock = clock.Add(25 * time.Hour)
	if err := store.CleanupExpired(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadReusablePrompt("owner", task.ID); err == nil {
		t.Fatal("expired reusable prompt remained readable")
	}
	if _, err := os.Stat(filepath.Join(root, task.ID)); !os.IsNotExist(err) {
		t.Fatalf("expired reusable prompt directory still exists: %v", err)
	}
}

func TestSuccessfulAttachmentTaskRetainsNoReusablePayload(t *testing.T) {
	root := filepath.Join(t.TempDir(), "tasks")
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	input := testEnqueueInput("source:attachment", "owner", "project")
	input.Images = []InputAttachment{{Name: "screen.png", ContentType: "image/png", Data: []byte("image")}}
	task := mustEnqueue(t, store, input)
	finishSuccessfulTask(t, store, "owner", task.ID)
	if _, err := store.LoadReusablePrompt("owner", task.ID); err == nil {
		t.Fatal("attachment task exposed an incomplete reusable prompt")
	}
	if _, err := os.Stat(filepath.Join(root, task.ID)); !os.IsNotExist(err) {
		t.Fatalf("successful attachment payload still exists: %v", err)
	}
}
