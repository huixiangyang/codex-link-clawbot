package conversation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/core/codex"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/request"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/target"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/thread"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/workspace"
)

type controlledClient struct {
	codex.ThreadClient
	list    func(context.Context) (codex.ThreadPage, error)
	start   func(context.Context, string) (codex.ThreadInfo, error)
	archive func(context.Context, string) error
}

func (c controlledClient) ListThreads(ctx context.Context, _ codex.ThreadListOptions) (codex.ThreadPage, error) {
	return c.list(ctx)
}
func (c controlledClient) StartThread(ctx context.Context, root string) (codex.ThreadInfo, error) {
	return c.start(ctx, root)
}
func (c controlledClient) ArchiveThread(ctx context.Context, id string) error {
	return c.archive(ctx, id)
}

func serviceFixture(t *testing.T, client codex.ThreadClient) *Service {
	t.Helper()
	root := t.TempDir()
	targets, err := target.Open(root, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	requests, err := request.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	workspaces, err := workspace.NewManager([]workspace.Definition{{ID: "alpha", Root: t.TempDir()}, {ID: "beta", Root: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	threads, err := thread.NewManager()
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(Dependencies{Workspaces: workspaces, Threads: threads, Targets: targets, Requests: requests, Client: client, Control: struct{ Control }{}})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestSlowSelectionCannotBlockOrOverwriteNewerSelection(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	client := &controlledClient{}
	service := serviceFixture(t, client)
	definition, _ := service.deps.Workspaces.Get("alpha")
	client.list = func(ctx context.Context) (codex.ThreadPage, error) {
		close(entered)
		select {
		case <-release:
			return codex.ThreadPage{Threads: []codex.ThreadInfo{{ID: "remote", Cwd: definition.Root}}}, nil
		case <-ctx.Done():
			return codex.ThreadPage{}, ctx.Err()
		}
	}
	first, err := service.Select(context.Background(), "owner", "alpha", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := service.Select(ctx, "owner", "alpha", "remote"); done <- err }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("remote selection did not start")
	}
	// 切走再切回同一意图也应失效旧操作，不能只比较最终目标值。
	for _, id := range []string{"beta", "alpha"} {
		if _, err := service.Select(ctx, "owner", id, ""); err != nil {
			t.Fatal(err)
		}
	}
	if ctx.Err() != nil {
		t.Fatal("remote lookup blocked workspace selection")
	}
	close(release)
	if err := <-done; !errors.Is(err, target.ErrSelectionChanged) {
		t.Fatalf("stale selection = %v", err)
	}
	if service.deps.Targets.Current("owner") != first {
		t.Fatal("stale remote operation stole focus")
	}
}

func TestCancelledCreationArchivesUncommittedThread(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	archived := false
	client := controlledClient{
		start: func(context.Context, string) (codex.ThreadInfo, error) {
			cancel()
			return codex.ThreadInfo{ID: "created"}, nil
		},
		archive: func(ctx context.Context, id string) error {
			if ctx.Err() != nil || id != "created" {
				t.Fatal("cleanup inherited cancellation or wrong identity")
			}
			if _, bounded := ctx.Deadline(); !bounded {
				t.Fatal("cleanup has no deadline")
			}
			archived = true
			return nil
		},
	}
	service := serviceFixture(t, client)
	before := service.deps.Targets.Current("owner")
	if _, err := service.Create(ctx, "owner", "alpha", ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("create = %v", err)
	}
	if !archived || service.deps.Targets.Current("owner") != before {
		t.Fatal("uncommitted creation was not compensated")
	}
}
