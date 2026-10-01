package wechat

import (
	"context"
	"testing"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/adapters/wechat/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/execution"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/preference"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/request"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/workspace"
)

func attachTestExecution(t *testing.T, handler *testHandler, client *ilink.Client, ownerID string) (*request.Store, context.CancelFunc) {
	t.Helper()
	projects, err := workspace.NewManager([]workspace.Definition{{
		ID: "workspace", Name: "Workspace", Root: t.TempDir(),
	}})
	if err != nil {
		t.Fatal(err)
	}
	preferences, err := preference.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := request.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	handler.projects = projects
	handler.preferences = preferences
	coordinator, err := newTestCoordinator(handler, store)
	if err != nil {
		t.Fatal(err)
	}
	handler.tasks = store
	handler.coordinator = coordinator
	handler.clients.register(ownerID, client)
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

func waitForEffects(t *testing.T, c *execution.Coordinator) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for c.EffectsActive() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if c.EffectsActive() {
		t.Fatal("channel effects did not finish")
	}
}
