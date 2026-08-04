package session

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/huixiangyang/weclaw/codex"
)

const (
	DefaultPageSize = 6
	MaxSessionName  = 80
)

type ManagedThread struct {
	Info        codex.ThreadInfo
	Current     bool
	Archived    bool
	Unavailable bool
}

type Page struct {
	Items      []ManagedThread
	Number     int
	TotalPages int
	Total      int
}

// Stats 是本地所有权索引的轻量管理概览，不读取或暴露 Codex 全局线程。
type Stats struct {
	Active     int
	Archived   int
	HasCurrent bool
	CurrentID  string
}

// Manager 把微信用户归属与 Codex 线程生命周期组合成一个事务边界。
type Manager struct {
	store *Store
	now   func() time.Time
}

func NewManager(path string) (*Manager, error) {
	store, err := OpenStore(path)
	if err != nil {
		return nil, err
	}
	return &Manager{store: store, now: time.Now}, nil
}

func (m *Manager) EnsureActive(ctx context.Context, ownerID string, client codex.ThreadClient, suggestedName string) (codex.ThreadInfo, error) {
	suggestedName, err := normalizeName(suggestedName)
	if err != nil {
		return codex.ThreadInfo{}, err
	}
	if threadID, ok := m.store.Active(ownerID); ok {
		thread, readErr := client.ReadThread(ctx, threadID)
		if readErr != nil {
			return codex.ThreadInfo{}, fmt.Errorf("read active session %s: %w", ShortCode(threadID), readErr)
		}
		// 对历史未命名会话做一次尽力命名；命名失败不能阻断用户的正常 turn。
		if strings.TrimSpace(thread.Name) == "" && suggestedName != "" {
			if nameErr := client.SetThreadName(ctx, threadID, suggestedName); nameErr == nil {
				thread.Name = suggestedName
			}
		}
		return thread, nil
	}
	return m.New(ctx, ownerID, client, suggestedName)
}

func (m *Manager) Current(ctx context.Context, ownerID string, client codex.ThreadClient) (ManagedThread, error) {
	threadID, ok := m.store.Active(ownerID)
	if !ok {
		return ManagedThread{}, ErrNoActive
	}
	thread, err := client.ReadThread(ctx, threadID)
	if err != nil {
		return ManagedThread{}, fmt.Errorf("read current session: %w", err)
	}
	return ManagedThread{Info: thread, Current: true}, nil
}

// Detail 只允许读取本地索引中归属于该微信用户的会话摘要。
func (m *Manager) Detail(ctx context.Context, ownerID string, client codex.ThreadClient, reference string, archived bool) (ManagedThread, error) {
	record, err := m.store.Resolve(ownerID, reference, archived)
	if err != nil {
		return ManagedThread{}, err
	}
	thread, err := client.ReadThread(ctx, record.ID)
	if err != nil {
		return ManagedThread{}, fmt.Errorf("read session detail: %w", err)
	}
	activeID, _ := m.store.Active(ownerID)
	return ManagedThread{
		Info: thread, Current: activeID == record.ID, Archived: archived,
	}, nil
}

func (m *Manager) Stats(ownerID string) Stats {
	active, archived, currentID, hasCurrent := m.store.Counts(ownerID)
	return Stats{
		Active:     active,
		Archived:   archived,
		HasCurrent: hasCurrent,
		CurrentID:  currentID,
	}
}

func (m *Manager) New(ctx context.Context, ownerID string, client codex.ThreadClient, name string) (codex.ThreadInfo, error) {
	name, err := normalizeName(name)
	if err != nil {
		return codex.ThreadInfo{}, err
	}
	thread, err := client.StartThread(ctx)
	if err != nil {
		return codex.ThreadInfo{}, fmt.Errorf("start session: %w", err)
	}
	if name != "" {
		if err := client.SetThreadName(ctx, thread.ID, name); err != nil {
			_ = client.ArchiveThread(context.WithoutCancel(ctx), thread.ID)
			return codex.ThreadInfo{}, fmt.Errorf("name new session: %w", err)
		}
		thread.Name = name
	}
	oldThreadID, hadOld := m.store.Active(ownerID)
	if err := m.store.Register(ownerID, thread, true, m.now()); err != nil {
		_ = client.ArchiveThread(context.WithoutCancel(ctx), thread.ID)
		return codex.ThreadInfo{}, fmt.Errorf("persist new session: %w", err)
	}
	if hadOld && oldThreadID != thread.ID {
		_ = client.UnsubscribeThread(context.WithoutCancel(ctx), oldThreadID)
	}
	return thread, nil
}

func (m *Manager) Use(ctx context.Context, ownerID string, client codex.ThreadClient, reference string) (codex.ThreadInfo, error) {
	record, err := m.store.Resolve(ownerID, reference, false)
	if err != nil {
		return codex.ThreadInfo{}, err
	}
	oldThreadID, _ := m.store.Active(ownerID)
	if oldThreadID == record.ID {
		return client.ReadThread(ctx, record.ID)
	}
	thread, err := client.ResumeThread(ctx, record.ID)
	if err != nil {
		return codex.ThreadInfo{}, fmt.Errorf("resume session: %w", err)
	}
	if err := m.store.SetActive(ownerID, record.ID, m.now()); err != nil {
		_ = client.UnsubscribeThread(context.WithoutCancel(ctx), record.ID)
		return codex.ThreadInfo{}, fmt.Errorf("persist selected session: %w", err)
	}
	if oldThreadID != "" {
		_ = client.UnsubscribeThread(context.WithoutCancel(ctx), oldThreadID)
	}
	return thread, nil
}

func (m *Manager) Rename(ctx context.Context, ownerID string, client codex.ThreadClient, name string) (codex.ThreadInfo, error) {
	name, err := normalizeName(name)
	if err != nil || name == "" {
		if err == nil {
			err = fmt.Errorf("session name is required")
		}
		return codex.ThreadInfo{}, err
	}
	threadID, ok := m.store.Active(ownerID)
	if !ok {
		return codex.ThreadInfo{}, ErrNoActive
	}
	thread, err := client.ReadThread(ctx, threadID)
	if err != nil {
		return codex.ThreadInfo{}, fmt.Errorf("read session before rename: %w", err)
	}
	if err := client.SetThreadName(ctx, threadID, name); err != nil {
		return codex.ThreadInfo{}, fmt.Errorf("rename session: %w", err)
	}
	thread.Name = name
	return thread, nil
}

func (m *Manager) Archive(ctx context.Context, ownerID string, client codex.ThreadClient, reference string) (string, error) {
	if strings.TrimSpace(reference) == "" {
		var ok bool
		reference, ok = m.store.Active(ownerID)
		if !ok {
			return "", ErrNoActive
		}
	}
	record, err := m.store.Resolve(ownerID, reference, false)
	if err != nil {
		return "", err
	}
	activeID, _ := m.store.Active(ownerID)
	nextActive := ""
	if activeID == record.ID {
		for _, candidate := range m.store.Records(ownerID, false) {
			if candidate.ID == record.ID {
				continue
			}
			if _, readErr := client.ResumeThread(ctx, candidate.ID); readErr == nil {
				nextActive = candidate.ID
				break
			}
		}
	}
	if err := client.ArchiveThread(ctx, record.ID); err != nil {
		if nextActive != "" {
			_ = client.UnsubscribeThread(context.WithoutCancel(ctx), nextActive)
		}
		return "", fmt.Errorf("archive session: %w", err)
	}
	if err := m.store.MarkArchived(ownerID, record.ID, nextActive, true, m.now()); err != nil {
		_, _ = client.UnarchiveThread(context.WithoutCancel(ctx), record.ID)
		if nextActive != "" {
			_ = client.UnsubscribeThread(context.WithoutCancel(ctx), nextActive)
		}
		return "", fmt.Errorf("persist archived session: %w", err)
	}
	return nextActive, nil
}

func (m *Manager) Restore(ctx context.Context, ownerID string, client codex.ThreadClient, reference string) (codex.ThreadInfo, error) {
	record, err := m.store.Resolve(ownerID, reference, true)
	if err != nil {
		return codex.ThreadInfo{}, err
	}
	thread, err := client.UnarchiveThread(ctx, record.ID)
	if err != nil {
		return codex.ThreadInfo{}, fmt.Errorf("restore session: %w", err)
	}
	_, hasActive := m.store.Active(ownerID)
	nextActive := ""
	if !hasActive {
		nextActive = record.ID
	}
	if err := m.store.MarkArchived(ownerID, record.ID, nextActive, false, m.now()); err != nil {
		_ = client.ArchiveThread(context.WithoutCancel(ctx), record.ID)
		return codex.ThreadInfo{}, fmt.Errorf("persist restored session: %w", err)
	}
	return thread, nil
}

func (m *Manager) List(ctx context.Context, ownerID string, client codex.ThreadClient, archived bool, pageNumber, pageSize int) (Page, error) {
	if pageSize <= 0 {
		pageSize = DefaultPageSize
	}
	if pageNumber <= 0 {
		pageNumber = 1
	}
	records := m.store.Records(ownerID, archived)
	activeID, _ := m.store.Active(ownerID)
	owned := make(map[string]Record, len(records))
	for _, record := range records {
		owned[record.ID] = record
	}
	items := make([]ManagedThread, 0, len(records))
	cursor := ""
	seenCursors := make(map[string]bool)
	for len(owned) > 0 {
		page, err := client.ListThreads(ctx, codex.ThreadListOptions{
			Cursor: cursor, Limit: 100, Archived: archived,
			SourceKinds: allCodexThreadSources,
		})
		if err != nil {
			return Page{}, fmt.Errorf("list codex sessions: %w", err)
		}
		for _, thread := range page.Threads {
			if _, ok := owned[thread.ID]; !ok {
				continue
			}
			items = append(items, ManagedThread{
				Info: thread, Current: activeID == thread.ID, Archived: archived,
			})
			delete(owned, thread.ID)
		}
		if page.NextCursor == "" || seenCursors[page.NextCursor] {
			break
		}
		seenCursors[page.NextCursor] = true
		cursor = page.NextCursor
	}
	// Codex 历史被外部删除或损坏时仍展示归属记录，但明确标记无法读取。
	for _, record := range owned {
		items = append(items, ManagedThread{
			Info: codex.ThreadInfo{
				ID: record.ID, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
				Status: codex.ThreadStatus{Type: "systemError"},
			},
			Current: activeID == record.ID, Archived: archived, Unavailable: true,
		})
	}
	sort.SliceStable(items, func(i, j int) bool {
		return recency(items[i].Info) > recency(items[j].Info)
	})
	totalPages := (len(items) + pageSize - 1) / pageSize
	if totalPages == 0 {
		totalPages = 1
	}
	if pageNumber > totalPages {
		return Page{}, fmt.Errorf("page %d exceeds total pages %d", pageNumber, totalPages)
	}
	start := (pageNumber - 1) * pageSize
	end := start + pageSize
	if end > len(items) {
		end = len(items)
	}
	return Page{
		Items: items[start:end], Number: pageNumber,
		TotalPages: totalPages, Total: len(items),
	}, nil
}

var allCodexThreadSources = []string{
	"cli", "vscode", "exec", "appServer",
	"subAgent", "subAgentReview", "subAgentCompact",
	"subAgentThreadSpawn", "subAgentOther", "unknown",
}

func (m *Manager) Touch(ownerID, threadID string, updatedAt int64) error {
	return m.store.Touch(ownerID, threadID, updatedAt)
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
