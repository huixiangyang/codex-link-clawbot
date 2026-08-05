package messaging

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/huixiangyang/weclaw/config"
	"github.com/huixiangyang/weclaw/ilink"
	"github.com/huixiangyang/weclaw/preference"
	"github.com/huixiangyang/weclaw/project"
	"github.com/huixiangyang/weclaw/taskqueue"
)

func attachTestTaskQueue(t *testing.T, handler *Handler, client *ilink.Client, ownerID string) (*taskqueue.Store, context.CancelFunc) {
	t.Helper()
	projects, err := project.NewManager([]config.ProjectConfig{{
		ID: "workspace", Name: "Workspace", Root: t.TempDir(),
	}}, filepath.Join(t.TempDir(), "projects.json"))
	if err != nil {
		t.Fatal(err)
	}
	preferences, err := preference.NewStore(filepath.Join(t.TempDir(), "preferences.json"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := taskqueue.NewStore(filepath.Join(t.TempDir(), "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	handler.SetProjectManager(projects)
	handler.SetPreferenceStore(preferences)
	coordinator, err := NewCoordinator(handler, store)
	if err != nil {
		t.Fatal(err)
	}
	handler.SetTaskQueue(store, coordinator)
	coordinator.RegisterOwnerClient(ownerID, client)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = coordinator.Run(ctx)
	}()
	return store, func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("test coordinator did not stop")
		}
	}
}

func waitForTerminalTask(t *testing.T, store *taskqueue.Store, ownerID string) taskqueue.Task {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, task := range store.List(ownerID) {
			if task.State.Terminal() {
				return task
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("task for %s did not reach a terminal state: %#v", ownerID, store.List(ownerID))
	return taskqueue.Task{}
}
