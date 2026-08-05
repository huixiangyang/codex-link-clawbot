package taskqueue

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Store struct {
	mu        sync.RWMutex
	root      string
	indexPath string
	state     indexFile
	now       func() time.Time
}

func NewStore(root string) (*Store, error) {
	return newStore(root, time.Now)
}

func newStore(root string, now func() time.Time) (*Store, error) {
	if now == nil {
		now = time.Now
	}
	if strings.TrimSpace(root) == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve task queue root: %w", err)
		}
		root = filepath.Join(home, ".weclaw", "tasks")
	}
	root = filepath.Clean(root)
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("task queue root must be an absolute path")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create task queue root: %w", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, fmt.Errorf("protect task queue root: %w", err)
	}
	store := &Store{
		root: root, indexPath: filepath.Join(root, "index.json"),
		state: defaultIndex(), now: now,
	}
	data, err := os.ReadFile(store.indexPath)
	if errors.Is(err, os.ErrNotExist) {
		if err := store.saveLocked(); err != nil {
			return nil, fmt.Errorf("initialize task queue index: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("read task queue index: %w", err)
	} else {
		if err := os.Chmod(store.indexPath, 0o600); err != nil {
			return nil, fmt.Errorf("protect task queue index: %w", err)
		}
		if err := decodeStrictJSON(data, &store.state); err != nil {
			return nil, fmt.Errorf("decode task queue index: %w", err)
		}
		if err := validateIndex(store.state); err != nil {
			return nil, err
		}
	}
	changed, err := store.recoverLocked()
	if err != nil {
		return nil, err
	}
	if changed {
		if err := store.saveLocked(); err != nil {
			return nil, fmt.Errorf("persist recovered task queue: %w", err)
		}
	}
	return store, nil
}

func (store *Store) Root() string {
	return store.root
}

func (store *Store) Enqueue(input EnqueueInput) (Task, bool, error) {
	input.SourceMessageKey = strings.TrimSpace(input.SourceMessageKey)
	input.OwnerID = strings.TrimSpace(input.OwnerID)
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	input.ThreadID = strings.TrimSpace(input.ThreadID)
	input.Summary = strings.TrimSpace(input.Summary)
	input.RetryOf = strings.TrimSpace(input.RetryOf)
	if err := validateEnqueueInput(input); err != nil {
		return Task{}, false, err
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.cleanupExpiredPayloadsLocked(); err != nil {
		return Task{}, false, fmt.Errorf("clean expired task payloads: %w", err)
	}
	if existing, exists := store.findBySourceLocked(input.SourceMessageKey); exists {
		return existing, true, nil
	}
	owner := store.state.Owners[input.OwnerID]
	if countState(owner.Tasks, StateQueued) >= MaxQueuedPerOwner {
		return Task{}, false, fmt.Errorf("task queue already contains %d waiting tasks", MaxQueuedPerOwner)
	}
	payloadBytes := inputPayloadBytes(input)
	if store.payloadBytesLocked()+payloadBytes > MaxQueueBytes {
		return Task{}, false, fmt.Errorf("task queue attachment storage exceeds %d bytes", MaxQueueBytes)
	}
	taskID, err := newTaskID()
	if err != nil {
		return Task{}, false, err
	}
	if _, _, err := store.stagePayloadLocked(input, taskID); err != nil {
		return Task{}, false, err
	}
	now := store.now().Unix()
	if now <= 0 {
		now = 1
	}
	task := Task{
		ID: taskID, SourceMessageKey: input.SourceMessageKey,
		OwnerID: input.OwnerID, ProjectID: input.ProjectID, ThreadID: input.ThreadID,
		Summary: input.Summary, State: StateQueued, Stage: "等待执行",
		ResponseMode: input.ResponseMode, VisualStyle: input.VisualStyle,
		Order: store.state.NextOrder, CreatedAt: now, RetryOf: input.RetryOf,
		ImageCount: len(input.Images), FileCount: len(input.Files), PayloadBytes: payloadBytes,
	}
	previousOwner, ownerExisted := store.state.Owners[input.OwnerID]
	previousNextOrder := store.state.NextOrder
	owner.Tasks = append(owner.Tasks, task)
	store.state.Owners[input.OwnerID] = owner
	store.state.NextOrder++
	if err := store.saveLocked(); err != nil {
		if ownerExisted {
			store.state.Owners[input.OwnerID] = previousOwner
		} else {
			delete(store.state.Owners, input.OwnerID)
		}
		store.state.NextOrder = previousNextOrder
		_ = os.RemoveAll(store.taskPath(taskID))
		_ = syncDirectory(store.root)
		return Task{}, false, fmt.Errorf("persist queued task: %w", err)
	}
	return task, false, nil
}

func (store *Store) Find(ownerID, taskID string) (Task, bool) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	return store.findTaskLocked(strings.TrimSpace(ownerID), strings.TrimSpace(taskID))
}

func (store *Store) List(ownerID string) []Task {
	store.mu.RLock()
	owner := store.state.Owners[strings.TrimSpace(ownerID)]
	tasks := append([]Task(nil), owner.Tasks...)
	store.mu.RUnlock()
	sortTasksForDisplay(tasks)
	return tasks
}

func (store *Store) Status(ownerID string) OwnerStatus {
	store.mu.RLock()
	owner := store.state.Owners[strings.TrimSpace(ownerID)]
	store.mu.RUnlock()
	status := OwnerStatus{Paused: owner.Paused}
	for _, task := range owner.Tasks {
		switch task.State {
		case StateQueued:
			status.Queued++
		case StateRunning:
			status.Running++
		case StateDelivering:
			status.Delivering++
		case StateSucceeded:
			status.Succeeded++
		case StateFailed:
			status.Failed++
		case StateInterrupted:
			status.Interrupted++
		case StateCancelled:
			status.Cancelled++
		}
	}
	return status
}

func (store *Store) TotalPayloadBytes() int64 {
	store.mu.RLock()
	defer store.mu.RUnlock()
	return store.payloadBytesLocked()
}

func (store *Store) saveLocked() error {
	if err := validateIndex(store.state); err != nil {
		return err
	}
	data, err := json.MarshalIndent(store.state, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(store.root, ".index-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, store.indexPath); err != nil {
		return err
	}
	if err := os.Chmod(store.indexPath, 0o600); err != nil {
		return err
	}
	return syncDirectory(store.root)
}

func (store *Store) taskPath(taskID string) string {
	return filepath.Join(store.root, taskID)
}

func (store *Store) findTaskLocked(ownerID, taskID string) (Task, bool) {
	owner, exists := store.state.Owners[ownerID]
	if !exists {
		return Task{}, false
	}
	for _, task := range owner.Tasks {
		if task.ID == taskID {
			return task, true
		}
	}
	return Task{}, false
}

func (store *Store) findBySourceLocked(source string) (Task, bool) {
	for _, owner := range store.state.Owners {
		for _, task := range owner.Tasks {
			if task.SourceMessageKey == source {
				return task, true
			}
		}
	}
	return Task{}, false
}

func (store *Store) payloadBytesLocked() int64 {
	var total int64
	for _, owner := range store.state.Owners {
		for _, task := range owner.Tasks {
			if taskHasPayload(task, store.now().Unix()) {
				total += task.PayloadBytes
			}
		}
	}
	return total
}

func taskHasPayload(task Task, now int64) bool {
	if task.State == StateSucceeded || task.State == StateCancelled {
		return false
	}
	return task.PayloadExpiresAt == 0 || now < task.PayloadExpiresAt
}

func inputPayloadBytes(input EnqueueInput) int64 {
	var total int64
	for _, attachment := range input.Images {
		total += int64(len(attachment.Data))
	}
	for _, attachment := range input.Files {
		total += int64(len(attachment.Data))
	}
	return total
}

func countState(tasks []Task, state State) int {
	count := 0
	for _, task := range tasks {
		if task.State == state {
			count++
		}
	}
	return count
}

func newTaskID() (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate task id: %w", err)
	}
	return "task-" + hex.EncodeToString(random), nil
}

func sortTasksForDisplay(tasks []Task) {
	sort.SliceStable(tasks, func(left, right int) bool {
		leftActive := !tasks[left].State.Terminal()
		rightActive := !tasks[right].State.Terminal()
		if leftActive != rightActive {
			return leftActive
		}
		if leftActive {
			if tasks[left].State == StateRunning || tasks[left].State == StateDelivering {
				return true
			}
			if tasks[right].State == StateRunning || tasks[right].State == StateDelivering {
				return false
			}
			return tasks[left].Order < tasks[right].Order
		}
		return tasks[left].FinishedAt > tasks[right].FinishedAt
	})
}
