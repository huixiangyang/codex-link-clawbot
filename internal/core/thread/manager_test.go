package thread

import (
	"context"

	"fmt"

	"sort"
	"sync"
	"testing"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/core/codex"
)

type fakeThreadClient struct {
	mu           sync.Mutex
	next         int
	threads      map[string]codex.ThreadInfo
	archived     map[string]bool
	resumed      []string
	unsubscribed []string
	listOptions  []codex.ThreadListOptions
	compacted    []string
	steered      []string
}

func newFakeThreadClient() *fakeThreadClient {
	return &fakeThreadClient{
		threads:  make(map[string]codex.ThreadInfo),
		archived: make(map[string]bool),
	}
}

func (f *fakeThreadClient) StartThread(_ context.Context, workspaceRoot string) (codex.ThreadInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	id := fmt.Sprintf("019fcc03-fc8b-7842-a812-%012d", f.next)
	thread := codex.ThreadInfo{
		ID: id, Preview: fmt.Sprintf("会话 %d", f.next),
		Cwd: workspaceRoot, CreatedAt: int64(100 + f.next), UpdatedAt: int64(100 + f.next),
		Status: codex.ThreadStatus{Type: "idle"},
	}
	f.threads[id] = thread
	return thread, nil
}

func (f *fakeThreadClient) ResumeThread(_ context.Context, threadID, _ string) (codex.ThreadInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	thread, ok := f.threads[threadID]
	if !ok || f.archived[threadID] {
		return codex.ThreadInfo{}, fmt.Errorf("thread not available")
	}
	f.resumed = append(f.resumed, threadID)
	thread.Status = codex.ThreadStatus{Type: "idle"}
	f.threads[threadID] = thread
	return thread, nil
}

func (f *fakeThreadClient) ReadThread(_ context.Context, threadID string) (codex.ThreadInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	thread, ok := f.threads[threadID]
	if !ok {
		return codex.ThreadInfo{}, fmt.Errorf("thread not found")
	}
	return thread, nil
}

func (f *fakeThreadClient) ListThreads(_ context.Context, options codex.ThreadListOptions) (codex.ThreadPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listOptions = append(f.listOptions, options)
	var threads []codex.ThreadInfo
	for id, thread := range f.threads {
		if f.archived[id] == options.Archived {
			threads = append(threads, thread)
		}
	}
	sort.Slice(threads, func(i, j int) bool { return threads[i].UpdatedAt > threads[j].UpdatedAt })
	return codex.ThreadPage{Threads: threads}, nil
}

func (f *fakeThreadClient) SetThreadName(_ context.Context, threadID, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	thread, ok := f.threads[threadID]
	if !ok {
		return fmt.Errorf("thread not found")
	}
	thread.Name = name
	f.threads[threadID] = thread
	return nil
}

func (f *fakeThreadClient) ArchiveThread(_ context.Context, threadID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.threads[threadID]; !ok {
		return fmt.Errorf("thread not found")
	}
	f.archived[threadID] = true
	return nil
}

func (f *fakeThreadClient) ChatThread(context.Context, string, codex.ChatRequest) (string, error) {
	return "ok", nil
}

func newTestManager(path string) (*Manager, error) { return NewManager() }

type delayedCatalog struct {
	codex.ThreadClient
	list func(context.Context) (codex.ThreadPage, error)
}

func (c delayedCatalog) ListThreads(ctx context.Context, _ codex.ThreadListOptions) (codex.ThreadPage, error) {
	return c.list(ctx)
}

func TestInvalidatedCatalogCannotBeRepopulatedByOldLookup(t *testing.T) {
	manager, _ := NewManager()
	entered, release := make(chan struct{}), make(chan struct{})
	calls := 0
	client := delayedCatalog{list: func(ctx context.Context) (codex.ThreadPage, error) {
		calls++
		if calls == 1 {
			close(entered)
			select {
			case <-release:
				return codex.ThreadPage{Threads: []codex.ThreadInfo{{ID: "stale"}}}, nil
			case <-ctx.Done():
				return codex.ThreadPage{}, ctx.Err()
			}
		}
		return codex.ThreadPage{}, nil
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := manager.directory(ctx, client, false, ""); done <- err }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("catalog lookup did not start")
	}
	manager.Invalidate()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	items, err := manager.directory(ctx, client, false, "")
	if err != nil || len(items) != 0 || calls != 2 {
		t.Fatalf("stale directory restored: %+v, %v, calls=%d", items, err, calls)
	}
}
