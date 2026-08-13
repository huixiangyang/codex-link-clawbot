package session

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/codex"
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

// Stats 是本地焦点索引的轻量概览，不代表 Codex 全局线程可见范围。
type Stats struct {
	Active     int
	Archived   int
	HasCurrent bool
	CurrentID  string
}

// Manager 把每位微信用户的远程操作焦点与 Codex 线程生命周期组合成事务边界。
type Manager struct {
	store     *Store
	now       func() time.Time
	projectID func(ownerID string) string
}

func NewManager(path string) (*Manager, error) {
	store, err := OpenStore(path)
	if err != nil {
		return nil, err
	}
	return &Manager{
		store:     store,
		now:       time.Now,
		projectID: func(string) string { return DefaultProjectID },
	}, nil
}

// SetProjectResolver 让同一个微信用户在不同项目中拥有相互隔离的当前会话。
func (m *Manager) SetProjectResolver(resolver func(ownerID string) string) {
	if resolver == nil {
		m.projectID = func(string) string { return DefaultProjectID }
		return
	}
	m.projectID = resolver
}

func (m *Manager) currentProject(ownerID string) string {
	projectID := strings.TrimSpace(m.projectID(ownerID))
	if projectID == "" {
		return DefaultProjectID
	}
	return projectID
}

func (m *Manager) EnsureActive(ctx context.Context, ownerID string, client codex.ThreadClient, suggestedName string) (codex.ThreadInfo, error) {
	suggestedName, err := normalizeName(suggestedName)
	if err != nil {
		return codex.ThreadInfo{}, err
	}
	projectID := m.currentProject(ownerID)
	if threadID, ok := m.store.ActiveForProject(ownerID, projectID); ok {
		thread, readErr := client.ReadThread(ctx, threadID)
		if readErr != nil {
			return codex.ThreadInfo{}, fmt.Errorf("read active session %s: %w", ShortCode(threadID), readErr)
		}
		// 对历史未命名线程做一次尽力命名；命名失败不能阻断用户的正常轮次。
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
	threadID, ok := m.store.ActiveForProject(ownerID, m.currentProject(ownerID))
	if !ok {
		return ManagedThread{}, ErrNoActive
	}
	thread, err := client.ReadThread(ctx, threadID)
	if err != nil {
		return ManagedThread{}, fmt.Errorf("read current session: %w", err)
	}
	return ManagedThread{Info: thread, Current: true}, nil
}

// Detail 读取本地焦点索引中的线程摘要；全局发现统一走 GlobalList。
func (m *Manager) Detail(ctx context.Context, ownerID string, client codex.ThreadClient, reference string, archived bool) (ManagedThread, error) {
	projectID := m.currentProject(ownerID)
	record, err := m.store.ResolveForProject(ownerID, projectID, reference, archived)
	if err != nil {
		return ManagedThread{}, err
	}
	thread, err := client.ReadThread(ctx, record.ID)
	if err != nil {
		return ManagedThread{}, fmt.Errorf("read session detail: %w", err)
	}
	activeID, _ := m.store.ActiveForProject(ownerID, projectID)
	return ManagedThread{
		Info: thread, Current: activeID == record.ID, Archived: archived,
	}, nil
}

func (m *Manager) Stats(ownerID string) Stats {
	active, archived, currentID, hasCurrent := m.store.CountsForProject(ownerID, m.currentProject(ownerID))
	return Stats{
		Active:     active,
		Archived:   archived,
		HasCurrent: hasCurrent,
		CurrentID:  currentID,
	}
}

// SnapshotThreadID 只读取指定项目当前会话，用于任务入队时冻结归属。
func (m *Manager) SnapshotThreadID(ownerID, projectID string) string {
	threadID, _ := m.store.ActiveForProject(strings.TrimSpace(ownerID), strings.TrimSpace(projectID))
	return threadID
}

// OpenTaskThread 按任务快照打开会话，不读取也不改写执行时的界面当前项目。
func (m *Manager) OpenTaskThread(ctx context.Context, ownerID, projectID, threadID string, client codex.ThreadClient, suggestedName string) (codex.ThreadInfo, error) {
	ownerID = strings.TrimSpace(ownerID)
	projectID = strings.TrimSpace(projectID)
	threadID = strings.TrimSpace(threadID)
	if ownerID == "" || projectID == "" {
		return codex.ThreadInfo{}, fmt.Errorf("task owner and project are required")
	}
	if threadID != "" {
		if _, err := m.store.ResolveForProject(ownerID, projectID, threadID, false); err != nil {
			return codex.ThreadInfo{}, err
		}
		thread, err := client.ResumeThread(ctx, threadID)
		if err != nil {
			return codex.ThreadInfo{}, fmt.Errorf("resume task session %s: %w", ShortCode(threadID), err)
		}
		return thread, nil
	}

	name, err := normalizeName(suggestedName)
	if err != nil {
		return codex.ThreadInfo{}, err
	}
	thread, err := client.StartThread(ctx)
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
	// 排队后用户可能已切换会话；新任务线程只登记归属，不抢占后来做出的界面选择。
	if err := m.store.RegisterProject(ownerID, projectID, thread, false, m.now()); err != nil {
		_ = client.ArchiveThread(context.WithoutCancel(ctx), thread.ID)
		return codex.ThreadInfo{}, fmt.Errorf("persist task session: %w", err)
	}
	return thread, nil
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
	projectID := m.currentProject(ownerID)
	oldThreadID, hadOld := m.store.ActiveForProject(ownerID, projectID)
	if err := m.store.RegisterProject(ownerID, projectID, thread, true, m.now()); err != nil {
		_ = client.ArchiveThread(context.WithoutCancel(ctx), thread.ID)
		return codex.ThreadInfo{}, fmt.Errorf("persist new session: %w", err)
	}
	if hadOld && oldThreadID != thread.ID {
		_ = client.UnsubscribeThread(context.WithoutCancel(ctx), oldThreadID)
	}
	return thread, nil
}

func (m *Manager) Use(ctx context.Context, ownerID string, client codex.ThreadClient, reference string) (codex.ThreadInfo, error) {
	projectID := m.currentProject(ownerID)
	record, err := m.store.ResolveForProject(ownerID, projectID, reference, false)
	if err != nil {
		return codex.ThreadInfo{}, err
	}
	oldThreadID, _ := m.store.ActiveForProject(ownerID, projectID)
	if oldThreadID == record.ID {
		return client.ReadThread(ctx, record.ID)
	}
	thread, err := client.ResumeThread(ctx, record.ID)
	if err != nil {
		return codex.ThreadInfo{}, fmt.Errorf("resume session: %w", err)
	}
	if err := m.store.SetActiveForProject(ownerID, projectID, record.ID, m.now()); err != nil {
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
	threadID, ok := m.store.ActiveForProject(ownerID, m.currentProject(ownerID))
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

func (m *Manager) CurrentSettings(ownerID string) (ThreadSettings, error) {
	projectID := m.currentProject(ownerID)
	threadID, ok := m.store.ActiveForProject(ownerID, projectID)
	if !ok {
		return ThreadSettings{}, ErrNoActive
	}
	return m.store.SettingsForProject(ownerID, projectID, threadID)
}

func (m *Manager) SetCurrentSettings(ownerID string, settings ThreadSettings) error {
	projectID := m.currentProject(ownerID)
	threadID, ok := m.store.ActiveForProject(ownerID, projectID)
	if !ok {
		return ErrNoActive
	}
	return m.store.SetSettingsForProject(ownerID, projectID, threadID, settings)
}

// SettingsForTask 在轮次真正执行前读取其线程设置，确保模型选择属于线程而不是微信队列。
func (m *Manager) SettingsForTask(ownerID, projectID, threadID string) ThreadSettings {
	settings, err := m.store.SettingsForProject(strings.TrimSpace(ownerID), strings.TrimSpace(projectID), strings.TrimSpace(threadID))
	if err != nil {
		return ThreadSettings{}
	}
	return settings
}

func (m *Manager) ForkCurrent(ctx context.Context, ownerID string, client codex.ThreadClient, advanced codex.AdvancedThreadClient) (codex.ThreadInfo, error) {
	projectID := m.currentProject(ownerID)
	threadID, ok := m.store.ActiveForProject(ownerID, projectID)
	if !ok {
		return codex.ThreadInfo{}, ErrNoActive
	}
	source, err := client.ReadThread(ctx, threadID)
	if err != nil {
		return codex.ThreadInfo{}, fmt.Errorf("read thread before fork: %w", err)
	}
	forked, err := advanced.ForkThread(ctx, threadID)
	if err != nil {
		return codex.ThreadInfo{}, fmt.Errorf("fork thread: %w", err)
	}
	if name := forkName(source.Name); name != "" {
		if err := client.SetThreadName(ctx, forked.ID, name); err != nil {
			_ = client.ArchiveThread(context.WithoutCancel(ctx), forked.ID)
			return codex.ThreadInfo{}, fmt.Errorf("name forked thread: %w", err)
		}
		forked.Name = name
	}
	if err := m.store.RegisterProject(ownerID, projectID, forked, true, m.now()); err != nil {
		_ = client.ArchiveThread(context.WithoutCancel(ctx), forked.ID)
		return codex.ThreadInfo{}, fmt.Errorf("persist forked thread: %w", err)
	}
	// 分叉继承源线程的模型与推理强度，保持同一历史分支的执行语义连续。
	if settings, settingsErr := m.store.SettingsForProject(ownerID, projectID, threadID); settingsErr == nil && settings.Model != "" {
		if err := m.store.SetSettingsForProject(ownerID, projectID, forked.ID, settings); err != nil {
			_ = client.ArchiveThread(context.WithoutCancel(ctx), forked.ID)
			_ = m.store.DeleteForProject(ownerID, projectID, forked.ID, threadID)
			return codex.ThreadInfo{}, fmt.Errorf("persist forked thread settings: %w", err)
		}
	}
	_ = client.UnsubscribeThread(context.WithoutCancel(ctx), threadID)
	return forked, nil
}

func (m *Manager) PinCurrent(ctx context.Context, ownerID string, advanced codex.AdvancedThreadClient, pinned bool) (codex.ThreadInfo, error) {
	threadID, ok := m.store.ActiveForProject(ownerID, m.currentProject(ownerID))
	if !ok {
		return codex.ThreadInfo{}, ErrNoActive
	}
	thread, err := advanced.SetThreadPinned(ctx, threadID, pinned)
	if err != nil {
		return codex.ThreadInfo{}, fmt.Errorf("update thread pin: %w", err)
	}
	return thread, nil
}

func (m *Manager) CompactCurrent(ctx context.Context, ownerID string, advanced codex.AdvancedThreadClient) error {
	threadID, ok := m.store.ActiveForProject(ownerID, m.currentProject(ownerID))
	if !ok {
		return ErrNoActive
	}
	if err := advanced.CompactThread(ctx, threadID); err != nil {
		return fmt.Errorf("compact thread: %w", err)
	}
	return nil
}

func (m *Manager) CurrentGoal(ctx context.Context, ownerID string, advanced codex.AdvancedThreadClient) (codex.ThreadGoal, bool, error) {
	threadID, ok := m.store.ActiveForProject(ownerID, m.currentProject(ownerID))
	if !ok {
		return codex.ThreadGoal{}, false, ErrNoActive
	}
	return advanced.GetThreadGoal(ctx, threadID)
}

func (m *Manager) SetCurrentGoal(ctx context.Context, ownerID string, advanced codex.AdvancedThreadClient, objective string) (codex.ThreadGoal, error) {
	objective = strings.TrimSpace(objective)
	if objective == "" || len([]rune(objective)) > 4000 {
		return codex.ThreadGoal{}, fmt.Errorf("thread goal must contain 1 to 4000 characters")
	}
	threadID, ok := m.store.ActiveForProject(ownerID, m.currentProject(ownerID))
	if !ok {
		return codex.ThreadGoal{}, ErrNoActive
	}
	return advanced.SetThreadGoal(ctx, threadID, objective, nil)
}

func (m *Manager) ClearCurrentGoal(ctx context.Context, ownerID string, advanced codex.AdvancedThreadClient) error {
	threadID, ok := m.store.ActiveForProject(ownerID, m.currentProject(ownerID))
	if !ok {
		return ErrNoActive
	}
	return advanced.ClearThreadGoal(ctx, threadID)
}

func (m *Manager) UpdateCurrentGoalStatus(ctx context.Context, ownerID string, client codex.GoalStatusClient, status string) (codex.ThreadGoal, error) {
	threadID, ok := m.store.ActiveForProject(ownerID, m.currentProject(ownerID))
	if !ok {
		return codex.ThreadGoal{}, ErrNoActive
	}
	return client.UpdateThreadGoalStatus(ctx, threadID, status)
}

func (m *Manager) SteerCurrent(ctx context.Context, ownerID string, advanced codex.AdvancedThreadClient, request codex.ChatRequest) error {
	threadID, ok := m.store.ActiveForProject(ownerID, m.currentProject(ownerID))
	if !ok {
		return ErrNoActive
	}
	return advanced.SteerThread(ctx, threadID, request)
}

func (m *Manager) ReviewCurrent(ctx context.Context, ownerID string, advanced codex.AdvancedThreadClient, target codex.ReviewTarget, progress codex.ProgressHandler) (string, error) {
	threadID, ok := m.store.ActiveForProject(ownerID, m.currentProject(ownerID))
	if !ok {
		return "", ErrNoActive
	}
	return advanced.ReviewThread(ctx, threadID, target, progress)
}

// OwnsProjectThread 校验移动端后续动作仍指向该绑定者在指定工作空间登记的线程。
func (m *Manager) OwnsProjectThread(ownerID, projectID, threadID string) bool {
	return m.store.OwnsProject(strings.TrimSpace(ownerID), strings.TrimSpace(projectID), strings.TrimSpace(threadID))
}

// ReviewProjectThread 使用冻结的工作空间与线程目标重新审查，避免菜单期间焦点漂移。
func (m *Manager) ReviewProjectThread(
	ctx context.Context,
	ownerID, projectID, threadID string,
	advanced codex.AdvancedThreadClient,
	target codex.ReviewTarget,
	progress codex.ProgressHandler,
) (string, error) {
	ownerID = strings.TrimSpace(ownerID)
	projectID = strings.TrimSpace(projectID)
	threadID = strings.TrimSpace(threadID)
	if !m.store.OwnsProject(ownerID, projectID, threadID) {
		return "", ErrNotOwned
	}
	return advanced.ReviewThread(ctx, threadID, target, progress)
}

func (m *Manager) DeleteCurrent(ctx context.Context, ownerID string, advanced codex.AdvancedThreadClient) error {
	projectID := m.currentProject(ownerID)
	threadID, ok := m.store.ActiveForProject(ownerID, projectID)
	if !ok {
		return ErrNoActive
	}
	if err := advanced.DeleteThread(ctx, threadID); err != nil {
		return fmt.Errorf("delete thread: %w", err)
	}
	// 删除会递归覆盖所有派生线程；清空当前选择可避免误切换到待删除的深层后代。
	if err := m.store.DeleteForProject(ownerID, projectID, threadID, ""); err != nil {
		return fmt.Errorf("persist deleted thread: %w", err)
	}
	return nil
}

func (m *Manager) Archive(ctx context.Context, ownerID string, client codex.ThreadClient, reference string) (string, error) {
	if strings.TrimSpace(reference) == "" {
		var ok bool
		reference, ok = m.store.ActiveForProject(ownerID, m.currentProject(ownerID))
		if !ok {
			return "", ErrNoActive
		}
	}
	projectID := m.currentProject(ownerID)
	record, err := m.store.ResolveForProject(ownerID, projectID, reference, false)
	if err != nil {
		return "", err
	}
	activeID, _ := m.store.ActiveForProject(ownerID, projectID)
	nextActive := ""
	if activeID == record.ID {
		for _, candidate := range m.store.RecordsForProject(ownerID, projectID, false) {
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
	if err := m.store.MarkArchivedForProject(ownerID, projectID, record.ID, nextActive, true, m.now()); err != nil {
		_, _ = client.UnarchiveThread(context.WithoutCancel(ctx), record.ID)
		if nextActive != "" {
			_ = client.UnsubscribeThread(context.WithoutCancel(ctx), nextActive)
		}
		return "", fmt.Errorf("persist archived session: %w", err)
	}
	return nextActive, nil
}

func (m *Manager) Restore(ctx context.Context, ownerID string, client codex.ThreadClient, reference string) (codex.ThreadInfo, error) {
	projectID := m.currentProject(ownerID)
	record, err := m.store.ResolveForProject(ownerID, projectID, reference, true)
	if err != nil {
		return codex.ThreadInfo{}, err
	}
	thread, err := client.UnarchiveThread(ctx, record.ID)
	if err != nil {
		return codex.ThreadInfo{}, fmt.Errorf("restore session: %w", err)
	}
	_, hasActive := m.store.ActiveForProject(ownerID, projectID)
	nextActive := ""
	if !hasActive {
		nextActive = record.ID
	}
	if err := m.store.MarkArchivedForProject(ownerID, projectID, record.ID, nextActive, false, m.now()); err != nil {
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
	projectID := m.currentProject(ownerID)
	records := m.store.RecordsForProject(ownerID, projectID, archived)
	activeID, _ := m.store.ActiveForProject(ownerID, projectID)
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

func forkName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	suffix := " · 分支"
	limit := MaxSessionName - len([]rune(suffix))
	runes := []rune(name)
	if len(runes) > limit {
		name = string(runes[:limit])
	}
	return name + suffix
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
