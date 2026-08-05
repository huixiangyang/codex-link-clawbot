package taskqueue

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/huixiangyang/weclaw/statefile"
)

func (store *Store) stagePayloadLocked(input EnqueueInput, taskID string) (Request, int64, error) {
	stagingPath, err := os.MkdirTemp(store.root, ".staging-")
	if err != nil {
		return Request{}, 0, fmt.Errorf("create task staging directory: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(stagingPath)
		}
	}()
	if err := os.Chmod(stagingPath, 0o700); err != nil {
		return Request{}, 0, fmt.Errorf("protect task staging directory: %w", err)
	}
	inboxPath := filepath.Join(stagingPath, "inbox")
	if err := os.Mkdir(inboxPath, 0o700); err != nil {
		return Request{}, 0, fmt.Errorf("create task inbox: %w", err)
	}
	if err := os.Chmod(inboxPath, 0o700); err != nil {
		return Request{}, 0, fmt.Errorf("protect task inbox: %w", err)
	}

	request := Request{
		Version: requestVersion, SourceMessageKey: input.SourceMessageKey,
		Text: input.Text, ContextToken: input.ContextToken,
		Images: make([]Attachment, 0, len(input.Images)), Files: make([]Attachment, 0, len(input.Files)),
	}
	var payloadBytes int64
	for index, attachment := range input.Images {
		stored, writeErr := writeInputAttachment(inboxPath, "image", index+1, attachment)
		if writeErr != nil {
			return Request{}, 0, fmt.Errorf("store image %d: %w", index+1, writeErr)
		}
		request.Images = append(request.Images, stored)
		payloadBytes += stored.Size
	}
	for index, attachment := range input.Files {
		stored, writeErr := writeInputAttachment(inboxPath, "file", index+1, attachment)
		if writeErr != nil {
			return Request{}, 0, fmt.Errorf("store file %d: %w", index+1, writeErr)
		}
		request.Files = append(request.Files, stored)
		payloadBytes += stored.Size
	}
	if err := writeJSONSync(filepath.Join(stagingPath, "request.json"), request); err != nil {
		return Request{}, 0, fmt.Errorf("write task request: %w", err)
	}
	if err := syncDirectory(inboxPath); err != nil {
		return Request{}, 0, fmt.Errorf("sync task inbox: %w", err)
	}
	if err := syncDirectory(stagingPath); err != nil {
		return Request{}, 0, fmt.Errorf("sync task staging directory: %w", err)
	}
	finalPath := store.taskPath(taskID)
	if err := os.Rename(stagingPath, finalPath); err != nil {
		return Request{}, 0, fmt.Errorf("commit task payload: %w", err)
	}
	committed = true
	if err := syncDirectory(store.root); err != nil {
		return Request{}, 0, fmt.Errorf("sync task root: %w", err)
	}
	return request, payloadBytes, nil
}

func writeInputAttachment(inboxPath, prefix string, index int, input InputAttachment) (Attachment, error) {
	name := fmt.Sprintf("%s-%02d%s", prefix, index, storedExtension(input.Name))
	relativePath := filepath.Join("inbox", name)
	absolutePath := filepath.Join(inboxPath, name)
	if err := writeFileSync(absolutePath, input.Data); err != nil {
		return Attachment{}, err
	}
	hash := sha256.Sum256(input.Data)
	return Attachment{
		Name: input.Name, Path: relativePath, ContentType: input.ContentType,
		Size: int64(len(input.Data)), SHA256: hex.EncodeToString(hash[:]),
	}, nil
}

func writeJSONSync(path string, value any) error {
	return statefile.WriteJSON(path, value, statefile.Options{MaxBytes: 8 << 20, CreateOnly: true})
}

func writeFileSync(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	closeWithError := func(cause error) error {
		if closeErr := file.Close(); cause == nil {
			return closeErr
		}
		return cause
	}
	if err := file.Chmod(0o600); err != nil {
		return closeWithError(err)
	}
	if _, err := file.Write(data); err != nil {
		return closeWithError(err)
	}
	if err := file.Sync(); err != nil {
		return closeWithError(err)
	}
	return closeWithError(nil)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func (store *Store) LoadRequest(ownerID, taskID string) (LoadedRequest, error) {
	store.mu.RLock()
	task, ok := store.findTaskLocked(ownerID, taskID)
	store.mu.RUnlock()
	if !ok {
		return LoadedRequest{}, fmt.Errorf("task not found")
	}
	if task.State == StateSucceeded || task.State == StateCancelled || task.PayloadExpiresAt > 0 && store.now().Unix() >= task.PayloadExpiresAt {
		return LoadedRequest{}, fmt.Errorf("task payload is unavailable")
	}
	requestPath := filepath.Join(store.taskPath(taskID), "request.json")
	var request Request
	found, err := statefile.ReadJSON(requestPath, &request, statefile.Options{
		MaxBytes: 2 << 20,
		Validate: func() error { return validateRequest(request) },
	})
	if err != nil {
		return LoadedRequest{}, fmt.Errorf("load task request: %w", err)
	}
	if !found {
		return LoadedRequest{}, fmt.Errorf("task request file is missing")
	}
	if request.SourceMessageKey != task.SourceMessageKey || len(request.Images) != task.ImageCount || len(request.Files) != task.FileCount {
		return LoadedRequest{}, fmt.Errorf("task request does not match its index")
	}
	loaded := LoadedRequest{
		SourceMessageKey: request.SourceMessageKey,
		Text:             request.Text, ContextToken: request.ContextToken,
		Images: make([]LoadedAttachment, 0, len(request.Images)), Files: make([]LoadedAttachment, 0, len(request.Files)),
	}
	var total int64
	for _, attachment := range request.Images {
		resolved, resolveErr := store.verifyAttachment(taskID, attachment)
		if resolveErr != nil {
			return LoadedRequest{}, resolveErr
		}
		loaded.Images = append(loaded.Images, LoadedAttachment{Attachment: attachment, AbsolutePath: resolved})
		total += attachment.Size
	}
	for _, attachment := range request.Files {
		resolved, resolveErr := store.verifyAttachment(taskID, attachment)
		if resolveErr != nil {
			return LoadedRequest{}, resolveErr
		}
		loaded.Files = append(loaded.Files, LoadedAttachment{Attachment: attachment, AbsolutePath: resolved})
		total += attachment.Size
	}
	if total != task.PayloadBytes {
		return LoadedRequest{}, fmt.Errorf("task payload size does not match its index")
	}
	return loaded, nil
}

// PrepareOutbox 创建本次任务唯一的私有交付目录，Codex 只能向该目录写入最终文件。
func (store *Store) PrepareOutbox(ownerID, taskID string) (string, error) {
	store.mu.RLock()
	task, ok := store.findTaskLocked(strings.TrimSpace(ownerID), strings.TrimSpace(taskID))
	store.mu.RUnlock()
	if !ok || task.State != StateRunning {
		return "", fmt.Errorf("only a running task can prepare an outbox")
	}
	outboxPath := filepath.Join(store.taskPath(task.ID), "outbox")
	info, err := os.Lstat(outboxPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := os.Mkdir(outboxPath, 0o700); err != nil {
			return "", fmt.Errorf("create task outbox: %w", err)
		}
	case err != nil:
		return "", fmt.Errorf("inspect task outbox: %w", err)
	case !info.IsDir() || info.Mode()&os.ModeSymlink != 0:
		return "", fmt.Errorf("task outbox is invalid")
	}
	if err := os.Chmod(outboxPath, 0o700); err != nil {
		return "", fmt.Errorf("protect task outbox: %w", err)
	}
	if err := syncDirectory(store.taskPath(task.ID)); err != nil {
		return "", fmt.Errorf("sync task outbox: %w", err)
	}
	return outboxPath, nil
}

func (store *Store) verifyAttachment(taskID string, attachment Attachment) (string, error) {
	taskRoot := store.taskPath(taskID)
	absolutePath := filepath.Join(taskRoot, attachment.Path)
	relative, err := filepath.Rel(taskRoot, absolutePath)
	if err != nil || !filepath.IsLocal(relative) || relative != attachment.Path {
		return "", fmt.Errorf("task attachment escaped its private directory")
	}
	info, err := os.Lstat(absolutePath)
	if err != nil {
		return "", fmt.Errorf("inspect task attachment: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() != attachment.Size {
		return "", fmt.Errorf("task attachment metadata changed")
	}
	if err := os.Chmod(absolutePath, 0o600); err != nil {
		return "", fmt.Errorf("protect task attachment: %w", err)
	}
	file, err := os.Open(absolutePath)
	if err != nil {
		return "", fmt.Errorf("open task attachment: %w", err)
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, io.LimitReader(file, attachment.Size+1))
	closeErr := file.Close()
	if copyErr != nil {
		return "", fmt.Errorf("hash task attachment: %w", copyErr)
	}
	if closeErr != nil {
		return "", closeErr
	}
	if hex.EncodeToString(hash.Sum(nil)) != attachment.SHA256 {
		return "", fmt.Errorf("task attachment checksum changed")
	}
	return absolutePath, nil
}
