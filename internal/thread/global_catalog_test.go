package thread

import (
	"context"
	"fmt"
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
	current, exists := manager.store.ActiveForProject("owner-1", "workspace")
	if !exists || current != threadID {
		t.Fatalf("workspace focus = %q, %v", current, exists)
	}
	if len(client.resumed) != 1 || client.resumed[0] != threadID {
		t.Fatalf("resumed = %#v", client.resumed)
	}
}

func TestCurrentRelationsUsesNativeOneLevelTopologyAndTrustedWorkspaces(t *testing.T) {
	manager, err := newTestManager(filepath.Join(t.TempDir(), "session-index.json"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	outside := t.TempDir()
	const parentID = "019fcc03-fc8b-7842-a812-000000000101"
	const currentID = "019fcc03-fc8b-7842-a812-000000000102"
	client := newFakeThreadClient()
	client.threads[parentID] = codex.ThreadInfo{ID: parentID, Name: "父线程", Cwd: root, UpdatedAt: 10, Status: codex.ThreadStatus{Type: "idle"}}
	client.threads[currentID] = codex.ThreadInfo{ID: currentID, Name: "当前分支", ForkedFromID: parentID, Cwd: root, UpdatedAt: 20, Status: codex.ThreadStatus{Type: "idle"}}
	for index := 0; index < 7; index++ {
		id := fmt.Sprintf("019fcc03-fc8b-7842-a812-%012d", 200+index)
		client.threads[id] = codex.ThreadInfo{
			ID: id, Name: fmt.Sprintf("直接子线程 %d", index+1), ForkedFromID: currentID,
			Cwd: root, UpdatedAt: int64(100 + index), Status: codex.ThreadStatus{Type: "idle"},
		}
	}
	client.threads["019fcc03-fc8b-7842-a812-000000000300"] = codex.ThreadInfo{
		ID: "019fcc03-fc8b-7842-a812-000000000300", Name: "越界子线程", ForkedFromID: currentID,
		Cwd: outside, UpdatedAt: 999, Status: codex.ThreadStatus{Type: "active"},
	}
	workspace := Workspace{ID: "workspace", Name: "Workspace", Root: root}
	manager.resolveWorkspace = func(string) Workspace { return workspace }
	if _, err := manager.UseGlobalThread(context.Background(), "owner-1", workspace, currentID, client); err != nil {
		t.Fatal(err)
	}

	relations, err := manager.CurrentRelations(context.Background(), "owner-1", client, []Workspace{workspace}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if relations.Current.ID != currentID || relations.Parent == nil || relations.Parent.ID != parentID || relations.ParentUnavailable {
		t.Fatalf("relation anchors = %#v", relations)
	}
	if len(relations.Children) != 5 || relations.Truncated != 2 {
		t.Fatalf("relation children = %d truncated=%d", len(relations.Children), relations.Truncated)
	}
	if relations.Children[0].Title != "直接子线程 1" || relations.Children[4].Title != "直接子线程 5" {
		t.Fatalf("relation order = %#v", relations.Children)
	}
	for _, child := range relations.Children {
		if child.WorkspaceID != "workspace" {
			t.Fatalf("untrusted relation leaked: %#v", child)
		}
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
