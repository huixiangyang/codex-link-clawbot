package thread

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
	threads, err := m.directory(ctx, client, archived, strings.TrimSpace(query))
	if err != nil {
		return GlobalPage{}, err
	}
	items := make([]GlobalThread, 0, len(threads))
	for _, thread := range threads {
		if runningOnly && thread.Status.Type != "active" {
			continue
		}
		workspace, allowed := matchWorkspace(thread.Cwd, resolved)
		if !allowed {
			continue
		}
		items = append(items, GlobalThread{
			Info: thread, WorkspaceID: workspace.ID, WorkspaceName: workspace.Name,
			Archived: archived,
		})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if (items[i].Info.Status.Type == "active") != (items[j].Info.Status.Type == "active") {
			return items[i].Info.Status.Type == "active"
		}
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

func scanGlobalThreads(ctx context.Context, client codex.ThreadClient, archived bool, query string) ([]codex.ThreadInfo, error) {
	threads := make([]codex.ThreadInfo, 0, globalThreadScanPageSize)
	cursor := ""
	seenCursors := make(map[string]bool)
	seenThreads := make(map[string]bool)
	for scannedPages := 0; scannedPages < 100; scannedPages++ {
		page, err := client.ListThreads(ctx, codex.ThreadListOptions{
			Cursor: cursor, Limit: globalThreadScanPageSize, Archived: archived, SearchTerm: query,
		})
		if err != nil {
			return nil, fmt.Errorf("list global codex threads: %w", err)
		}
		for _, thread := range page.Threads {
			if strings.TrimSpace(thread.ID) == "" || seenThreads[thread.ID] {
				continue
			}
			seenThreads[thread.ID] = true
			threads = append(threads, thread)
		}
		if page.NextCursor == "" {
			return threads, nil
		}
		if seenCursors[page.NextCursor] {
			return nil, fmt.Errorf("thread cursor did not advance")
		}
		seenCursors[page.NextCursor] = true
		cursor = page.NextCursor
	}
	return nil, fmt.Errorf("thread catalog exceeded the scan limit; narrow the search")
}

// ReadAvailableThread 确认线程仍在未归档目录，避免 Resume 隐式恢复已归档会话。
func ReadAvailableThread(ctx context.Context, client codex.ThreadClient, threadID string) (codex.ThreadInfo, error) {
	threads, err := scanGlobalThreads(ctx, client, false, "")
	if err != nil {
		return codex.ThreadInfo{}, err
	}
	for _, item := range threads {
		if item.ID == threadID {
			return item, nil
		}
	}
	return codex.ThreadInfo{}, fmt.Errorf("thread is archived or unavailable")
}

// UseGlobalThread 把任意受信任工作空间中的 Codex 线程设为远程操作焦点。
func (m *Manager) UseGlobalThread(ctx context.Context, ownerID string, workspace Workspace, threadID string, client codex.ThreadClient) (codex.ThreadInfo, error) {
	ownerID = strings.TrimSpace(ownerID)
	projectID := strings.TrimSpace(workspace.ID)
	threadID = strings.TrimSpace(threadID)
	if ownerID == "" || projectID == "" || threadID == "" {
		return codex.ThreadInfo{}, fmt.Errorf("owner, workspace and thread are required")
	}
	// 切换只改变输入目标；不取消订阅旧会话，也不重载正在执行的会话。
	thread, err := ReadAvailableThread(ctx, client, threadID)
	if err != nil {
		return codex.ThreadInfo{}, err
	}
	resolved, err := resolveWorkspaces([]Workspace{workspace})
	if err != nil {
		return codex.ThreadInfo{}, err
	}
	if matched, allowed := matchWorkspace(thread.Cwd, resolved); !allowed || matched.ID != projectID {
		return codex.ThreadInfo{}, fmt.Errorf("thread is outside the trusted workspace")
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
