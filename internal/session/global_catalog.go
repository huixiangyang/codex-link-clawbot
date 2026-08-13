package session

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/codex"
)

const globalThreadScanPageSize = 100

type Workspace struct {
	ID   string
	Name string
	Root string
}

type GlobalThread struct {
	Info          codex.ThreadInfo
	WorkspaceID   string
	WorkspaceName string
	Current       bool
	Archived      bool
}

type GlobalPage struct {
	Items      []GlobalThread
	Number     int
	TotalPages int
	Total      int
	Running    int
	Loaded     int
}

// GlobalList 直接读取 Codex 全局线程目录；本地索引只标记操作焦点，不参与可见性判断。
func (m *Manager) GlobalList(ctx context.Context, ownerID string, client codex.ThreadClient, workspaces []Workspace, archived, runningOnly bool, query string, pageNumber, pageSize int) (GlobalPage, error) {
	if pageSize <= 0 {
		pageSize = DefaultPageSize
	}
	if pageNumber <= 0 {
		pageNumber = 1
	}
	resolved, err := resolveWorkspaces(workspaces)
	if err != nil {
		return GlobalPage{}, err
	}
	items := make([]GlobalThread, 0, globalThreadScanPageSize)
	currentProjectID := m.currentProject(ownerID)
	cursor := ""
	seenCursors := make(map[string]bool)
	for scannedPages := 0; scannedPages < 100; scannedPages++ {
		page, listErr := client.ListThreads(ctx, codex.ThreadListOptions{
			Cursor: cursor, Limit: globalThreadScanPageSize, Archived: archived,
			SearchTerm: strings.TrimSpace(query),
		})
		if listErr != nil {
			return GlobalPage{}, fmt.Errorf("list global codex threads: %w", listErr)
		}
		for _, thread := range page.Threads {
			if runningOnly && thread.Status.Type != "active" {
				continue
			}
			workspace, allowed := matchWorkspace(thread.Cwd, resolved)
			if !allowed {
				continue
			}
			currentID, _ := m.store.ActiveForProject(ownerID, workspace.ID)
			items = append(items, GlobalThread{
				Info: thread, WorkspaceID: workspace.ID, WorkspaceName: workspace.Name,
				Current: workspace.ID == currentProjectID && currentID == thread.ID, Archived: archived,
			})
		}
		if page.NextCursor == "" || seenCursors[page.NextCursor] {
			break
		}
		seenCursors[page.NextCursor] = true
		cursor = page.NextCursor
	}
	sort.SliceStable(items, func(i, j int) bool {
		return recency(items[i].Info) > recency(items[j].Info)
	})
	result := GlobalPage{Total: len(items)}
	for _, item := range items {
		if item.Info.Status.Type == "active" {
			result.Running++
		}
		if item.Info.Status.Type != "notLoaded" {
			result.Loaded++
		}
	}
	result.TotalPages = (len(items) + pageSize - 1) / pageSize
	if result.TotalPages == 0 {
		result.TotalPages = 1
	}
	if pageNumber > result.TotalPages {
		return GlobalPage{}, fmt.Errorf("page %d exceeds total pages %d", pageNumber, result.TotalPages)
	}
	start := (pageNumber - 1) * pageSize
	end := start + pageSize
	if end > len(items) {
		end = len(items)
	}
	result.Number = pageNumber
	result.Items = items[start:end]
	return result, nil
}

// UseGlobalThread 把任意受信任工作空间中的 Codex 线程设为远程操作焦点。
func (m *Manager) UseGlobalThread(ctx context.Context, ownerID string, workspace Workspace, threadID string, client codex.ThreadClient) (codex.ThreadInfo, error) {
	ownerID = strings.TrimSpace(ownerID)
	projectID := strings.TrimSpace(workspace.ID)
	threadID = strings.TrimSpace(threadID)
	if ownerID == "" || projectID == "" || threadID == "" {
		return codex.ThreadInfo{}, fmt.Errorf("owner, workspace and thread are required")
	}
	thread, err := client.ResumeThread(ctx, threadID)
	if err != nil {
		return codex.ThreadInfo{}, fmt.Errorf("resume global thread: %w", err)
	}
	resolved, err := resolveWorkspaces([]Workspace{workspace})
	if err != nil {
		_ = client.UnsubscribeThread(context.WithoutCancel(ctx), threadID)
		return codex.ThreadInfo{}, err
	}
	if matched, allowed := matchWorkspace(thread.Cwd, resolved); !allowed || matched.ID != projectID {
		_ = client.UnsubscribeThread(context.WithoutCancel(ctx), threadID)
		return codex.ThreadInfo{}, fmt.Errorf("resumed thread is outside the trusted workspace")
	}
	oldThreadID, _ := m.store.ActiveForProject(ownerID, projectID)
	if err := m.store.RegisterProject(ownerID, projectID, thread, true, m.now()); err != nil {
		_ = client.UnsubscribeThread(context.WithoutCancel(ctx), threadID)
		return codex.ThreadInfo{}, fmt.Errorf("persist global thread focus: %w", err)
	}
	if oldThreadID != "" && oldThreadID != threadID {
		_ = client.UnsubscribeThread(context.WithoutCancel(ctx), oldThreadID)
	}
	return thread, nil
}

type resolvedWorkspace struct {
	Workspace
	CanonicalRoot string
}

func resolveWorkspaces(workspaces []Workspace) ([]resolvedWorkspace, error) {
	if len(workspaces) == 0 {
		return nil, fmt.Errorf("at least one workspace is required")
	}
	resolved := make([]resolvedWorkspace, 0, len(workspaces))
	seen := make(map[string]bool, len(workspaces))
	for _, workspace := range workspaces {
		workspace.ID = strings.TrimSpace(workspace.ID)
		workspace.Name = strings.TrimSpace(workspace.Name)
		workspace.Root = filepath.Clean(strings.TrimSpace(workspace.Root))
		if workspace.ID == "" || workspace.Name == "" || !filepath.IsAbs(workspace.Root) || seen[workspace.ID] {
			return nil, fmt.Errorf("invalid workspace definition")
		}
		canonical, err := filepath.EvalSymlinks(workspace.Root)
		if err != nil {
			return nil, fmt.Errorf("resolve workspace %s: %w", workspace.ID, err)
		}
		seen[workspace.ID] = true
		resolved = append(resolved, resolvedWorkspace{Workspace: workspace, CanonicalRoot: filepath.Clean(canonical)})
	}
	// 嵌套工作空间优先匹配更具体的根目录。
	sort.SliceStable(resolved, func(i, j int) bool {
		return len(resolved[i].CanonicalRoot) > len(resolved[j].CanonicalRoot)
	})
	return resolved, nil
}

func matchWorkspace(cwd string, workspaces []resolvedWorkspace) (resolvedWorkspace, bool) {
	cwd = filepath.Clean(strings.TrimSpace(cwd))
	if !filepath.IsAbs(cwd) {
		return resolvedWorkspace{}, false
	}
	canonical, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return resolvedWorkspace{}, false
	}
	canonical = filepath.Clean(canonical)
	for _, workspace := range workspaces {
		relative, relErr := filepath.Rel(workspace.CanonicalRoot, canonical)
		if relErr == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative) {
			return workspace, true
		}
	}
	return resolvedWorkspace{}, false
}

func (item GlobalThread) DisplayLabel(now time.Time) string {
	name := strings.TrimSpace(item.Info.Name)
	if name == "" {
		name = strings.TrimSpace(item.Info.Preview)
	}
	if name == "" {
		name = ShortCode(item.Info.ID)
	}
	return fmt.Sprintf("%s · %s · %s", name, item.WorkspaceName, relativeThreadTime(recency(item.Info), now))
}

// ActivityLabel 返回线程最近活动的移动端友好时间，不暴露原始时间戳。
func (item GlobalThread) ActivityLabel(now time.Time) string {
	return relativeThreadTime(recency(item.Info), now)
}

func relativeThreadTime(timestamp int64, now time.Time) string {
	if timestamp <= 0 {
		return "时间未知"
	}
	delta := now.Sub(time.Unix(timestamp, 0))
	if delta < time.Minute {
		return "刚刚"
	}
	if delta < time.Hour {
		return fmt.Sprintf("%d 分钟前", int(delta.Minutes()))
	}
	if delta < 24*time.Hour {
		return fmt.Sprintf("%d 小时前", int(delta.Hours()))
	}
	return fmt.Sprintf("%d 天前", int(delta.Hours()/24))
}
