package request

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/statefile"
)

type Store struct {
	mu              sync.RWMutex
	root            string
	indexPath       string
	state           indexFile
	now             func() time.Time
	resultSummaries sync.Map // 只缓存列表所需回执，不缓存回答正文或文件内容。
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
			return nil, fmt.Errorf("resolve request root: %w", err)
		}
		root = filepath.Join(home, ".codex-link-clawbot", "tasks")
	}
	root = filepath.Clean(root)
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("request root must be an absolute path")
	}
	if err := statefile.EnsurePrivateDirectory(root); err != nil {
		return nil, fmt.Errorf("protect request root: %w", err)
	}
	store := &Store{
		root: root, indexPath: filepath.Join(root, "index.json"),
		state: defaultIndex(), now: now,
	}
	found, err := statefile.ReadJSON(store.indexPath, &store.state, statefile.Options{
		MaxBytes: 16 << 20,
		Validate: func() error { return validateIndex(store.state) },
	})
	if err != nil {
		return nil, fmt.Errorf("load request index (旧格式请运行 migrate-business): %w", err)
	}
	if !found {
		if err := store.saveLocked(); err != nil {
			return nil, fmt.Errorf("initialize request index: %w", err)
		}
	}
	changed, err := store.recoverLocked()
	if err != nil {
		return nil, err
	}
	if changed {
		if err := store.saveLocked(); err != nil {
			return nil, fmt.Errorf("persist recovered request: %w", err)
		}
	}
	return store, nil
}

func (store *Store) Root() string {
	return store.root
}

func (store *Store) Start(input StartInput) (Task, bool, error) {
	input.SourceMessageKey = strings.TrimSpace(input.SourceMessageKey)
	input.OwnerID = strings.TrimSpace(input.OwnerID)
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	input.ThreadID = strings.TrimSpace(input.ThreadID)
	input.Summary = strings.TrimSpace(input.Summary)
	input.RetryOf = strings.TrimSpace(input.RetryOf)
	if err := validateStartInput(input); err != nil {
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
	if _, cleared := store.state.Cleared[input.SourceMessageKey]; cleared {
		return Task{}, false, ErrCleared
	}
	owner := store.state.Owners[input.OwnerID]
	if len(owner.Tasks) >= MaxRecordsPerOwner {
		return Task{}, false, fmt.Errorf("%w：记录达到 %d 条上限", ErrCapacity, MaxRecordsPerOwner)
	}
	if _, rejected := store.state.Rejected[input.SourceMessageKey]; rejected {
		return Task{}, false, ErrRejected
	}
	for _, records := range store.state.Owners {
		for _, running := range records.Tasks {
			if !running.State.Terminal() && SessionKey(running.OwnerID, running.TargetID, running.ThreadID) == SessionKey(input.OwnerID, input.TargetID, input.ThreadID) {
				receipt := Rejection{Source: input.SourceMessageKey, OwnerID: input.OwnerID,
					TargetID: input.TargetID, ThreadID: running.ThreadID, TaskID: running.ID}
				if err := store.rejectLocked(receipt); err != nil {
					return Task{}, false, err
				}
				return Task{}, false, ErrSessionBusy
			}
		}
	}
	if store.resultBytesLocked()+maxResultReservation > MaxResultStoreBytes {
		return Task{}, false, fmt.Errorf("%w：结果可用空间不足，请先清理历史结果或等待当前执行结束", ErrCapacity)
	}
	payloadBytes := inputPayloadBytes(input)
	if store.payloadBytesLocked()+payloadBytes > MaxInputStoreBytes {
		return Task{}, false, fmt.Errorf("%w：输入存储达到 %d MiB 上限", ErrCapacity, MaxInputStoreBytes/(1<<20))
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
		InputPending: len(input.SourceData) > 0, TargetID: input.TargetID,
		ID: taskID, SourceMessageKey: input.SourceMessageKey,
		OwnerID: input.OwnerID, ProjectID: input.ProjectID, ThreadID: input.ThreadID,
		Summary: input.Summary, State: StateRunning, Stage: "准备执行",
		ResponseMode: input.ResponseMode, VisualStyle: input.VisualStyle,
		Order: store.state.NextOrder, CreatedAt: now, StartedAt: now, RetryOf: input.RetryOf,
		ImageCount: len(input.Images), FileCount: len(input.Files), PayloadBytes: payloadBytes,
	}
	if task.InputPending {
		task.Stage = "正在接收附件，可打断"
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
		return Task{}, false, fmt.Errorf("persist active request: %w", err)
	}
	return task, false, nil
}

func (store *Store) Find(ownerID, taskID string) (Task, bool) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	return store.findTaskLocked(strings.TrimSpace(ownerID), strings.TrimSpace(taskID))
}

func (store *Store) FindBySource(sourceMessageKey string) (Task, bool) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	return store.findBySourceLocked(strings.TrimSpace(sourceMessageKey))
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
	status := OwnerStatus{}
	for _, task := range owner.Tasks {
		switch task.State {
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

func (store *Store) ExecutionStatus() ExecutionStatus {
	store.mu.RLock()
	defer store.mu.RUnlock()
	var status ExecutionStatus
	for _, owner := range store.state.Owners {
		for _, task := range owner.Tasks {
			switch task.State {
			case StateRunning:
				status.Running++
			case StateDelivering:
				status.Delivering++
			}
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
	return statefile.WriteJSON(store.indexPath, store.state, statefile.Options{
		MaxBytes: 16 << 20,
		Validate: func() error { return validateIndex(store.state) },
	})
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
	return !task.State.Terminal() || task.PayloadExpiresAt > now
}

func inputPayloadBytes(input StartInput) int64 {
	total := int64(len(input.Text) + len(input.ContextToken) + len(input.SourceData))
	if len(input.SourceData) > 0 {
		total += MaxTaskBytes
	}
	for _, attachment := range input.Images {
		total += int64(len(attachment.Data))
	}
	for _, attachment := range input.Files {
		total += int64(len(attachment.Data))
	}
	return total
}

func newTaskID() (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate task id: %w", err)
	}
	return "task-" + hex.EncodeToString(random), nil
}

// 活动会话并列展示，按创建顺序稳定排序；历史按结束时间倒序。
func sortTasksForDisplay(tasks []Task) {
	sort.SliceStable(tasks, func(left, right int) bool {
		a, b := tasks[left], tasks[right]
		activeA, activeB := !a.State.Terminal(), !b.State.Terminal()
		if activeA != activeB {
			return activeA
		}
		if activeA {
			return a.Order < b.Order
		}
		if a.FinishedAt != b.FinishedAt {
			return a.FinishedAt > b.FinishedAt
		}
		return a.Order > b.Order
	})
}
