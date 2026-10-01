package wechat

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/huixiangyang/codex-link-clawbot/internal/core/codex"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/thread"
)

type handlerThreadClient struct {
	mu           sync.Mutex
	chat         func(context.Context, string, codex.ChatRequest) (string, error)
	next         int
	threads      map[string]codex.ThreadInfo
	archived     map[string]bool
	chatThreadID string
	cwd          string
}

func newHandlerThreadClient() *handlerThreadClient {
	return &handlerThreadClient{threads: make(map[string]codex.ThreadInfo), archived: make(map[string]bool), cwd: "/workspace"}
}

func (a *handlerThreadClient) Info() codex.RuntimeInfo {
	return codex.RuntimeInfo{Command: "codex", PID: 4242}
}

func (a *handlerThreadClient) StartThread(_ context.Context, workspaceRoot string) (codex.ThreadInfo, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cwd = workspaceRoot
	a.next++
	id := fmt.Sprintf("019fcc03-fc8b-7842-a812-%012d", a.next)
	item := codex.ThreadInfo{
		ID: id, Preview: fmt.Sprintf("测试线程 %d", a.next), Cwd: a.cwd,
		CreatedAt: int64(100 + a.next), UpdatedAt: int64(100 + a.next), Status: codex.ThreadStatus{Type: "idle"},
	}
	a.threads[id] = item
	return item, nil
}

func (a *handlerThreadClient) ResumeThread(_ context.Context, threadID, workspaceRoot string) (codex.ThreadInfo, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cwd = workspaceRoot
	item, exists := a.threads[threadID]
	if !exists || a.archived[threadID] {
		return codex.ThreadInfo{}, fmt.Errorf("thread unavailable")
	}
	return item, nil
}

func (a *handlerThreadClient) ReadThread(_ context.Context, threadID string) (codex.ThreadInfo, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	item, exists := a.threads[threadID]
	if !exists {
		return codex.ThreadInfo{}, fmt.Errorf("thread not found")
	}
	return item, nil
}

func (a *handlerThreadClient) ListThreads(_ context.Context, options codex.ThreadListOptions) (codex.ThreadPage, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	items := make([]codex.ThreadInfo, 0, len(a.threads))
	for id, item := range a.threads {
		search := strings.ToLower(strings.TrimSpace(options.SearchTerm))
		searchable := strings.ToLower(item.ID + " " + item.Name + " " + item.Preview)
		if a.archived[id] == options.Archived && (search == "" || strings.Contains(searchable, search)) {
			items = append(items, item)
		}
	}
	return codex.ThreadPage{Threads: items}, nil
}

func (a *handlerThreadClient) SetThreadName(_ context.Context, threadID, name string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	item, exists := a.threads[threadID]
	if !exists {
		return fmt.Errorf("thread not found")
	}
	item.Name = name
	a.threads[threadID] = item
	return nil
}

func (a *handlerThreadClient) ArchiveThread(_ context.Context, threadID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, exists := a.threads[threadID]; !exists {
		return fmt.Errorf("thread not found")
	}
	a.archived[threadID] = true
	return nil
}

func (a *handlerThreadClient) ChatThread(ctx context.Context, threadID string, input codex.ChatRequest) (string, error) {
	a.mu.Lock()
	a.chatThreadID = threadID
	chat := a.chat
	a.mu.Unlock()
	if chat != nil {
		return chat(ctx, threadID, input)
	}
	return "显式线程回复", nil
}

func attachTestSessionManager(t *testing.T, handler *testHandler) {
	t.Helper()
	manager, err := thread.NewManager()
	if err != nil {
		t.Fatal(err)
	}
	handler.sessions = manager
}
