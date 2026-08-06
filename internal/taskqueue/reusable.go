package taskqueue

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/huixiangyang/weclaw/internal/statefile"
)

const (
	reusablePromptVersion = 1
	reusablePromptFile    = "reusable.json"
)

type reusablePrompt struct {
	Version int    `json:"version"`
	Text    string `json:"text"`
}

func reusablePromptEligible(input EnqueueInput) bool {
	return strings.TrimSpace(input.Text) != "" && len(input.Images) == 0 && len(input.Files) == 0
}

// LoadReusablePrompt 只返回仍在 24 小时保留期内的成功纯文字请求，不返回令牌、附件或回答。
func (store *Store) LoadReusablePrompt(ownerID, taskID string) (string, error) {
	store.mu.RLock()
	task, ok := store.findTaskLocked(strings.TrimSpace(ownerID), strings.TrimSpace(taskID))
	now := store.now().Unix()
	store.mu.RUnlock()
	if !ok || task.State != StateSucceeded || task.ImageCount != 0 || task.FileCount != 0 ||
		task.FinishedAt <= 0 || now >= task.FinishedAt+int64(payloadRetention.Seconds()) {
		return "", fmt.Errorf("reusable task prompt is unavailable")
	}
	path := filepath.Join(store.taskPath(task.ID), reusablePromptFile)
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("reusable task prompt is unavailable")
		}
		return "", fmt.Errorf("inspect reusable task prompt: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("reusable task prompt is invalid")
	}
	var prompt reusablePrompt
	found, err := statefile.ReadJSON(path, &prompt, statefile.Options{
		MaxBytes: 2 << 20,
		Validate: func() error {
			if prompt.Version != reusablePromptVersion || strings.TrimSpace(prompt.Text) == "" ||
				!utf8.ValidString(prompt.Text) || strings.ContainsRune(prompt.Text, '\x00') || len([]byte(prompt.Text)) > maxRequestTextBytes {
				return fmt.Errorf("invalid reusable task prompt")
			}
			return nil
		},
	})
	if err != nil {
		return "", fmt.Errorf("load reusable task prompt: %w", err)
	}
	if !found {
		return "", fmt.Errorf("reusable task prompt is unavailable")
	}
	return strings.TrimSpace(prompt.Text), nil
}

// cleanupCompletedTaskIDs 删除完整任务负载，只在成功纯文字任务仍可复用时保留 reusable.json。
func (store *Store) cleanupCompletedTaskIDs(taskIDs []string) {
	seen := make(map[string]struct{}, len(taskIDs))
	for _, taskID := range taskIDs {
		if !taskIDPattern.MatchString(taskID) {
			continue
		}
		if _, exists := seen[taskID]; exists {
			continue
		}
		seen[taskID] = struct{}{}
		store.cleanupCompletedTask(taskID)
	}
	if len(seen) > 0 {
		_ = syncDirectory(store.root)
	}
}

func (store *Store) cleanupCompletedTask(taskID string) {
	taskPath := store.taskPath(taskID)
	entries, err := os.ReadDir(taskPath)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		_ = os.RemoveAll(taskPath)
		return
	}
	reusablePath := filepath.Join(taskPath, reusablePromptFile)
	info, reusableErr := os.Lstat(reusablePath)
	if reusableErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		_ = os.RemoveAll(taskPath)
		return
	}
	for _, entry := range entries {
		if entry.Name() == reusablePromptFile {
			continue
		}
		if err := os.RemoveAll(filepath.Join(taskPath, entry.Name())); err != nil {
			_ = os.RemoveAll(taskPath)
			return
		}
	}
	if err := os.Chmod(taskPath, 0o700); err != nil {
		_ = os.RemoveAll(taskPath)
		return
	}
	if err := os.Chmod(reusablePath, 0o600); err != nil {
		_ = os.RemoveAll(taskPath)
		return
	}
	if err := syncDirectory(taskPath); err != nil {
		_ = os.RemoveAll(taskPath)
	}
}
