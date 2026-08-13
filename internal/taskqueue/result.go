package taskqueue

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/huixiangyang/codex-link-clawbot/internal/preference"
	"github.com/huixiangyang/codex-link-clawbot/internal/statefile"
	"github.com/huixiangyang/codex-link-clawbot/internal/visual"
)

const (
	resultVersion       = 1
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
	Outcome     DeliveryOutcome `json:"outcome"`
	AttemptedAt int64           `json:"attempted_at,omitempty"`
	MediaSent   int             `json:"media_sent,omitempty"`
	TextSent    bool            `json:"text_sent,omitempty"`
	FailureCode string          `json:"failure_code,omitempty"`
}

type Result struct {
	Version      int                     `json:"version"`
	Reply        string                  `json:"reply,omitempty"`
	Artifacts    []ResultArtifact        `json:"artifacts"`
	ImageURLs    []string                `json:"image_urls"`
	ResponseMode preference.ResponseMode `json:"response_mode"`
	VisualStyle  visual.Style            `json:"visual_style"`
	FrozenAt     int64                   `json:"frozen_at"`
	Receipt      DeliveryReceipt         `json:"receipt"`
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
	if !ok || task.State != StateRunning {
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
	resultPath := filepath.Join(store.taskPath(task.ID), "result.json")
	if _, err := os.Lstat(resultPath); err == nil {
		return Result{}, fmt.Errorf("task result is already frozen")
	} else if !errors.Is(err, os.ErrNotExist) {
		return Result{}, fmt.Errorf("inspect task result: %w", err)
	}
	if err := writeJSONSync(resultPath, result); err != nil {
		return Result{}, fmt.Errorf("freeze task result: %w", err)
	}
	if err := syncDirectory(store.taskPath(task.ID)); err != nil {
		return Result{}, fmt.Errorf("sync frozen task result: %w", err)
	}
	return result, nil
}

func (store *Store) freezeArtifact(task Task, absolutePath string) (ResultArtifact, error) {
	taskRoot := store.taskPath(task.ID)
	absolutePath = filepath.Clean(absolutePath)
	relativePath, err := filepath.Rel(taskRoot, absolutePath)
	if err != nil || !filepath.IsLocal(relativePath) || !strings.HasPrefix(relativePath, "outbox"+string(filepath.Separator)) {
		return ResultArtifact{}, fmt.Errorf("result artifact escaped its task outbox")
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
	return ResultArtifact{Name: filepath.Base(absolutePath), Path: relativePath, Size: info.Size(), SHA256: hash}, nil
}

func (store *Store) LoadResult(ownerID, taskID string) (Result, error) {
	store.mu.RLock()
	task, ok := store.findTaskLocked(strings.TrimSpace(ownerID), strings.TrimSpace(taskID))
	store.mu.RUnlock()
	if !ok || task.State == StateQueued || task.State == StateSucceeded || task.State == StateCancelled {
		return Result{}, fmt.Errorf("task result is unavailable")
	}
	if task.PayloadExpiresAt > 0 && store.now().Unix() >= task.PayloadExpiresAt {
		return Result{}, fmt.Errorf("task result is unavailable")
	}
	return store.loadResult(task)
}

func (store *Store) loadResult(task Task) (Result, error) {
	resultPath := filepath.Join(store.taskPath(task.ID), "result.json")
	var result Result
	found, err := statefile.ReadJSON(resultPath, &result, statefile.Options{
		MaxBytes: maxResultReplyBytes + 1<<20,
		Validate: func() error { return validateResult(result, task) },
	})
	if err != nil {
		return Result{}, fmt.Errorf("load task result: %w", err)
	}
	if !found {
		return Result{}, fmt.Errorf("task result file is missing")
	}
	for _, artifact := range result.Artifacts {
		absolutePath := filepath.Join(store.taskPath(task.ID), artifact.Path)
		info, err := os.Lstat(absolutePath)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() != artifact.Size {
			return Result{}, fmt.Errorf("result artifact metadata changed")
		}
		hash, err := hashRegularFile(absolutePath, artifact.Size)
		if err != nil {
			return Result{}, err
		}
		if hash != artifact.SHA256 {
			return Result{}, fmt.Errorf("result artifact checksum changed")
		}
	}
	return result, nil
}

func (store *Store) RecordDelivery(ownerID, taskID string, receipt DeliveryReceipt) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	task, ok := store.findTaskLocked(strings.TrimSpace(ownerID), strings.TrimSpace(taskID))
	if !ok || task.State != StateDelivering {
		return fmt.Errorf("only a delivering task can record delivery")
	}
	result, err := store.loadResult(task)
	if err != nil {
		return err
	}
	if receipt.Outcome == DeliveryPending || receipt.AttemptedAt == 0 {
		return fmt.Errorf("delivery receipt is incomplete")
	}
	result.Receipt = receipt
	if err := validateResult(result, task); err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(store.taskPath(task.ID), "result.json"), result); err != nil {
		return fmt.Errorf("persist delivery receipt: %w", err)
	}
	return nil
}

func validateResult(result Result, task Task) error {
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
		if !validAttachmentName(artifact.Name) || artifact.Size <= 0 || artifact.Size > MaxFileBytes || !sha256Pattern.MatchString(artifact.SHA256) || !filepath.IsLocal(artifact.Path) || filepath.Clean(artifact.Path) != artifact.Path || !strings.HasPrefix(artifact.Path, "outbox"+string(filepath.Separator)) || seen[artifact.Path] {
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
	receipt := result.Receipt
	if receipt.AttemptedAt < 0 || receipt.MediaSent < 0 || receipt.MediaSent > maxResultArtifacts+maxResultURLs+16 || receipt.FailureCode != "" && !reasonPattern.MatchString(receipt.FailureCode) {
		return fmt.Errorf("invalid delivery receipt")
	}
	if receipt.Outcome == DeliveryPending {
		if receipt.AttemptedAt != 0 || receipt.MediaSent != 0 || receipt.TextSent || receipt.FailureCode != "" {
			return fmt.Errorf("invalid pending delivery receipt")
		}
	} else if receipt.AttemptedAt < result.FrozenAt {
		return fmt.Errorf("invalid completed delivery receipt")
	}
	if receipt.Outcome == DeliverySucceeded && receipt.FailureCode != "" || receipt.Outcome != DeliverySucceeded && receipt.Outcome != DeliveryPending && receipt.FailureCode == "" {
		return fmt.Errorf("invalid delivery outcome metadata")
	}
	return nil
}

func hashRegularFile(path string, expectedSize int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open result artifact: %w", err)
	}
	hash := sha256.New()
	written, copyErr := io.Copy(hash, io.LimitReader(file, expectedSize+1))
	closeErr := file.Close()
	if copyErr != nil {
		return "", fmt.Errorf("hash result artifact: %w", copyErr)
	}
	if closeErr != nil {
		return "", closeErr
	}
	if written != expectedSize {
		return "", fmt.Errorf("result artifact size changed")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func writeJSONAtomic(path string, value any) error {
	return statefile.WriteJSON(path, value, statefile.Options{MaxBytes: maxResultReplyBytes + 1<<20})
}
