package request

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

func resultSize(result Result) int64 {
	size := int64(len(result.Reply))
	for _, artifact := range result.Artifacts {
		size += artifact.Size
	}
	return size
}
func (store *Store) resultBytesLocked() int64 {
	var total int64
	for _, owner := range store.state.Owners {
		for _, task := range owner.Tasks {
			total += store.resultReservation(task)
		}
	}
	return total
}

// 进行中的工作预留最坏情况下的产物与回答空间，避免先执行后发现额度已满。
const maxResultReservation = MaxTaskBytes + maxResultReplyBytes

func (store *Store) resultReservation(task Task) int64 {
	if !task.State.Terminal() && task.ExecutionCompletedAt == 0 {
		return maxResultReservation
	}
	if task.ResultExpiresAt > store.now().Unix() {
		return task.ResultBytes
	}
	return 0
}

// BeginRedelivery 在发送前持久化操作编号，网络重试不得重复发送同一结果。
func (store *Store) BeginRedelivery(ownerID, taskID, operationID string) (Result, bool, error) {
	parsed, err := uuid.Parse(operationID)
	if err != nil {
		return Result{}, false, fmt.Errorf("invalid recovery operation id")
	}
	operationID = parsed.String()
	store.mu.Lock()
	defer store.mu.Unlock()
	task, ok := store.findTaskLocked(ownerID, taskID)
	if !ok || task.State != StateSucceeded || task.ResultExpiresAt <= store.now().Unix() {
		return Result{}, false, fmt.Errorf("请求结果不可重新投递")
	}
	result, err := store.loadResult(task)
	if err != nil {
		return Result{}, false, err
	}
	for _, attempt := range result.Attempts {
		if attempt.OperationID == operationID {
			return result, true, nil
		}
	}
	if len(result.Attempts) >= 50 {
		return Result{}, false, fmt.Errorf("该结果已达到投递次数上限")
	}
	// 在途操作统一显示待确认；崩溃后不能被误判为可以自动重投。
	receipt := DeliveryReceipt{OperationID: operationID, Outcome: DeliveryAmbiguous, AttemptedAt: max(store.now().Unix(), result.FrozenAt), FailureCode: ReasonDeliveryAmbiguous}
	result.Attempts = append(result.Attempts, receipt)
	result.Receipt = receipt
	if err := store.saveReceipts(task.ID, result); err != nil {
		return Result{}, false, err
	}
	return result, false, nil
}

// OpenArtifact 只打开冻结清单里的文件，并在同一个句柄上验证完整性。
func (store *Store) OpenArtifact(ownerID, taskID string, index int) (*os.File, ResultArtifact, error) {
	taskID = strings.TrimSpace(taskID)
	result, err := store.InspectResult(ownerID, taskID)
	if err != nil {
		return nil, ResultArtifact{}, err
	}
	if index < 0 || index >= len(result.Artifacts) {
		return nil, ResultArtifact{}, fmt.Errorf("产物不存在")
	}
	artifact := result.Artifacts[index]
	file, err := store.openResultArtifact(taskID, artifact)
	return file, artifact, err
}

func (store *Store) openResultArtifact(taskID string, artifact ResultArtifact) (*os.File, error) {
	path := filepath.Join(store.taskPath(taskID), artifact.Path)
	if err := rejectArtifactSymlinks(path); err != nil {
		return nil, err
	}
	// 固定目录句柄限制路径解析范围，避免检查后目录被替换而越界读取。
	root, err := os.OpenRoot(store.root)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	relative := filepath.Join(taskID, artifact.Path)
	info, err := root.Lstat(relative)
	if err != nil || !info.Mode().IsRegular() || info.Size() != artifact.Size {
		return nil, fmt.Errorf("产物不可用")
	}
	file, err := root.Open(relative)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		file.Close()
		return nil, fmt.Errorf("产物发生变化")
	}
	hash, err := hashFile(file, artifact.Size)
	if err != nil || hash != artifact.SHA256 {
		file.Close()
		return nil, fmt.Errorf("产物完整性校验失败")
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func (task Task) ExecutionStatus() string {
	if task.Reason == ReasonInterruptUnconfirmed {
		return "unknown"
	}
	if task.ExecutionCompletedAt > 0 {
		return "succeeded"
	}
	switch task.State {
	case StateRunning:
		return "running"
	case StateCancelled:
		return "cancelled"
	case StateFailed:
		return "failed"
	default:
		return "interrupted"
	}
}

// RecoverySource 使用绑定和客户端操作编号形成可重放的去重键。
func RecoverySource(owner, operationID string) (string, error) {
	parsed, err := uuid.Parse(operationID)
	if err != nil {
		return "", fmt.Errorf("恢复操作编号无效")
	}
	return fmt.Sprintf("recovery:%x:%s", sha256.Sum256([]byte(strings.TrimSpace(owner))), parsed.String()), nil
}
