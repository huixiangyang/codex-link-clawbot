package request

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/huixiangyang/codex-link-clawbot/internal/statefile"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const payloadRetention = 24 * time.Hour
const ResultRetention = 7 * 24 * time.Hour
const historyRetention = 30 * 24 * time.Hour

const (
	ReasonInterruptUnconfirmed = "interrupt_unconfirmed"
	ReasonUserCancelled        = "user_cancelled"
	ReasonRestartRunning       = "restart_running"
	ReasonRestartDelivery      = "restart_delivery"
	ReasonCodexFailed          = "codex_failed"
	ReasonDeliveryFailed       = "delivery_failed"
	ReasonDeliveryAmbiguous    = "delivery_ambiguous"
	ReasonResultFreezeFailed   = "result_freeze_failed"
	ReasonPayloadInvalid       = "payload_invalid"
	ReasonProjectUnavailable   = "project_unavailable"
	ReasonSessionUnavailable   = "session_unavailable"
)

// Retry 从仍在保留期内的失败输入创建全新任务，绝不回退原任务状态。
func (store *Store) Retry(ownerID, taskID, sourceMessageKey, contextToken string) (Task, error) {
	original, ok := store.Find(ownerID, taskID)
	if !ok || original.ExecutionCompletedAt != 0 || original.Reason == ReasonInterruptUnconfirmed || original.State != StateFailed && original.State != StateInterrupted {
		return Task{}, fmt.Errorf("only a failed or interrupted task can be retried")
	}
	if existing, found := store.FindBySource(strings.TrimSpace(sourceMessageKey)); found {
		if existing.OwnerID != ownerID || existing.RetryOf != taskID {
			return Task{}, fmt.Errorf("恢复操作编号已被使用")
		}
		return existing, nil
	}
	request, err := store.LoadRequest(ownerID, taskID)
	if err != nil {
		return Task{}, err
	}
	input := StartInput{
		TargetID:         original.TargetID,
		SourceMessageKey: strings.TrimSpace(sourceMessageKey), OwnerID: original.OwnerID,
		ProjectID: original.ProjectID, ThreadID: original.ThreadID, Summary: original.Summary,
		SourceData: request.SourceData, Text: request.Text, ContextToken: contextToken, ResponseMode: original.ResponseMode,
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
	retried, existed, err := store.Start(input)
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
	// 执行完成是不可逆的业务事实；后续投递失败只能改变投递回执。
	if current.ExecutionCompletedAt > 0 && next.Terminal() {
		if next == StateCancelled {
			return Task{}, fmt.Errorf("已完成的执行不可取消")
		}
		next, stage, reason = StateSucceeded, terminalStage(StateSucceeded), ""
		if current.ArchiveFailed {
			stage, reason = "执行完成，结果待保存", ReasonResultFreezeFailed
		}
	}
	if !allowedTransition(current.State, next) && !(current.State == StateRunning && next == StateSucceeded && current.ExecutionCompletedAt > 0) {
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
		owner.Tasks[index].PayloadExpiresAt = finishedAt + int64(payloadRetention.Seconds())
	}
	completed := owner.Tasks[index]
	store.state.Owners[ownerID] = owner
	if err := store.saveLocked(); err != nil {
		store.state.Owners[ownerID] = previous
		return Task{}, err
	}
	updated, ok := store.findTaskLocked(ownerID, current.ID)
	if !ok {
		return completed, nil
	}
	return updated, nil
}

func (store *Store) updateTask(ownerID, taskID string, change func(*Task) error) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.updateTaskLocked(ownerID, taskID, change)
}

func (store *Store) updateTaskLocked(ownerID, taskID string, change func(*Task) error) error {
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
	now := store.now().Unix()
	changed := false
	known := make(map[string]bool)
	for ownerID, owner := range store.state.Owners {
		for index := range owner.Tasks {
			task := &owner.Tasks[index]
			known[task.ID] = true
			if task.State == StateSucceeded && !task.ArchiveFailed && task.ResultExpiresAt > now {
				result, err := store.readResult(*task, false)
				if err != nil {
					return false, err
				}
				if result.Receipt.Outcome == DeliveryPending {
					result.Receipt = DeliveryReceipt{Outcome: DeliveryAmbiguous, AttemptedAt: max(now, result.FrozenAt), FailureCode: ReasonRestartDelivery}
					result.Attempts = append(result.Attempts, result.Receipt)
					if err := writeJSONAtomic(filepath.Join(store.taskPath(task.ID), "result.json"), result); err != nil {
						return false, err
					}
				}
			}
			// 完成检查点先于索引提交；崩溃后据此恢复成功事实，绝不重新执行。
			if task.ExecutionCompletedAt == 0 && !task.State.Terminal() {
				var completed Completion
				if found, err := statefile.ReadJSON(filepath.Join(store.taskPath(task.ID), "completion.json"), &completed, statefile.Options{MaxBytes: maxResultReplyBytes + (1 << 20)}); err == nil && found && completed.Version == 1 && completed.At >= task.StartedAt {
					task.ExecutionCompletedAt = completed.At
					task.ResultExpiresAt = completed.At + int64(ResultRetention.Seconds())
					task.ArchiveFailed = true
					task.ResultBytes = MaxTaskBytes + int64(len(completed.Reply))
				}
			}
			if task.State == StateRunning || task.State == StateDelivering {
				result, resultErr := store.loadResult(*task)
				if resultErr == nil {
					task.ExecutionCompletedAt = result.FrozenAt
					task.ResultExpiresAt = result.FrozenAt + int64(ResultRetention.Seconds())
					task.ResultBytes = resultSize(result)
					task.State, task.Stage, task.Reason = StateSucceeded, "执行完成", ""
					task.ArchiveFailed = false
					if result.Receipt.Outcome == DeliveryPending {
						result.Receipt = DeliveryReceipt{Outcome: DeliveryAmbiguous, AttemptedAt: max(now, result.FrozenAt), FailureCode: ReasonRestartDelivery}
						if err := writeJSONAtomic(filepath.Join(store.taskPath(task.ID), "result.json"), result); err != nil {
							return false, err
						}
					}
				} else if task.ExecutionCompletedAt > 0 && task.ArchiveFailed {
					task.State, task.Stage, task.Reason = StateSucceeded, "执行完成，结果待保存", ReasonResultFreezeFailed
				} else if task.ExecutionCompletedAt > 0 || task.State == StateDelivering {
					return false, fmt.Errorf("恢复请求结果失败 %s: %w", task.ID, resultErr)
				} else {
					task.State, task.Stage, task.Reason = StateInterrupted, "执行中断，等待处理", ReasonRestartRunning
				}
				task.FinishedAt = max(now, task.StartedAt)
				task.PayloadExpiresAt = task.FinishedAt + int64(payloadRetention.Seconds())
				changed = true
			}
			if taskHasPayload(*task, now) {
				if err := store.validatePayloadRootLocked(*task); err != nil {
					return false, err
				}
			}
		}
		store.state.Owners[ownerID] = owner
	}
	entries, err := os.ReadDir(store.root)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".staging-") || taskIDPattern.MatchString(name) && !known[name] {
			if err := os.RemoveAll(filepath.Join(store.root, name)); err != nil {
				return false, err
			}
		} else if taskIDPattern.MatchString(name) {
			if err := store.cleanTaskTemporaryFiles(name); err != nil {
				return false, err
			}
		}
	}
	if err := store.cleanupExpiredPayloadsLocked(); err != nil {
		return false, err
	}
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

// CleanupExpired 分别清理输入、结果与历史；输入到期不影响仍可取回的结果。
func (store *Store) CleanupExpired() error {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.cleanupExpiredPayloadsLocked()
}
func (store *Store) cleanupExpiredPayloadsLocked() error {
	now := store.now().Unix()
	for source, receipt := range store.state.Cleared {
		if receipt.At <= now-int64(historyRetention.Seconds()) {
			delete(store.state.Cleared, source)
		}
	}
	for source, receipt := range store.state.Rejected {
		if receipt.At <= now-int64(historyRetention.Seconds()) {
			delete(store.state.Rejected, source)
		}
	}
	for ownerID, owner := range store.state.Owners {
		kept := make([]Task, 0, len(owner.Tasks))
		for _, task := range owner.Tasks {
			if !task.State.Terminal() {
				kept = append(kept, task)
				continue
			}
			root := store.taskPath(task.ID)
			if task.PayloadExpiresAt <= now {
				for _, name := range []string{"request.json", "prepared.json", "inbox"} {
					if err := os.RemoveAll(filepath.Join(root, name)); err != nil {
						return err
					}
				}
			}
			if task.ResultExpiresAt <= now {
				store.resultSummaries.Delete(task.ID)
				for _, name := range []string{"result.json", "completion.json", "outbox"} {
					if err := os.RemoveAll(filepath.Join(root, name)); err != nil {
						return err
					}
				}
			}
			if task.FinishedAt+int64(historyRetention.Seconds()) <= now {
				if err := os.RemoveAll(root); err != nil {
					return err
				}
				continue
			}
			kept = append(kept, task)
		}
		if len(kept) != len(owner.Tasks) {
			previous := owner
			owner.Tasks = kept
			store.state.Owners[ownerID] = owner
			if err := store.saveLocked(); err != nil {
				store.state.Owners[ownerID] = previous
				return err
			}
		}
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
		store.resultSummaries.Delete(taskID)
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

func (s *Store) AttachTurn(owner, id, turn string) error {
	if t, ok := s.Find(owner, id); ok && t.TurnID == turn {
		return nil
	}
	return s.updateTask(owner, id, func(t *Task) error {
		if t.TurnID == turn {
			return nil
		}
		if t.TurnID != "" || !validSingleLine(turn, 512) {
			return fmt.Errorf("轮次不可改变")
		}
		t.TurnID = turn
		return nil
	})
}

func ReasonLabel(reason string) string {
	labels := map[string]string{ReasonInterruptUnconfirmed: "打断未确认，请到会话状态刷新或再次打断", ReasonResultFreezeFailed: "执行已完成，结果等待恢复保存", ReasonCodexFailed: "Codex 执行失败，输入有效时可重新执行", ReasonPayloadInvalid: "附件或输入未能接收，请重新发送", ReasonSessionUnavailable: "原会话不可用，请切换会话", ReasonProjectUnavailable: "工作空间不可用，请检查配置", ReasonRestartRunning: "服务重启中断，没有自动重跑", ReasonUserCancelled: "已打断本次工作，已有修改保留", ReasonDeliveryFailed: "微信发送失败，可取回保存的结果", ReasonDeliveryAmbiguous: "微信发送未确认，请先检查是否收到"}
	if label, ok := labels[reason]; ok {
		return label
	}
	return "本次工作已停止，请检查会话状态"
}
