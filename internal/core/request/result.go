package request

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/presentation"
)

const (
	resultVersion       = 2
	maxResultReplyBytes = 5 << 20
	maxResultArtifacts  = 8
	maxResultURLs       = 8
)

type DeliveryOutcome string

const (
	DeliveryPending         DeliveryOutcome = "pending"
	DeliverySucceeded       DeliveryOutcome = "succeeded"
	DeliveryExplicitFailure DeliveryOutcome = "explicit_failure"
	DeliveryAmbiguous       DeliveryOutcome = "ambiguous"
)

func (outcome DeliveryOutcome) valid() bool {
	switch outcome {
	case DeliveryPending, DeliverySucceeded, DeliveryExplicitFailure, DeliveryAmbiguous:
		return true
	default:
		return false
	}
}

type ResultArtifact struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type DeliveryReceipt struct {
	OperationID string          `json:"operation_id,omitempty"`
	Outcome     DeliveryOutcome `json:"outcome"`
	AttemptedAt int64           `json:"attempted_at,omitempty"`
	MediaSent   int             `json:"media_sent,omitempty"`
	TextSent    bool            `json:"text_sent,omitempty"`
	FailureCode string          `json:"failure_code,omitempty"`
}

type Result struct {
	Attempts     []DeliveryReceipt         `json:"attempts,omitempty"`
	Version      int                       `json:"version"`
	Reply        string                    `json:"reply,omitempty"`
	Artifacts    []ResultArtifact          `json:"artifacts"`
	ImageURLs    []string                  `json:"image_urls"`
	ResponseMode presentation.ResponseMode `json:"response_mode"`
	VisualStyle  presentation.Style        `json:"visual_style"`
	FrozenAt     int64                     `json:"frozen_at"`
	Receipt      DeliveryReceipt           `json:"receipt"`
}

type FreezeResultInput struct {
	Reply         string
	ArtifactPaths []string
	ImageURLs     []string
}

func (store *Store) FreezeResult(ownerID, taskID string, input FreezeResultInput) (Result, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	task, ok := store.findTaskLocked(strings.TrimSpace(ownerID), strings.TrimSpace(taskID))
	if !ok || task.State != StateRunning && !(task.State == StateSucceeded && task.ArchiveFailed) {
		return Result{}, fmt.Errorf("only a running task can freeze its result")
	}
	if len([]byte(input.Reply)) > maxResultReplyBytes || len(input.ArtifactPaths) > maxResultArtifacts || len(input.ImageURLs) > maxResultURLs {
		return Result{}, fmt.Errorf("task result exceeds its limits")
	}
	result := Result{
		Version: resultVersion, Reply: input.Reply,
		Artifacts:    make([]ResultArtifact, 0, len(input.ArtifactPaths)),
		ImageURLs:    append([]string(nil), input.ImageURLs...),
		ResponseMode: task.ResponseMode, VisualStyle: task.VisualStyle,
		FrozenAt: store.now().Unix(), Receipt: DeliveryReceipt{Outcome: DeliveryPending},
	}
	if result.FrozenAt < task.StartedAt {
		result.FrozenAt = task.StartedAt
	}
	var total int64
	seenPaths := make(map[string]bool)
	for _, artifactPath := range input.ArtifactPaths {
		artifact, err := store.freezeArtifact(task, artifactPath)
		if err != nil {
			return Result{}, err
		}
		if seenPaths[artifact.Path] {
			return Result{}, fmt.Errorf("duplicated result artifact")
		}
		seenPaths[artifact.Path] = true
		total += artifact.Size
		if total > MaxTaskBytes {
			return Result{}, fmt.Errorf("result artifacts exceed the total size limit")
		}
		result.Artifacts = append(result.Artifacts, artifact)
	}
	if err := validateResult(result, task); err != nil {
		return Result{}, err
	}
	if store.resultBytesLocked()-store.resultReservation(task)+resultSize(result) > MaxResultStoreBytes {
		return Result{}, fmt.Errorf("结果存储已达 1 GiB 上限，请先清理到期结果")
	}
	if _, err := store.readResultRows(task.ID); err == nil {
		return Result{}, fmt.Errorf("task result is already frozen")
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Result{}, err
	}

	if err := store.updateTaskLocked(ownerID, taskID, func(task *Task) error {
		if task.ExecutionCompletedAt == 0 {
			task.ExecutionCompletedAt = result.FrozenAt
		}
		task.ArchiveFailed = false
		if task.State == StateSucceeded {
			task.Stage = "执行完成，结果已保存"
			task.Reason = ""
		}
		task.ResultExpiresAt = task.ExecutionCompletedAt + int64(ResultRetention.Seconds())
		task.ResultBytes = resultSize(result)
		return nil
	}, func(tx *sql.Tx) error {
		if err := saveResult(tx, task.ID, result); err != nil {
			return err
		}
		_, err := tx.Exec("DELETE FROM completions WHERE request_id=?", task.ID)
		return err
	}); err != nil {
		return Result{}, err
	}
	return result, nil
}

func (store *Store) freezeArtifact(task Task, absolutePath string) (ResultArtifact, error) {
	taskRoot := store.taskPath(task.ID)
	absolutePath = filepath.Clean(absolutePath)
	relativePath, err := filepath.Rel(taskRoot, absolutePath)
	if err != nil || !filepath.IsLocal(relativePath) || !strings.HasPrefix(relativePath, "output"+string(filepath.Separator)) {
		return ResultArtifact{}, fmt.Errorf("result artifact escaped its task outbox")
	}
	if err := rejectArtifactSymlinks(absolutePath); err != nil {
		return ResultArtifact{}, err
	}
	info, err := os.Lstat(absolutePath)
	if err != nil {
		return ResultArtifact{}, fmt.Errorf("inspect result artifact: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > MaxFileBytes {
		return ResultArtifact{}, fmt.Errorf("result artifact is invalid")
	}
	if err := os.Chmod(absolutePath, 0o600); err != nil {
		return ResultArtifact{}, fmt.Errorf("protect result artifact: %w", err)
	}
	hash, err := hashRegularFile(absolutePath, info.Size())
	if err != nil {
		return ResultArtifact{}, err
	}
	file, err := os.Open(absolutePath)
	if err != nil {
		return ResultArtifact{}, err
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(syncErr, closeErr, syncDirectory(filepath.Dir(absolutePath))); err != nil {
		return ResultArtifact{}, err
	}
	return ResultArtifact{Name: filepath.Base(absolutePath), Path: relativePath, Size: info.Size(), SHA256: hash}, nil
}

func (store *Store) LoadResult(ownerID, taskID string) (Result, error) {
	store.mu.RLock()
	task, ok := store.findTaskLocked(strings.TrimSpace(ownerID), strings.TrimSpace(taskID))
	store.mu.RUnlock()
	if !ok || task.ExecutionCompletedAt == 0 {
		return Result{}, fmt.Errorf("task result is unavailable")
	}
	if task.ResultExpiresAt <= store.now().Unix() {
		return Result{}, fmt.Errorf("task result is unavailable")
	}
	return store.loadResult(task)
}

func (store *Store) loadResult(task Task) (Result, error) { return store.readResult(task, true) }

func (store *Store) readResult(task Task, verify bool) (Result, error) {
	result, err := store.readResultRows(task.ID)
	if err != nil {
		return Result{}, fmt.Errorf("load task result: %w", err)
	}
	if err := validateResult(result, task); err != nil {
		return Result{}, err
	}
	if !verify {
		return result, nil
	}
	for _, artifact := range result.Artifacts {
		file, err := store.openResultArtifact(task.ID, artifact)
		if err != nil {
			return Result{}, err
		}
		if err := file.Close(); err != nil {
			return Result{}, err
		}
	}
	return result, nil
}

func (store *Store) RecordDelivery(ownerID, taskID string, receipt DeliveryReceipt) error {
	return store.recordDelivery(ownerID, taskID, receipt, false)
}

// CompleteDelivery 将首次投递回执和请求终态原子提交，失败时两者都不前进。
func (store *Store) CompleteDelivery(ownerID, taskID string, receipt DeliveryReceipt) error {
	return store.recordDelivery(ownerID, taskID, receipt, true)
}

func (store *Store) recordDelivery(ownerID, taskID string, receipt DeliveryReceipt, complete bool) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	task, ok := store.findTaskLocked(strings.TrimSpace(ownerID), strings.TrimSpace(taskID))
	if !ok || task.ExecutionCompletedAt == 0 || task.ResultExpiresAt <= store.now().Unix() {
		return fmt.Errorf("task result is unavailable for delivery")
	}
	if complete && (task.State != StateDelivering || receipt.OperationID != "") {
		return fmt.Errorf("only the initial delivery can complete an active task")
	}
	result, err := store.readResult(task, false)
	if err != nil {
		return err
	}
	if receipt.Outcome == DeliveryPending || receipt.AttemptedAt == 0 {
		return fmt.Errorf("delivery receipt is incomplete")
	}
	if receipt.OperationID != "" {
		operation, err := uuid.Parse(receipt.OperationID)
		if err != nil {
			return err
		}
		receipt.OperationID = operation.String()
		found := false
		for index, attempt := range result.Attempts {
			if attempt.OperationID == receipt.OperationID {
				result.Attempts[index] = receipt
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("投递操作尚未登记")
		}
	} else if len(result.Attempts) == 0 {
		result.Attempts = append(result.Attempts, receipt)
	}
	result.Receipt = receipt
	if err := validateResult(result, task); err != nil {
		return err
	}
	if complete {
		_, err := store.transitionLocked(ownerID, taskID, StateSucceeded, terminalStage(StateSucceeded), "", func(tx *sql.Tx) error { return saveReceipts(tx, task.ID, result) })
		return err
	}
	if err := store.saveReceipts(task.ID, result); err != nil {
		return fmt.Errorf("persist delivery receipt: %w", err)
	}
	return nil
}

func validateResult(result Result, task Task) error {
	if len(result.Attempts) > 50 {
		return fmt.Errorf("delivery attempt limit exceeded")
	}
	if result.Version != resultVersion || len([]byte(result.Reply)) > maxResultReplyBytes || len(result.Artifacts) > maxResultArtifacts || len(result.ImageURLs) > maxResultURLs {
		return fmt.Errorf("invalid task result schema")
	}
	if result.ResponseMode != task.ResponseMode || result.VisualStyle != task.VisualStyle || result.FrozenAt < task.StartedAt || !result.Receipt.Outcome.valid() {
		return fmt.Errorf("task result does not match its task")
	}
	if strings.TrimSpace(result.Reply) == "" && len(result.Artifacts) == 0 && len(result.ImageURLs) == 0 {
		return fmt.Errorf("task result is empty")
	}
	seen := make(map[string]bool)
	var total int64
	for _, artifact := range result.Artifacts {
		if !validAttachmentName(artifact.Name) || artifact.Size <= 0 || artifact.Size > MaxFileBytes || !sha256Pattern.MatchString(artifact.SHA256) || !filepath.IsLocal(artifact.Path) || filepath.Clean(artifact.Path) != artifact.Path || !strings.HasPrefix(artifact.Path, "output"+string(filepath.Separator)) || seen[artifact.Path] {
			return fmt.Errorf("invalid result artifact")
		}
		seen[artifact.Path] = true
		total += artifact.Size
	}
	if total > MaxTaskBytes {
		return fmt.Errorf("result artifact total is invalid")
	}
	for _, rawURL := range result.ImageURLs {
		parsed, err := url.Parse(rawURL)
		if err != nil || len(rawURL) > 2048 || parsed.Host == "" || parsed.Scheme != "http" && parsed.Scheme != "https" {
			return fmt.Errorf("invalid result image URL")
		}
	}
	seenOperations := map[string]bool{}
	for _, receipt := range result.Attempts {
		if seenOperations[receipt.OperationID] {
			return fmt.Errorf("duplicate delivery operation")
		}
		seenOperations[receipt.OperationID] = true
		if receipt.Outcome == DeliveryPending {
			return fmt.Errorf("attempt cannot be pending")
		}
		if err := validateReceipt(receipt, result.FrozenAt); err != nil {
			return err
		}
	}
	return validateReceipt(result.Receipt, result.FrozenAt)
}

func validateReceipt(receipt DeliveryReceipt, frozenAt int64) error {
	if !receipt.Outcome.valid() {
		return fmt.Errorf("invalid delivery outcome")
	}
	if receipt.OperationID != "" {
		if _, err := uuid.Parse(receipt.OperationID); err != nil {
			return fmt.Errorf("invalid delivery operation")
		}
	}
	if receipt.AttemptedAt < 0 || receipt.MediaSent < 0 || receipt.MediaSent > maxResultArtifacts+maxResultURLs+16 || receipt.FailureCode != "" && !reasonPattern.MatchString(receipt.FailureCode) {
		return fmt.Errorf("invalid delivery receipt")
	}
	if receipt.Outcome == DeliveryPending {
		if receipt.AttemptedAt != 0 || receipt.MediaSent != 0 || receipt.TextSent || receipt.FailureCode != "" {
			return fmt.Errorf("invalid pending delivery receipt")
		}
	} else if receipt.AttemptedAt < frozenAt {
		return fmt.Errorf("invalid completed delivery receipt")
	}
	if receipt.Outcome == DeliverySucceeded && receipt.FailureCode != "" || receipt.Outcome != DeliverySucceeded && receipt.Outcome != DeliveryPending && receipt.FailureCode == "" {
		return fmt.Errorf("invalid delivery outcome metadata")
	}
	return nil
}

func rejectArtifactSymlinks(path string) error {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	if resolved != filepath.Clean(path) {
		return fmt.Errorf("result artifact contains a symbolic link")
	}
	return nil
}
func hashRegularFile(path string, expectedSize int64) (string, error) {
	if err := rejectArtifactSymlinks(path); err != nil {
		return "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open result artifact: %w", err)
	}
	hash, hashErr := hashFile(file, expectedSize)
	closeErr := file.Close()
	return hash, errors.Join(hashErr, closeErr)
}

func hashFile(file *os.File, expectedSize int64) (string, error) {
	if expectedSize <= 0 || expectedSize > MaxFileBytes {
		return "", fmt.Errorf("invalid result artifact size")
	}
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() != expectedSize {
		return "", fmt.Errorf("result artifact metadata changed")
	}
	hash := sha256.New()
	written, copyErr := io.Copy(hash, io.LimitReader(file, expectedSize+1))
	if copyErr != nil {
		return "", fmt.Errorf("hash result artifact: %w", copyErr)
	}
	if written != expectedSize {
		return "", fmt.Errorf("result artifact size changed")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// InspectResult 只读取回答和清单；下载校验目标文件，实际投递校验全部文件。
func (store *Store) InspectResult(ownerID, taskID string) (Result, error) {
	task, ok := store.Find(ownerID, taskID)
	if !ok || task.ExecutionCompletedAt == 0 || task.ResultExpiresAt <= store.now().Unix() {
		return Result{}, fmt.Errorf("结果已过期或尚未生成")
	}
	return store.readResult(task, false)
}

// SummarizeResult 只查询投递行，不加载回答正文和文件清单。
func (store *Store) SummarizeResult(ownerID, taskID string) (DeliveryReceipt, error) {
	store.mu.RLock()
	task, ok := store.findTaskLocked(ownerID, taskID)
	defer store.mu.RUnlock()
	if !ok || task.ExecutionCompletedAt == 0 || task.ResultExpiresAt <= store.now().Unix() {
		return DeliveryReceipt{}, fmt.Errorf("result unavailable")
	}
	return store.readReceipt(task.ID)
}
