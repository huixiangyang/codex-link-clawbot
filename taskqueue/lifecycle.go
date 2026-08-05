package taskqueue

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const payloadRetention = 24 * time.Hour

const (
	ReasonUserCancelled      = "user_cancelled"
	ReasonQueueCleared       = "queue_cleared"
	ReasonRestartRunning     = "restart_running"
	ReasonRestartDelivery    = "restart_delivery"
	ReasonCodexFailed        = "codex_failed"
	ReasonDeliveryFailed     = "delivery_failed"
	ReasonDeliveryAmbiguous  = "delivery_ambiguous"
	ReasonResultFreezeFailed = "result_freeze_failed"
	ReasonPayloadInvalid     = "payload_invalid"
	ReasonProjectUnavailable = "project_unavailable"
	ReasonSessionUnavailable = "session_unavailable"
)

func (store *Store) SetPaused(ownerID string, paused bool) error {
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return fmt.Errorf("task owner is required")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	owner := store.state.Owners[ownerID]
	if owner.Paused == paused {
		return nil
	}
	previous, existed := store.state.Owners[ownerID]
	owner.Paused = paused
	store.state.Owners[ownerID] = owner
	if err := store.saveLocked(); err != nil {
		if existed {
			store.state.Owners[ownerID] = previous
		} else {
			delete(store.state.Owners, ownerID)
		}
		return err
	}
	return nil
}

func (store *Store) MoveToFront(ownerID, taskID string) (Task, error) {
	ownerID = strings.TrimSpace(ownerID)
	store.mu.Lock()
	defer store.mu.Unlock()
	owner, exists := store.state.Owners[ownerID]
	if !exists {
		return Task{}, fmt.Errorf("task not found")
	}
	index := taskIndex(owner.Tasks, strings.TrimSpace(taskID))
	if index < 0 || owner.Tasks[index].State != StateQueued {
		return Task{}, fmt.Errorf("only a queued task can move to the front")
	}
	minimum := owner.Tasks[index].Order
	for _, candidateOwner := range store.state.Owners {
		for _, task := range candidateOwner.Tasks {
			if task.State == StateQueued && task.Order < minimum {
				minimum = task.Order
			}
		}
	}
	previous := owner
	owner.Tasks = append([]Task(nil), owner.Tasks...)
	owner.Tasks[index].Order = minimum - 1
	store.state.Owners[ownerID] = owner
	if err := store.saveLocked(); err != nil {
		store.state.Owners[ownerID] = previous
		return Task{}, err
	}
	return owner.Tasks[index], nil
}

func (store *Store) DeleteQueued(ownerID, taskID string) (Task, error) {
	return store.finish(ownerID, taskID, StateCancelled, ReasonUserCancelled)
}

// Delete 删除等待任务或终态记录；活动任务只能通过取消流程结束。
func (store *Store) Delete(ownerID, taskID string) error {
	task, ok := store.Find(ownerID, taskID)
	if !ok {
		return fmt.Errorf("task not found")
	}
	if task.State == StateQueued {
		_, err := store.DeleteQueued(ownerID, taskID)
		return err
	}
	if !task.State.Terminal() {
		return fmt.Errorf("active task cannot be deleted")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	owner := store.state.Owners[strings.TrimSpace(ownerID)]
	index := taskIndex(owner.Tasks, strings.TrimSpace(taskID))
	if index < 0 {
		return fmt.Errorf("task not found")
	}
	previous := owner
	owner.Tasks = append([]Task(nil), owner.Tasks...)
	owner.Tasks = append(owner.Tasks[:index], owner.Tasks[index+1:]...)
	store.state.Owners[strings.TrimSpace(ownerID)] = owner
	if err := store.saveLocked(); err != nil {
		store.state.Owners[strings.TrimSpace(ownerID)] = previous
		return err
	}
	if err := store.removeTaskIDs([]string{taskID}); err != nil {
		return err
	}
	return nil
}

// Retry 从仍在保留期内的失败输入创建全新任务，绝不回退原任务状态。
func (store *Store) Retry(ownerID, taskID, sourceMessageKey, contextToken string) (Task, error) {
	original, ok := store.Find(ownerID, taskID)
	if !ok || original.State != StateFailed && original.State != StateInterrupted {
		return Task{}, fmt.Errorf("only a failed or interrupted task can be retried")
	}
	request, err := store.LoadRequest(ownerID, taskID)
	if err != nil {
		return Task{}, err
	}
	input := EnqueueInput{
		SourceMessageKey: strings.TrimSpace(sourceMessageKey), OwnerID: original.OwnerID,
		ProjectID: original.ProjectID, ThreadID: original.ThreadID, Summary: original.Summary,
		Text: request.Text, ContextToken: contextToken, ResponseMode: original.ResponseMode,
		VisualStyle: original.VisualStyle, RetryOf: original.ID,
	}
	for _, attachment := range request.Images {
		data, err := readRetryAttachment(attachment)
		if err != nil {
			return Task{}, err
		}
		input.Images = append(input.Images, InputAttachment{Name: attachment.Name, ContentType: attachment.ContentType, Data: data})
	}
	for _, attachment := range request.Files {
		data, err := readRetryAttachment(attachment)
		if err != nil {
			return Task{}, err
		}
		input.Files = append(input.Files, InputAttachment{Name: attachment.Name, ContentType: attachment.ContentType, Data: data})
	}
	retried, existed, err := store.Enqueue(input)
	if err != nil {
		return Task{}, err
	}
	if existed && retried.RetryOf != original.ID {
		return Task{}, fmt.Errorf("retry source message already belongs to another task")
	}
	return retried, nil
}

func readRetryAttachment(attachment LoadedAttachment) ([]byte, error) {
	data, err := os.ReadFile(attachment.AbsolutePath)
	if err != nil {
		return nil, fmt.Errorf("read retained task attachment: %w", err)
	}
	hash := sha256.Sum256(data)
	if int64(len(data)) != attachment.Size || hex.EncodeToString(hash[:]) != attachment.SHA256 {
		return nil, fmt.Errorf("retained task attachment changed during retry")
	}
	return data, nil
}

func (store *Store) ClearQueued(ownerID string) (int, error) {
	ownerID = strings.TrimSpace(ownerID)
	store.mu.Lock()
	defer store.mu.Unlock()
	owner, exists := store.state.Owners[ownerID]
	if !exists {
		return 0, nil
	}
	previous := owner
	owner.Tasks = append([]Task(nil), owner.Tasks...)
	now := store.now().Unix()
	var cleanup []string
	count := 0
	for index := range owner.Tasks {
		if owner.Tasks[index].State != StateQueued {
			continue
		}
		owner.Tasks[index].State = StateCancelled
		owner.Tasks[index].Stage = "已删除"
		owner.Tasks[index].Reason = ReasonQueueCleared
		finishedAt := now
		if finishedAt < owner.Tasks[index].CreatedAt {
			finishedAt = owner.Tasks[index].CreatedAt
		}
		owner.Tasks[index].FinishedAt = finishedAt
		owner.Tasks[index].PayloadExpiresAt = 0
		cleanup = append(cleanup, owner.Tasks[index].ID)
		count++
	}
	if count == 0 {
		return 0, nil
	}
	owner, pruned := pruneTerminal(owner)
	cleanup = append(cleanup, pruned...)
	store.state.Owners[ownerID] = owner
	if err := store.saveLocked(); err != nil {
		store.state.Owners[ownerID] = previous
		return 0, err
	}
	store.cleanupTaskIDs(cleanup)
	return count, nil
}

func (store *Store) ClaimNext(blockedOwners map[string]bool) (Task, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, owner := range store.state.Owners {
		for _, task := range owner.Tasks {
			if task.State == StateRunning || task.State == StateDelivering {
				return Task{}, false, nil
			}
		}
	}
	selectedOwner := ""
	selectedIndex := -1
	var selected Task
	for ownerID, owner := range store.state.Owners {
		if owner.Paused || blockedOwners[ownerID] {
			continue
		}
		for index, task := range owner.Tasks {
			if task.State != StateQueued {
				continue
			}
			if selectedIndex < 0 || task.Order < selected.Order || task.Order == selected.Order && task.ID < selected.ID {
				selectedOwner, selectedIndex, selected = ownerID, index, task
			}
		}
	}
	if selectedIndex < 0 {
		return Task{}, false, nil
	}
	owner := store.state.Owners[selectedOwner]
	previous := owner
	owner.Tasks = append([]Task(nil), owner.Tasks...)
	owner.Tasks[selectedIndex].State = StateRunning
	owner.Tasks[selectedIndex].Stage = "任务已接收，正在分析"
	owner.Tasks[selectedIndex].Reason = ""
	startedAt := store.now().Unix()
	if startedAt < owner.Tasks[selectedIndex].CreatedAt {
		startedAt = owner.Tasks[selectedIndex].CreatedAt
	}
	owner.Tasks[selectedIndex].StartedAt = startedAt
	owner.Tasks[selectedIndex].FinishedAt = 0
	store.state.Owners[selectedOwner] = owner
	if err := store.saveLocked(); err != nil {
		store.state.Owners[selectedOwner] = previous
		return Task{}, false, err
	}
	return owner.Tasks[selectedIndex], true, nil
}

func (store *Store) UpdateStage(ownerID, taskID, stage string) error {
	stage = strings.TrimSpace(stage)
	if !validSingleLine(stage, 120) {
		return fmt.Errorf("task stage is invalid")
	}
	return store.updateTask(ownerID, taskID, func(task *Task) error {
		if task.State != StateRunning && task.State != StateDelivering {
			return fmt.Errorf("task stage cannot change in state %s", task.State)
		}
		task.Stage = stage
		return nil
	})
}

func (store *Store) AttachThread(ownerID, taskID, threadID string) error {
	threadID = strings.TrimSpace(threadID)
	if !validSingleLine(threadID, 512) {
		return fmt.Errorf("task thread is invalid")
	}
	return store.updateTask(ownerID, taskID, func(task *Task) error {
		if task.State != StateRunning {
			return fmt.Errorf("task thread can only attach while running")
		}
		if task.ThreadID != "" && task.ThreadID != threadID {
			return fmt.Errorf("task thread is already fixed")
		}
		task.ThreadID = threadID
		return nil
	})
}

func (store *Store) AttachUsage(ownerID, taskID string, inputTokens, outputTokens, totalTokens int64) error {
	if inputTokens < 0 || outputTokens < 0 || totalTokens < 0 {
		return fmt.Errorf("task token usage is invalid")
	}
	return store.updateTask(ownerID, taskID, func(task *Task) error {
		if task.State != StateRunning && task.State != StateDelivering {
			return fmt.Errorf("task usage cannot change in state %s", task.State)
		}
		task.InputTokens = inputTokens
		task.OutputTokens = outputTokens
		task.TotalTokens = totalTokens
		return nil
	})
}

func (store *Store) BeginDelivery(ownerID, taskID string) (Task, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	task, ok := store.findTaskLocked(strings.TrimSpace(ownerID), strings.TrimSpace(taskID))
	if !ok || task.State != StateRunning {
		return Task{}, fmt.Errorf("only a running task can begin delivery")
	}
	if _, err := store.loadResult(task); err != nil {
		return Task{}, fmt.Errorf("load frozen task result: %w", err)
	}
	return store.transitionLocked(ownerID, taskID, StateDelivering, "正在发送结果", "")
}

func (store *Store) Finish(ownerID, taskID string, state State, reason string) (Task, error) {
	if state != StateSucceeded && state != StateFailed && state != StateInterrupted && state != StateCancelled {
		return Task{}, fmt.Errorf("invalid terminal task state")
	}
	return store.finish(ownerID, taskID, state, reason)
}

func (store *Store) finish(ownerID, taskID string, state State, reason string) (Task, error) {
	if reason != "" && !reasonPattern.MatchString(reason) {
		return Task{}, fmt.Errorf("task reason is invalid")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	stage := terminalStage(state)
	task, err := store.transitionLocked(ownerID, taskID, state, stage, reason)
	if err != nil {
		return Task{}, err
	}
	return task, nil
}

func (store *Store) transitionLocked(ownerID, taskID string, next State, stage, reason string) (Task, error) {
	ownerID = strings.TrimSpace(ownerID)
	owner, exists := store.state.Owners[ownerID]
	if !exists {
		return Task{}, fmt.Errorf("task not found")
	}
	index := taskIndex(owner.Tasks, strings.TrimSpace(taskID))
	if index < 0 {
		return Task{}, fmt.Errorf("task not found")
	}
	current := owner.Tasks[index]
	if !allowedTransition(current.State, next) {
		return Task{}, fmt.Errorf("task cannot transition from %s to %s", current.State, next)
	}
	previous := owner
	owner.Tasks = append([]Task(nil), owner.Tasks...)
	owner.Tasks[index].State = next
	owner.Tasks[index].Stage = stage
	owner.Tasks[index].Reason = reason
	if next.Terminal() {
		now := store.now()
		finishedAt := now.Unix()
		if finishedAt < owner.Tasks[index].StartedAt {
			finishedAt = owner.Tasks[index].StartedAt
		}
		if finishedAt < owner.Tasks[index].CreatedAt {
			finishedAt = owner.Tasks[index].CreatedAt
		}
		owner.Tasks[index].FinishedAt = finishedAt
		if next == StateFailed || next == StateInterrupted {
			owner.Tasks[index].PayloadExpiresAt = time.Unix(finishedAt, 0).Add(payloadRetention).Unix()
		} else {
			owner.Tasks[index].PayloadExpiresAt = 0
		}
	}
	cleanup := []string(nil)
	if next == StateSucceeded || next == StateCancelled {
		cleanup = append(cleanup, current.ID)
	}
	completed := owner.Tasks[index]
	owner, pruned := pruneTerminal(owner)
	cleanup = append(cleanup, pruned...)
	store.state.Owners[ownerID] = owner
	if err := store.saveLocked(); err != nil {
		store.state.Owners[ownerID] = previous
		return Task{}, err
	}
	store.cleanupTaskIDs(cleanup)
	updated, ok := store.findTaskLocked(ownerID, current.ID)
	if !ok {
		return completed, nil
	}
	return updated, nil
}

func (store *Store) updateTask(ownerID, taskID string, change func(*Task) error) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	ownerID = strings.TrimSpace(ownerID)
	owner, exists := store.state.Owners[ownerID]
	if !exists {
		return fmt.Errorf("task not found")
	}
	index := taskIndex(owner.Tasks, strings.TrimSpace(taskID))
	if index < 0 {
		return fmt.Errorf("task not found")
	}
	previous := owner
	owner.Tasks = append([]Task(nil), owner.Tasks...)
	if err := change(&owner.Tasks[index]); err != nil {
		return err
	}
	store.state.Owners[ownerID] = owner
	if err := store.saveLocked(); err != nil {
		store.state.Owners[ownerID] = previous
		return err
	}
	return nil
}

func (store *Store) recoverLocked() (bool, error) {
	now := store.now()
	nowUnix := now.Unix()
	changed := false
	known := make(map[string]Task)
	var cleanup []string
	for ownerID, owner := range store.state.Owners {
		owner.Tasks = append([]Task(nil), owner.Tasks...)
		for index := range owner.Tasks {
			task := &owner.Tasks[index]
			taskNow := nowUnix
			if taskNow < task.CreatedAt {
				taskNow = task.CreatedAt
			}
			if task.State == StateDelivering {
				if _, err := store.loadResult(*task); err != nil {
					return false, fmt.Errorf("recover delivering task %s: %w", task.ID, err)
				}
			}
			switch task.State {
			case StateRunning:
				task.State = StateInterrupted
				task.Stage = "服务重启，等待处理"
				task.Reason = ReasonRestartRunning
				task.FinishedAt = taskNow
				task.PayloadExpiresAt = time.Unix(taskNow, 0).Add(payloadRetention).Unix()
				changed = true
			case StateDelivering:
				task.State = StateInterrupted
				task.Stage = "发送被重启中断，等待处理"
				task.Reason = ReasonRestartDelivery
				task.FinishedAt = taskNow
				task.PayloadExpiresAt = time.Unix(taskNow, 0).Add(payloadRetention).Unix()
				changed = true
			case StateSucceeded, StateCancelled:
				cleanup = append(cleanup, task.ID)
			case StateFailed, StateInterrupted:
				if task.PayloadExpiresAt == 0 {
					task.PayloadExpiresAt = time.Unix(taskNow, 0).Add(payloadRetention).Unix()
					changed = true
				}
				if taskNow >= task.PayloadExpiresAt {
					cleanup = append(cleanup, task.ID)
				}
			}
			if taskHasPayload(*task, taskNow) {
				if err := store.validatePayloadRootLocked(*task); err != nil {
					return false, err
				}
			}
			known[task.ID] = *task
		}
		var pruned []string
		owner, pruned = pruneTerminal(owner)
		if len(pruned) > 0 {
			cleanup = append(cleanup, pruned...)
			changed = true
		}
		store.state.Owners[ownerID] = owner
	}

	entries, err := os.ReadDir(store.root)
	if err != nil {
		return false, fmt.Errorf("scan task queue root: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		switch {
		case strings.HasPrefix(name, ".staging-"):
			if err := os.RemoveAll(filepath.Join(store.root, name)); err != nil {
				return false, fmt.Errorf("clean abandoned task staging: %w", err)
			}
			changed = true
		case taskIDPattern.MatchString(name):
			if _, exists := known[name]; !exists {
				if err := os.RemoveAll(filepath.Join(store.root, name)); err != nil {
					return false, fmt.Errorf("clean orphan task payload: %w", err)
				}
				changed = true
			} else if err := store.cleanTaskTemporaryFiles(name); err != nil {
				return false, err
			}
		}
	}
	store.cleanupTaskIDs(cleanup)
	return changed, nil
}

func (store *Store) cleanTaskTemporaryFiles(taskID string) error {
	entries, err := os.ReadDir(store.taskPath(taskID))
	if err != nil {
		return fmt.Errorf("scan task %s private directory: %w", taskID, err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".result-") {
			continue
		}
		if err := os.RemoveAll(filepath.Join(store.taskPath(taskID), entry.Name())); err != nil {
			return fmt.Errorf("clean task result staging: %w", err)
		}
	}
	return nil
}

// CleanupExpired 删除已超过保留期限的失败任务输入，供常驻协调器定时调用。
func (store *Store) CleanupExpired() error {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.cleanupExpiredPayloadsLocked()
}

func (store *Store) cleanupExpiredPayloadsLocked() error {
	now := store.now().Unix()
	var cleanup []string
	for _, owner := range store.state.Owners {
		for _, task := range owner.Tasks {
			if (task.State == StateFailed || task.State == StateInterrupted) && task.PayloadExpiresAt > 0 && now >= task.PayloadExpiresAt {
				cleanup = append(cleanup, task.ID)
			}
		}
	}
	if err := store.removeTaskIDs(cleanup); err != nil {
		return err
	}
	return nil
}

func (store *Store) validatePayloadRootLocked(task Task) error {
	taskPath := store.taskPath(task.ID)
	info, err := os.Lstat(taskPath)
	if err != nil {
		return fmt.Errorf("task %s payload directory is missing or unreadable: %w", task.ID, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("task %s payload directory is invalid", task.ID)
	}
	if err := os.Chmod(taskPath, 0o700); err != nil {
		return fmt.Errorf("protect task %s payload directory: %w", task.ID, err)
	}
	requestPath := filepath.Join(taskPath, "request.json")
	requestInfo, err := os.Lstat(requestPath)
	if err != nil {
		return fmt.Errorf("task %s request is missing or unreadable: %w", task.ID, err)
	}
	if !requestInfo.Mode().IsRegular() || requestInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("task %s request is invalid", task.ID)
	}
	if err := os.Chmod(requestPath, 0o600); err != nil {
		return fmt.Errorf("protect task %s request: %w", task.ID, err)
	}
	return nil
}

func (store *Store) cleanupTaskIDs(taskIDs []string) {
	_ = store.removeTaskIDs(taskIDs)
}

func (store *Store) removeTaskIDs(taskIDs []string) error {
	seen := make(map[string]struct{}, len(taskIDs))
	for _, taskID := range taskIDs {
		if !taskIDPattern.MatchString(taskID) {
			continue
		}
		if _, exists := seen[taskID]; exists {
			continue
		}
		seen[taskID] = struct{}{}
		if err := os.RemoveAll(store.taskPath(taskID)); err != nil {
			return fmt.Errorf("remove task %s payload: %w", taskID, err)
		}
	}
	if len(seen) > 0 {
		if err := syncDirectory(store.root); err != nil {
			return fmt.Errorf("sync task payload cleanup: %w", err)
		}
	}
	return nil
}

func taskIndex(tasks []Task, taskID string) int {
	for index := range tasks {
		if tasks[index].ID == taskID {
			return index
		}
	}
	return -1
}

func allowedTransition(current, next State) bool {
	switch current {
	case StateQueued:
		return next == StateRunning || next == StateCancelled
	case StateRunning:
		return next == StateDelivering || next == StateFailed || next == StateInterrupted || next == StateCancelled
	case StateDelivering:
		return next == StateSucceeded || next == StateFailed || next == StateInterrupted
	default:
		return false
	}
}

func terminalStage(state State) string {
	switch state {
	case StateSucceeded:
		return "已完成"
	case StateFailed:
		return "执行失败，等待处理"
	case StateInterrupted:
		return "任务中断，等待处理"
	case StateCancelled:
		return "已取消"
	default:
		return "状态异常"
	}
}

func pruneTerminal(owner OwnerQueue) (OwnerQueue, []string) {
	var active []Task
	var terminal []Task
	for _, task := range owner.Tasks {
		if task.State.Terminal() {
			terminal = append(terminal, task)
		} else {
			active = append(active, task)
		}
	}
	sort.SliceStable(terminal, func(left, right int) bool {
		if terminal[left].FinishedAt == terminal[right].FinishedAt {
			return terminal[left].CreatedAt > terminal[right].CreatedAt
		}
		return terminal[left].FinishedAt > terminal[right].FinishedAt
	})
	var removed []string
	if len(terminal) > MaxTerminalPerOwner {
		for _, task := range terminal[MaxTerminalPerOwner:] {
			removed = append(removed, task.ID)
		}
		terminal = terminal[:MaxTerminalPerOwner]
	}
	owner.Tasks = append(active, terminal...)
	return owner, removed
}
