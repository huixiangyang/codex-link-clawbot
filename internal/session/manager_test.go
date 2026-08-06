package session

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/huixiangyang/weclaw/internal/codex"
)

type fakeThreadClient struct {
	mu           sync.Mutex
	next         int
	threads      map[string]codex.ThreadInfo
	archived     map[string]bool
	resumed      []string
	unsubscribed []string
	listOptions  []codex.ThreadListOptions
}

func newFakeThreadClient() *fakeThreadClient {
	return &fakeThreadClient{
		threads:  make(map[string]codex.ThreadInfo),
		archived: make(map[string]bool),
	}
}

func (f *fakeThreadClient) StartThread(context.Context) (codex.ThreadInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	id := fmt.Sprintf("019fcc03-fc8b-7842-a812-%012d", f.next)
	thread := codex.ThreadInfo{
		ID: id, Preview: fmt.Sprintf("会话 %d", f.next),
		Cwd: "/workspace", CreatedAt: int64(100 + f.next), UpdatedAt: int64(100 + f.next),
		Status: codex.ThreadStatus{Type: "idle"},
	}
	f.threads[id] = thread
	return thread, nil
}

func (f *fakeThreadClient) ResumeThread(_ context.Context, threadID string) (codex.ThreadInfo, error) {
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

func (f *fakeThreadClient) UnarchiveThread(_ context.Context, threadID string) (codex.ThreadInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	thread, ok := f.threads[threadID]
	if !ok || !f.archived[threadID] {
		return codex.ThreadInfo{}, fmt.Errorf("archived thread not found")
	}
	delete(f.archived, threadID)
	return thread, nil
}

func (f *fakeThreadClient) UnsubscribeThread(_ context.Context, threadID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unsubscribed = append(f.unsubscribed, threadID)
	return nil
}

func (f *fakeThreadClient) ChatThread(context.Context, string, codex.ChatRequest) (string, error) {
	return "ok", nil
}

func TestManagerPersistsSelectionAcrossRestart(t *testing.T) {
	path := t.TempDir() + "/session-index.json"
	client := newFakeThreadClient()
	manager, err := NewManager(path)
	if err != nil {
		t.Fatal(err)
	}
	manager.now = func() time.Time { return time.Unix(1000, 0) }
	first, err := manager.New(context.Background(), "owner-1", client, "第一个会话")
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.New(context.Background(), "owner-1", client, "第二个会话")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatal("new sessions should have different thread ids")
	}
	selected, err := manager.Use(context.Background(), "owner-1", client, ShortCode(first.ID))
	if err != nil || selected.ID != first.ID {
		t.Fatalf("Use() = %#v, %v", selected, err)
	}

	restarted, err := NewManager(path)
	if err != nil {
		t.Fatal(err)
	}
	current, err := restarted.Current(context.Background(), "owner-1", client)
	if err != nil || current.Info.ID != first.ID {
		t.Fatalf("Current() after restart = %#v, %v", current, err)
	}
	if _, err := restarted.Use(context.Background(), "owner-2", client, ShortCode(first.ID)); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("foreign Use() error = %v, want ErrNotOwned", err)
	}
}

func TestManagerDetailEnforcesOwnershipAndArchiveState(t *testing.T) {
	manager, err := NewManager(t.TempDir() + "/session-index.json")
	if err != nil {
		t.Fatal(err)
	}
	client := newFakeThreadClient()
	thread, err := manager.New(context.Background(), "owner-1", client, "详情测试")
	if err != nil {
		t.Fatal(err)
	}
	detail, err := manager.Detail(context.Background(), "owner-1", client, thread.ID, false)
	if err != nil || detail.Info.ID != thread.ID || !detail.Current || detail.Archived {
		t.Fatalf("Detail() = %#v, %v", detail, err)
	}
	if _, err := manager.Detail(context.Background(), "owner-2", client, thread.ID, false); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("foreign Detail() error = %v, want ErrNotOwned", err)
	}
	if _, err := manager.Archive(context.Background(), "owner-1", client, thread.ID); err != nil {
		t.Fatal(err)
	}
	archived, err := manager.Detail(context.Background(), "owner-1", client, thread.ID, true)
	if err != nil || !archived.Archived || archived.Current {
		t.Fatalf("archived Detail() = %#v, %v", archived, err)
	}
}

func TestEnsureActiveNamesAnExistingUnnamedSession(t *testing.T) {
	manager, err := NewManager(t.TempDir() + "/session-index.json")
	if err != nil {
		t.Fatal(err)
	}
	client := newFakeThreadClient()
	unnamed, err := manager.New(context.Background(), "owner-1", client, "")
	if err != nil {
		t.Fatal(err)
	}
	active, err := manager.EnsureActive(context.Background(), "owner-1", client, "发布检查")
	if err != nil {
		t.Fatal(err)
	}
	if active.ID != unnamed.ID || active.Name != "发布检查" || client.threads[unnamed.ID].Name != "发布检查" {
		t.Fatalf("EnsureActive() = %#v, stored = %#v", active, client.threads[unnamed.ID])
	}
}

func TestManagerListsRenamesArchivesAndRestores(t *testing.T) {
	manager, err := NewManager(t.TempDir() + "/session-index.json")
	if err != nil {
		t.Fatal(err)
	}
	client := newFakeThreadClient()
	first, err := manager.New(context.Background(), "owner-1", client, "第一项")
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.New(context.Background(), "owner-1", client, "第二项")
	if err != nil {
		t.Fatal(err)
	}
	if stats := manager.Stats("owner-1"); stats.Active != 2 || stats.Archived != 0 || !stats.HasCurrent || stats.CurrentID != second.ID {
		t.Fatalf("Stats() before archive = %#v", stats)
	}
	renamed, err := manager.Rename(context.Background(), "owner-1", client, "发布排障")
	if err != nil || renamed.Name != "发布排障" {
		t.Fatalf("Rename() = %#v, %v", renamed, err)
	}
	client.threads["019fcc03-fc8b-7842-a812-999999999999"] = codex.ThreadInfo{
		ID: "019fcc03-fc8b-7842-a812-999999999999", Name: "其他客户端会话", UpdatedAt: 999,
	}
	page, err := manager.List(context.Background(), "owner-1", client, false, 1, 1)
	if err != nil || page.Total != 2 || page.TotalPages != 2 || len(page.Items) != 1 {
		t.Fatalf("List() = %#v, %v", page, err)
	}
	if !page.Items[0].Current || page.Items[0].Info.ID != second.ID {
		t.Fatalf("first list item = %#v, want current second thread", page.Items[0])
	}
	if len(client.listOptions) == 0 || len(client.listOptions[0].SourceKinds) == 0 {
		t.Fatal("List() should request every Codex source before applying ownership filtering")
	}

	next, err := manager.Archive(context.Background(), "owner-1", client, "")
	if err != nil || next != first.ID {
		t.Fatalf("Archive() next = %q, err = %v", next, err)
	}
	current, err := manager.Current(context.Background(), "owner-1", client)
	if err != nil || current.Info.ID != first.ID {
		t.Fatalf("Current() after archive = %#v, %v", current, err)
	}
	archivedPage, err := manager.List(context.Background(), "owner-1", client, true, 1, 6)
	if err != nil || archivedPage.Total != 1 || archivedPage.Items[0].Info.ID != second.ID {
		t.Fatalf("archived List() = %#v, %v", archivedPage, err)
	}
	if stats := manager.Stats("owner-1"); stats.Active != 1 || stats.Archived != 1 || stats.CurrentID != first.ID {
		t.Fatalf("Stats() after archive = %#v", stats)
	}
	restored, err := manager.Restore(context.Background(), "owner-1", client, ShortCode(second.ID))
	if err != nil || restored.ID != second.ID {
		t.Fatalf("Restore() = %#v, %v", restored, err)
	}
	activePage, err := manager.List(context.Background(), "owner-1", client, false, 1, 6)
	if err != nil || activePage.Total != 2 {
		t.Fatalf("active List() = %#v, %v", activePage, err)
	}
	if stats := manager.Stats("owner-1"); stats.Active != 2 || stats.Archived != 0 {
		t.Fatalf("Stats() after restore = %#v", stats)
	}
}

func TestManagerValidatesSessionNames(t *testing.T) {
	manager, err := NewManager(t.TempDir() + "/session-index.json")
	if err != nil {
		t.Fatal(err)
	}
	client := newFakeThreadClient()
	if _, err := manager.New(context.Background(), "owner-1", client, "两行\n名称"); err == nil {
		t.Fatal("New() should reject multiline name")
	}
	if _, err := manager.New(context.Background(), "owner-1", client, string(make([]rune, MaxSessionName+1))); err == nil {
		t.Fatal("New() should reject oversized name")
	}
}
