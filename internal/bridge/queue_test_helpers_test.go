package bridge

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/preference"
	"github.com/huixiangyang/codex-link-clawbot/internal/request"
	"github.com/huixiangyang/codex-link-clawbot/internal/workspace"
)

func attachTestTaskQueue(t *testing.T, handler *Handler, client *ilink.Client, ownerID string) (*request.Store, context.CancelFunc) {
	t.Helper()
	projects, err := workspace.NewManager([]workspace.Definition{{
		ID: "workspace", Name: "Workspace", Root: t.TempDir(),
	}}, filepath.Join(t.TempDir(), "projects.json"))
	if err != nil {
		t.Fatal(err)
	}
	preferences, err := preference.NewStore(filepath.Join(t.TempDir(), "preferences.json"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := request.NewStore(filepath.Join(t.TempDir(), "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	handler.projects = projects
	handler.preferences = preferences
	coordinator, err := newCoordinator(handler, store)
	if err != nil {
		t.Fatal(err)
	}
	handler.tasks = store
	handler.coordinator = coordinator
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

func waitForTerminalTask(t *testing.T, store *request.Store, ownerID string) request.Task {
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
	return request.Task{}
}
