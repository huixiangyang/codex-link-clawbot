package execution

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/presentation"
	"github.com/huixiangyang/codex-link-clawbot/internal/request"
)

type controlledExecutor struct {
	allowed bool
	run     func(context.Context, request.Task, func() bool)
}

func (e *controlledExecutor) CanExecute(string) bool { return e.allowed }
func (e *controlledExecutor) Execute(ctx context.Context, task request.Task, finalize func() bool) {
	e.run(ctx, task, finalize)
}

func TestImmediateAdmissionRejectsBusyAndRunsIndependentSessions(t *testing.T) {
	store, err := request.NewStore(filepath.Join(t.TempDir(), "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan string, 2)
	cancelled := make(chan string, 2)
	executor := &controlledExecutor{allowed: true, run: func(ctx context.Context, task request.Task, finalize func() bool) {
		started <- task.ID
		<-ctx.Done()
		if finalize() {
			cancelled <- task.ID
		}
		_, _ = store.Finish(task.OwnerID, task.ID, request.StateCancelled, request.ReasonUserCancelled)
	}}
	c, err := NewCoordinator(store, executor)
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = c.Run(ctx); close(done) }()
	t.Cleanup(func() {
		stop()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("shutdown stuck")
		}
	})
	first, err := c.Begin("owner", "target-a", "thread-a", "first-source")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Begin("owner", "target-a", "thread-a", ""); !errors.Is(err, request.ErrSessionBusy) {
		t.Fatalf("preflight not protected: %v", err)
	}
	if _, err := c.Begin("owner", "target-a", "thread-a", "first-source"); !errors.Is(err, ErrDuplicateSource) {
		t.Fatal("source replay during input staging was not deduplicated")
	}
	second, err := c.Begin("owner", "target-b", "thread-b", "")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i, admission := range []*Admission{first, second} {
		name := []string{"a", "b"}[i]
		task, _, err := store.Start(request.StartInput{OwnerID: "owner", TargetID: "target-" + name, ThreadID: "thread-" + name, ProjectID: "workspace", SourceMessageKey: name, Text: "执行", Summary: "执行", ResponseMode: presentation.ResponseAdaptive, VisualStyle: presentation.DefaultStyle})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, task.ID)
		admission.Launch(task)
	}
	for range 2 {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("independent session waited")
		}
	}
	if _, err := c.Begin("owner", "other-intent", "thread-a", ""); !errors.Is(err, request.ErrSessionBusy) {
		t.Fatal("same native thread bypassed busy guard")
	}
	if c.Cancel("owner", "stale-id") || c.Cancel("foreign", ids[0]) {
		t.Fatal("cancelled wrong execution")
	}
	if !c.Cancel("owner", ids[0]) || c.Cancel("owner", ids[0]) {
		t.Fatal("cancellation not idempotent")
	}
	select {
	case id := <-cancelled:
		if id != ids[0] {
			t.Fatal("wrong session cancelled")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel not delivered")
	}
	if task, _ := store.Find("owner", ids[1]); task.State != request.StateRunning {
		t.Fatal("second session affected")
	}
	c.SetDraining(true)
	if _, err := c.Begin("owner", "target-c", "thread-c", ""); err == nil {
		t.Fatal("drain admitted work")
	}
	c.SetDraining(false)
	idle, err := c.Begin("owner", "target-c", "thread-c", "")
	if err != nil {
		t.Fatal(err)
	}
	idle.Release()
}

func TestFinalizingExecutionCannotBeCancelled(t *testing.T) {
	store, err := request.NewStore(filepath.Join(t.TempDir(), "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	finalizing := make(chan struct{})
	finish := make(chan struct{})
	executor := &controlledExecutor{allowed: true, run: func(_ context.Context, _ request.Task, finalize func() bool) {
		if finalize() {
			t.Error("unexpected cancellation")
		}
		close(finalizing)
		<-finish
	}}
	c, _ := NewCoordinator(store, executor)
	admission, err := c.Begin("owner", "target", "thread", "")
	if err != nil {
		t.Fatal(err)
	}
	admission.Launch(request.Task{ID: "task", OwnerID: "owner"})
	<-finalizing
	if c.Cancel("owner", "task") {
		t.Error("cancelled finalization")
	}
	close(finish)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = c.Run(ctx)
}
