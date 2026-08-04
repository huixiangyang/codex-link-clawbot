package messaging

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var (
	activityURLPattern         = regexp.MustCompile(`(?i)https?://[^\s，。！？；;]+`)
	activityUnixPathPattern    = regexp.MustCompile(`/[^\s，。！？；;]+`)
	activityWindowsPathPattern = regexp.MustCompile(`(?i)[a-z]:[\\/][^\s，。！？；;]+`)
)

const (
	activityStoreVersion = 1
	activityHistoryLimit = 20
)

type ActivityStatus string

const (
	ActivityRunning     ActivityStatus = "running"
	ActivitySucceeded   ActivityStatus = "succeeded"
	ActivityFailed      ActivityStatus = "failed"
	ActivityCancelled   ActivityStatus = "cancelled"
	ActivityInterrupted ActivityStatus = "interrupted"
)

type ActivityRecord struct {
	ID         string         `json:"id"`
	Summary    string         `json:"summary"`
	Status     ActivityStatus `json:"status"`
	StartedAt  int64          `json:"started_at"`
	FinishedAt int64          `json:"finished_at,omitempty"`
}

type activityFile struct {
	Version int                         `json:"version"`
	Owners  map[string][]ActivityRecord `json:"owners"`
}

// ActivityStore 只持久化任务管理元数据，不保存对话正文、输出或私有路径。
type ActivityStore struct {
	mu    sync.RWMutex
	path  string
	state activityFile
	now   func() time.Time
}

func NewActivityStore(path string) (*ActivityStore, error) {
	if strings.TrimSpace(path) == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve task history path: %w", err)
		}
		path = filepath.Join(home, ".weclaw", "task-history.json")
	}
	store := &ActivityStore{
		path: filepath.Clean(path), now: time.Now,
		state: activityFile{Version: activityStoreVersion, Owners: make(map[string][]ActivityRecord)},
	}
	if err := os.MkdirAll(filepath.Dir(store.path), 0o700); err != nil {
		return nil, fmt.Errorf("create task history directory: %w", err)
	}
	if err := os.Chmod(filepath.Dir(store.path), 0o700); err != nil {
		return nil, fmt.Errorf("protect task history directory: %w", err)
	}
	data, err := os.ReadFile(store.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read task history: %w", err)
	}
	needsSave := errors.Is(err, os.ErrNotExist)
	if err == nil {
		if err := os.Chmod(store.path, 0o600); err != nil {
			return nil, fmt.Errorf("protect task history: %w", err)
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&store.state); err != nil {
			return nil, fmt.Errorf("decode task history: %w", err)
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("decode task history: trailing data")
		}
		if err := validateActivityFile(store.state); err != nil {
			return nil, err
		}
	}
	if store.interruptRunning(store.now()) {
		needsSave = true
	}
	if needsSave {
		if err := store.saveLocked(); err != nil {
			return nil, fmt.Errorf("initialize task history: %w", err)
		}
	}
	return store, nil
}

func (s *ActivityStore) Start(ownerID, summary string) (string, error) {
	ownerID = strings.TrimSpace(ownerID)
	summary = normalizeSessionLine(summary, 120)
	if ownerID == "" || summary == "" {
		return "", fmt.Errorf("task owner and summary are required")
	}
	now := s.now()
	id, err := newActivityID(now)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := append([]ActivityRecord(nil), s.state.Owners[ownerID]...)
	records := append([]ActivityRecord{{
		ID: id, Summary: summary, Status: ActivityRunning, StartedAt: now.Unix(),
	}}, previous...)
	if len(records) > activityHistoryLimit {
		records = records[:activityHistoryLimit]
	}
	s.state.Owners[ownerID] = records
	if err := s.saveLocked(); err != nil {
		s.state.Owners[ownerID] = previous
		return "", err
	}
	return id, nil
}

func (s *ActivityStore) Finish(ownerID, id string, status ActivityStatus) error {
	if status != ActivitySucceeded && status != ActivityFailed && status != ActivityCancelled {
		return fmt.Errorf("invalid final task status %q", status)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	records := s.state.Owners[ownerID]
	for index := range records {
		if records[index].ID != id {
			continue
		}
		previous := records[index]
		records[index].Status = status
		finishedAt := s.now().Unix()
		if finishedAt < records[index].StartedAt {
			finishedAt = records[index].StartedAt
		}
		records[index].FinishedAt = finishedAt
		s.state.Owners[ownerID] = records
		if err := s.saveLocked(); err != nil {
			records[index] = previous
			s.state.Owners[ownerID] = records
			return err
		}
		return nil
	}
	return fmt.Errorf("task activity %q not found", id)
}

func (s *ActivityStore) List(ownerID string) []ActivityRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]ActivityRecord(nil), s.state.Owners[ownerID]...)
}

func (s *ActivityStore) Find(ownerID, id string) (ActivityRecord, bool) {
	for _, record := range s.List(ownerID) {
		if record.ID == id {
			return record, true
		}
	}
	return ActivityRecord{}, false
}

func (s *ActivityStore) interruptRunning(now time.Time) bool {
	changed := false
	for ownerID, records := range s.state.Owners {
		for index := range records {
			if records[index].Status != ActivityRunning {
				continue
			}
			records[index].Status = ActivityInterrupted
			records[index].FinishedAt = now.Unix()
			if records[index].FinishedAt < records[index].StartedAt {
				records[index].FinishedAt = records[index].StartedAt
			}
			changed = true
		}
		s.state.Owners[ownerID] = records
	}
	return changed
}

func (s *ActivityStore) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(s.path), ".task-history-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, s.path)
}

func validateActivityFile(state activityFile) error {
	if state.Version != activityStoreVersion || state.Owners == nil {
		return fmt.Errorf("invalid task history schema")
	}
	for ownerID, records := range state.Owners {
		if strings.TrimSpace(ownerID) == "" || len(records) > activityHistoryLimit {
			return fmt.Errorf("invalid task history owner")
		}
		seen := make(map[string]bool, len(records))
		for _, record := range records {
			if record.ID == "" || record.Summary == "" || record.StartedAt <= 0 || seen[record.ID] {
				return fmt.Errorf("invalid task history record")
			}
			seen[record.ID] = true
			switch record.Status {
			case ActivityRunning:
				if record.FinishedAt != 0 {
					return fmt.Errorf("running task has a finish time")
				}
			case ActivitySucceeded, ActivityFailed, ActivityCancelled, ActivityInterrupted:
				if record.FinishedAt < record.StartedAt {
					return fmt.Errorf("finished task has an invalid time")
				}
			default:
				return fmt.Errorf("invalid task history status")
			}
		}
	}
	return nil
}

func newActivityID(now time.Time) (string, error) {
	random := make([]byte, 6)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate task activity id: %w", err)
	}
	return fmt.Sprintf("%d-%s", now.UnixMilli(), hex.EncodeToString(random)), nil
}
