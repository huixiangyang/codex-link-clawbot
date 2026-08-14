package thread

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/codex"
)

type fakeThreadClient struct {
	mu           sync.Mutex
	next         int
	threads      map[string]codex.ThreadInfo
	archived     map[string]bool
	resumed      []string
	unsubscribed []string
	listOptions  []codex.ThreadListOptions
	goals        map[string]codex.ThreadGoal
	compacted    []string
	steered      []string
}

func newFakeThreadClient() *fakeThreadClient {
	return &fakeThreadClient{
		threads:  make(map[string]codex.ThreadInfo),
		archived: make(map[string]bool),
		goals:    make(map[string]codex.ThreadGoal),
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

func (f *fakeThreadClient) ForkThread(_ context.Context, threadID string) (codex.ThreadInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	source, ok := f.threads[threadID]
	if !ok {
		return codex.ThreadInfo{}, fmt.Errorf("thread not found")
	}
	f.next++
	id := fmt.Sprintf("019fcc03-fc8b-7842-a812-%012d", f.next)
	forked := source
	forked.ID = id
	forked.ForkedFromID = threadID
	forked.Name = ""
	f.threads[id] = forked
	return forked, nil
}

func (f *fakeThreadClient) SetThreadPinned(_ context.Context, threadID string, pinned bool) (codex.ThreadInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	thread, ok := f.threads[threadID]
	if !ok {
		return codex.ThreadInfo{}, fmt.Errorf("thread not found")
	}
	thread.IsPinned = pinned
	f.threads[threadID] = thread
	return thread, nil
}

func (f *fakeThreadClient) CompactThread(_ context.Context, threadID string) error {
	f.compacted = append(f.compacted, threadID)
	return nil
}

func (f *fakeThreadClient) DeleteThread(_ context.Context, threadID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.threads[threadID]; !ok {
		return fmt.Errorf("thread not found")
	}
	deleted := map[string]bool{threadID: true}
	for changed := true; changed; {
		changed = false
		for id, thread := range f.threads {
			if !deleted[id] && deleted[thread.ForkedFromID] {
				deleted[id] = true
				changed = true
			}
		}
	}
	for id := range deleted {
		delete(f.threads, id)
		delete(f.goals, id)
	}
	return nil
}

func (f *fakeThreadClient) SetThreadGoal(_ context.Context, threadID, objective string, tokenBudget *int64) (codex.ThreadGoal, error) {
	goal := codex.ThreadGoal{ThreadID: threadID, Objective: objective, Status: "active", TokenBudget: tokenBudget}
	f.goals[threadID] = goal
	return goal, nil
}

func (f *fakeThreadClient) GetThreadGoal(_ context.Context, threadID string) (codex.ThreadGoal, bool, error) {
	goal, ok := f.goals[threadID]
	return goal, ok, nil
}

func (f *fakeThreadClient) ClearThreadGoal(_ context.Context, threadID string) error {
	delete(f.goals, threadID)
	return nil
}

func (f *fakeThreadClient) SteerThread(_ context.Context, threadID string, request codex.ChatRequest) error {
	f.steered = append(f.steered, threadID+":"+request.Text)
	return nil
}

func (f *fakeThreadClient) ReviewThread(_ context.Context, threadID, _ string, target codex.ReviewTarget, _ codex.TurnPhaseHandler) (string, error) {
	return threadID + ":" + target.Type, nil
}

func newTestManager(path string) (*Manager, error) {
	return NewManager(path, func(string) Workspace {
		return Workspace{ID: DefaultProjectID, Name: "Workspace", Root: "/workspace"}
	})
}

var _ codex.AdvancedThreadClient = (*fakeThreadClient)(nil)

func TestManagerPersistsSelectionAcrossRestart(t *testing.T) {
	path := t.TempDir() + "/session-index.json"
	client := newFakeThreadClient()
	manager, err := newTestManager(path)
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

	restarted, err := newTestManager(path)
	if err != nil {
		t.Fatal(err)
	}
	current, err := restarted.Current(context.Background(), "owner-1", client)
	if err != nil || current.Info.ID != first.ID || current.Workspace.Name != "Workspace" || current.Workspace.Root != "/workspace" {
		t.Fatalf("Current() after restart = %#v, %v", current, err)
	}
	if _, err := restarted.Use(context.Background(), "owner-2", client, ShortCode(first.ID)); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("foreign Use() error = %v, want ErrNotOwned", err)
	}
}

func TestManagerDetailEnforcesOwnershipAndArchiveState(t *testing.T) {
	manager, err := newTestManager(t.TempDir() + "/session-index.json")
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
	manager, err := newTestManager(t.TempDir() + "/session-index.json")
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
	manager, err := newTestManager(t.TempDir() + "/session-index.json")
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
	manager, err := newTestManager(t.TempDir() + "/session-index.json")
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

func TestManagerForkInheritsSettingsAndDeletesDescendants(t *testing.T) {
	path := t.TempDir() + "/session-index.json"
	manager, err := newTestManager(path)
	if err != nil {
		t.Fatal(err)
	}
	client := newFakeThreadClient()
	root, err := manager.New(context.Background(), "owner-1", client, "主线程")
	if err != nil {
		t.Fatal(err)
	}
	settings := ThreadSettings{Model: "gpt-test", Effort: "high"}
	if err := manager.SetCurrentSettings("owner-1", settings); err != nil {
		t.Fatal(err)
	}
	child, err := manager.ForkCurrent(context.Background(), "owner-1", client, client)
	if err != nil || child.ForkedFromID != root.ID || child.Name != "主线程 · 分支" {
		t.Fatalf("ForkCurrent() = %#v, %v", child, err)
	}
	if got, err := manager.CurrentSettings("owner-1"); err != nil || got != settings {
		t.Fatalf("CurrentSettings() = %#v, %v", got, err)
	}
	grandchild, err := manager.ForkCurrent(context.Background(), "owner-1", client, client)
	if err != nil || grandchild.ForkedFromID != child.ID {
		t.Fatalf("second ForkCurrent() = %#v, %v", grandchild, err)
	}
	if _, err := manager.Use(context.Background(), "owner-1", client, root.ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.DeleteCurrent(context.Background(), "owner-1", client); err != nil {
		t.Fatal(err)
	}
	if stats := manager.Stats("owner-1"); stats.Active != 0 || stats.HasCurrent {
		t.Fatalf("Stats() after recursive delete = %#v", stats)
	}
	if len(client.threads) != 0 {
		t.Fatalf("remote descendants were not deleted: %#v", client.threads)
	}

	restarted, err := newTestManager(path)
	if err != nil {
		t.Fatal(err)
	}
	if stats := restarted.Stats("owner-1"); stats.Active != 0 || stats.HasCurrent {
		t.Fatalf("restarted Stats() = %#v", stats)
	}
}

func TestManagerNativeGoalSteerReviewAndPin(t *testing.T) {
	manager, err := newTestManager(t.TempDir() + "/session-index.json")
	if err != nil {
		t.Fatal(err)
	}
	client := newFakeThreadClient()
	thread, err := manager.New(context.Background(), "owner-1", client, "高级控制")
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := manager.PinCurrent(context.Background(), "owner-1", client, true)
	if err != nil || !pinned.IsPinned {
		t.Fatalf("PinCurrent() = %#v, %v", pinned, err)
	}
	if err := manager.CompactCurrent(context.Background(), "owner-1", client); err != nil || !reflect.DeepEqual(client.compacted, []string{thread.ID}) {
		t.Fatalf("CompactCurrent() compacted=%#v err=%v", client.compacted, err)
	}
	goal, err := manager.SetCurrentGoal(context.Background(), "owner-1", client, "完成高级控制")
	if err != nil || goal.Objective != "完成高级控制" {
		t.Fatalf("SetCurrentGoal() = %#v, %v", goal, err)
	}
	if read, exists, err := manager.CurrentGoal(context.Background(), "owner-1", client); err != nil || !exists || read.ThreadID != thread.ID {
		t.Fatalf("CurrentGoal() = %#v, %v, %v", read, exists, err)
	}
	if err := manager.SteerCurrent(context.Background(), "owner-1", client, codex.ChatRequest{Text: "先跑测试"}); err != nil {
		t.Fatal(err)
	}
	if review, err := manager.ReviewCurrent(context.Background(), "owner-1", client, codex.ReviewTarget{Type: "uncommittedChanges"}, nil); err != nil || review != thread.ID+":uncommittedChanges" {
		t.Fatalf("ReviewCurrent() = %q, %v", review, err)
	}
	if err := manager.ClearCurrentGoal(context.Background(), "owner-1", client); err != nil {
		t.Fatal(err)
	}
}
