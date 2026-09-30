package thread

import (
	"context"

	"os"
	"path/filepath"
	"testing"

	"github.com/huixiangyang/codex-link-clawbot/internal/codex"
)

type pagedGlobalThreadClient struct {
	*fakeThreadClient
	pages map[string]codex.ThreadPage
}

func (client *pagedGlobalThreadClient) ListThreads(_ context.Context, options codex.ThreadListOptions) (codex.ThreadPage, error) {
	return client.pages[options.Cursor], nil
}

func TestGlobalListUsesCodexAsVisibilitySourceAndFiltersWorkspaces(t *testing.T) {
	manager, err := newTestManager(filepath.Join(t.TempDir(), "session-index.json"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	client := newFakeThreadClient()
	client.threads["019fcc03-fc8b-7842-a812-000000000001"] = codex.ThreadInfo{
		ID: "019fcc03-fc8b-7842-a812-000000000001", Name: "桌面端线程", Cwd: root, UpdatedAt: 30,
		Status: codex.ThreadStatus{Type: "active"},
	}
	client.threads["019fcc03-fc8b-7842-a812-000000000002"] = codex.ThreadInfo{
		ID: "019fcc03-fc8b-7842-a812-000000000002", Name: "嵌套线程", Cwd: nested, UpdatedAt: 20,
		Status: codex.ThreadStatus{Type: "idle"},
	}
	client.threads["019fcc03-fc8b-7842-a812-000000000003"] = codex.ThreadInfo{
		ID: "019fcc03-fc8b-7842-a812-000000000003", Name: "越界线程", Cwd: outside, UpdatedAt: 40,
		Status: codex.ThreadStatus{Type: "idle"},
	}
	workspaces := []Workspace{
		{ID: "root", Name: "根工作空间", Root: root},
		{ID: "nested", Name: "嵌套工作空间", Root: nested},
	}
	page, err := manager.GlobalList(context.Background(), "owner-1", client, workspaces, false, false, "", 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || page.Running != 1 || len(page.Items) != 2 {
		t.Fatalf("GlobalList() = %#v", page)
	}
	if page.Items[0].WorkspaceID != "root" || page.Items[1].WorkspaceID != "nested" {
		t.Fatalf("workspace matching = %#v", page.Items)
	}
	if _, err := manager.GlobalList(context.Background(), "owner-1", client, workspaces, false, false, "", 2, 1); err != nil {
		t.Fatal(err)
	}
	// 同一目录翻页复用短缓存，不再对每页全量调用上游。
	if len(client.listOptions) != 1 || len(client.listOptions[0].SourceKinds) != 0 {
		t.Fatalf("global list must omit sourceKinds: %#v", client.listOptions)
	}
}

func TestUseGlobalThreadAdoptsExternalThreadAsWorkspaceFocus(t *testing.T) {
	manager, err := newTestManager(filepath.Join(t.TempDir(), "session-index.json"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	const threadID = "019fcc03-fc8b-7842-a812-000000000004"
	client := newFakeThreadClient()
	client.threads[threadID] = codex.ThreadInfo{ID: threadID, Name: "CLI 线程", Cwd: root, Status: codex.ThreadStatus{Type: "idle"}}

	selected, err := manager.UseGlobalThread(context.Background(), "owner-1", Workspace{ID: "workspace", Name: "Workspace", Root: root}, threadID, client)
	if err != nil || selected.ID != threadID {
		t.Fatalf("UseGlobalThread() = %#v, %v", selected, err)
	}
	if len(client.resumed) != 0 {
		t.Fatalf("resumed = %#v", client.resumed)
	}
}

func TestGlobalListDeduplicatesRepeatedThreadsAcrossPages(t *testing.T) {
	manager, err := newTestManager(filepath.Join(t.TempDir(), "session-index.json"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	first := codex.ThreadInfo{ID: "019fcc03-fc8b-7842-a812-000000000401", Name: "第一页", Cwd: root, UpdatedAt: 10, Status: codex.ThreadStatus{Type: "idle"}}
	second := codex.ThreadInfo{ID: "019fcc03-fc8b-7842-a812-000000000402", Name: "第二页", Cwd: root, UpdatedAt: 20, Status: codex.ThreadStatus{Type: "active"}}
	client := &pagedGlobalThreadClient{
		fakeThreadClient: newFakeThreadClient(),
		pages: map[string]codex.ThreadPage{
			"":       {Threads: []codex.ThreadInfo{first}, NextCursor: "page-2"},
			"page-2": {Threads: []codex.ThreadInfo{first, second}},
		},
	}
	page, err := manager.GlobalList(context.Background(), "owner-1", client, []Workspace{{ID: "workspace", Name: "Workspace", Root: root}}, false, false, "", 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || page.Running != 1 || len(page.Items) != 2 || page.Items[0].Info.ID != second.ID || page.Items[1].Info.ID != first.ID {
		t.Fatalf("deduplicated global page = %#v", page)
	}
}
