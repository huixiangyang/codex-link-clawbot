package thread

import (
	"context"
	"fmt"
	"sync"

	"strings"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/codex"
)

const DefaultPageSize = 12
const MaxSessionName = 80

type Manager struct {
	mu      sync.Mutex
	catalog map[string]catalogEntry
}
type catalogEntry struct {
	items   []codex.ThreadInfo
	expires time.Time
}

func NewManager() (*Manager, error) { return &Manager{catalog: map[string]catalogEntry{}}, nil }

// Invalidate 由会话变更入口调用；执行准入仍查询新鲜目录，避免恢复已归档线程。
func (m *Manager) Invalidate() { m.mu.Lock(); m.catalog = map[string]catalogEntry{}; m.mu.Unlock() }
func (m *Manager) directory(ctx context.Context, client codex.ThreadClient, archived bool, query string) ([]codex.ThreadInfo, error) {
	key := fmt.Sprintf("%t:%s", archived, query)
	m.mu.Lock()
	entry, ok := m.catalog[key]
	m.mu.Unlock()
	if ok && time.Now().Before(entry.expires) {
		return append([]codex.ThreadInfo(nil), entry.items...), nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	items, err := scanGlobalThreads(ctx, client, archived, query)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	if len(m.catalog) >= 64 {
		m.catalog = map[string]catalogEntry{}
	}
	m.catalog[key] = catalogEntry{items: append([]codex.ThreadInfo(nil), items...), expires: time.Now().Add(3 * time.Second)}
	m.mu.Unlock()
	return items, nil
}

// OpenTaskThread 按任务快照打开会话，不读取也不改写执行时的界面当前项目。
func (m *Manager) OpenTaskThread(ctx context.Context, ownerID string, workspace Workspace, threadID string, client codex.ThreadClient, suggestedName string) (codex.ThreadInfo, error) {
	ownerID = strings.TrimSpace(ownerID)
	projectID := strings.TrimSpace(workspace.ID)
	workspaceRoot := strings.TrimSpace(workspace.Root)
	threadID = strings.TrimSpace(threadID)
	if ownerID == "" || projectID == "" || workspaceRoot == "" {
		return codex.ThreadInfo{}, fmt.Errorf("task owner and workspace are required")
	}
	if threadID != "" {
		info, err := ReadAvailableThread(ctx, client, threadID)
		if err != nil {
			return codex.ThreadInfo{}, err
		}
		resolved, err := resolveWorkspaces([]Workspace{workspace})
		if err != nil {
			return codex.ThreadInfo{}, err
		}
		if _, ok := matchWorkspace(info.Cwd, resolved); !ok {
			return codex.ThreadInfo{}, fmt.Errorf("会话不在受信任工作空间")
		}
		if info.Status.Type == "active" {
			return codex.ThreadInfo{}, codex.ErrThreadBusy
		}
		thread, err := client.ResumeThread(ctx, threadID, workspaceRoot)
		if err != nil {
			return codex.ThreadInfo{}, fmt.Errorf("resume task session %s: %w", ShortCode(threadID), err)
		}
		return thread, nil
	}

	name, err := normalizeName(suggestedName)
	if err != nil {
		return codex.ThreadInfo{}, err
	}
	thread, err := client.StartThread(ctx, workspaceRoot)
	if err != nil {
		return codex.ThreadInfo{}, fmt.Errorf("start task session: %w", err)
	}
	if name != "" {
		if err := client.SetThreadName(ctx, thread.ID, name); err != nil {
			_ = client.ArchiveThread(context.WithoutCancel(ctx), thread.ID)
			return codex.ThreadInfo{}, fmt.Errorf("name task session: %w", err)
		}
		thread.Name = name
	}
	m.Invalidate()
	return thread, nil
}

func normalizeName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if strings.ContainsAny(name, "\r\n") {
		return "", fmt.Errorf("session name must be a single line")
	}
	if len([]rune(name)) > MaxSessionName {
		return "", fmt.Errorf("session name exceeds %d characters", MaxSessionName)
	}
	return name, nil
}

func recency(thread codex.ThreadInfo) int64 {
	if thread.RecencyAt != nil {
		return *thread.RecencyAt
	}
	return thread.UpdatedAt
}

func ShortCode(threadID string) string {
	const length = 8
	if len(threadID) <= length {
		return threadID
	}
	return threadID[len(threadID)-length:]
}
