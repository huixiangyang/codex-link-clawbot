package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/huixiangyang/weclaw/internal/codex"
	"github.com/huixiangyang/weclaw/internal/statefile"
)

const (
	indexVersion     = 3
	DefaultProjectID = "default"
)

var (
	ErrNotOwned      = errors.New("session does not belong to this user")
	ErrNoActive      = errors.New("no active session")
	ErrAmbiguousCode = errors.New("session code is ambiguous")
)

type indexFile struct {
	Version int                   `json:"version"`
	Owners  map[string]*ownerData `json:"owners"`
}

type ownerData struct {
	ActiveThreads map[string]string         `json:"active_threads"`
	Threads       map[string]*trackedThread `json:"threads"`
}

type trackedThread struct {
	ID             string `json:"id"`
	ForkedFromID   string `json:"forked_from_id,omitempty"`
	ProjectID      string `json:"project_id"`
	Archived       bool   `json:"archived"`
	CreatedAt      int64  `json:"created_at"`
	UpdatedAt      int64  `json:"updated_at"`
	LastSelectedAt int64  `json:"last_selected_at"`
	Model          string `json:"model,omitempty"`
	Effort         string `json:"effort,omitempty"`
}

// Record 是会话管理器可读取的归属记录，不包含聊天正文。
type Record struct {
	ID             string
	ForkedFromID   string
	ProjectID      string
	Archived       bool
	CreatedAt      int64
	UpdatedAt      int64
	LastSelectedAt int64
	Model          string
	Effort         string
}

type ThreadSettings struct {
	Model  string
	Effort string
}

// Store 以原子文件替换持久化微信用户与 Codex 线程的归属关系。
type Store struct {
	mu    sync.RWMutex
	path  string
	index indexFile
}

func DefaultIndexPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".weclaw", "session-index.json"), nil
}

func OpenStore(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		resolved, err := DefaultIndexPath()
		if err != nil {
			return nil, err
		}
		path = resolved
	}
	store := &Store{
		path: path,
		index: indexFile{
			Version: indexVersion,
			Owners:  make(map[string]*ownerData),
		},
	}
	var decoded indexFile
	found, err := statefile.ReadJSON(path, &decoded, statefile.Options{
		MaxBytes: 8 << 20,
		Validate: func() error { return validateIndex(decoded) },
	})
	if err != nil {
		return nil, fmt.Errorf("load session index: %w", err)
	}
	if !found {
		return store, nil
	}
	store.index = decoded
	return store, nil
}

func validateIndex(index indexFile) error {
	if index.Version != indexVersion {
		return fmt.Errorf("unsupported session index version %d", index.Version)
	}
	if index.Owners == nil {
		return fmt.Errorf("session index owners are missing")
	}
	for ownerID, owner := range index.Owners {
		if strings.TrimSpace(ownerID) == "" || owner == nil || owner.Threads == nil || owner.ActiveThreads == nil {
			return fmt.Errorf("invalid session owner %q", ownerID)
		}
		for projectID, threadID := range owner.ActiveThreads {
			if strings.TrimSpace(projectID) == "" || strings.TrimSpace(threadID) == "" {
				return fmt.Errorf("invalid active project session")
			}
			thread, ok := owner.Threads[threadID]
			if !ok || thread == nil || thread.Archived || thread.ProjectID != projectID {
				return fmt.Errorf("active thread %q is missing, archived, or belongs to another project", threadID)
			}
		}
		for id, thread := range owner.Threads {
			if thread == nil || id == "" || thread.ID != id || strings.TrimSpace(thread.ProjectID) == "" {
				return fmt.Errorf("invalid thread record %q", id)
			}
			if !validThreadSetting(thread.Model, 128) || !validThreadSetting(thread.Effort, 32) {
				return fmt.Errorf("invalid thread settings %q", id)
			}
		}
	}
	return nil
}

func (s *Store) Active(ownerID string) (string, bool) {
	return s.ActiveForProject(ownerID, DefaultProjectID)
}

func (s *Store) ActiveForProject(ownerID, projectID string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state := findOwner(s.index, ownerID)
	if state == nil || state.ActiveThreads[projectID] == "" {
		return "", false
	}
	return state.ActiveThreads[projectID], true
}

func (s *Store) Counts(ownerID string) (active, archived int, currentID string, hasCurrent bool) {
	return s.CountsForProject(ownerID, DefaultProjectID)
}

func (s *Store) CountsForProject(ownerID, projectID string) (active, archived int, currentID string, hasCurrent bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state := findOwner(s.index, ownerID)
	if state == nil {
		return 0, 0, "", false
	}
	for _, thread := range state.Threads {
		if thread.ProjectID != projectID {
			continue
		}
		if thread.Archived {
			archived++
		} else {
			active++
		}
	}
	currentID = state.ActiveThreads[projectID]
	return active, archived, currentID, currentID != ""
}

func (s *Store) Owns(ownerID, threadID string) bool {
	return s.OwnsProject(ownerID, DefaultProjectID, threadID)
}

func (s *Store) OwnsProject(ownerID, projectID, threadID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state := findOwner(s.index, ownerID)
	if state == nil {
		return false
	}
	thread, ok := state.Threads[threadID]
	return ok && thread.ProjectID == projectID
}

func (s *Store) Records(ownerID string, archived bool) []Record {
	return s.RecordsForProject(ownerID, DefaultProjectID, archived)
}

func (s *Store) RecordsForProject(ownerID, projectID string, archived bool) []Record {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state := findOwner(s.index, ownerID)
	if state == nil {
		return nil
	}
	records := make([]Record, 0, len(state.Threads))
	for _, thread := range state.Threads {
		if thread.ProjectID != projectID || thread.Archived != archived {
			continue
		}
		records = append(records, Record{
			ID: thread.ID, ForkedFromID: thread.ForkedFromID, ProjectID: thread.ProjectID, Archived: thread.Archived,
			CreatedAt: thread.CreatedAt, UpdatedAt: thread.UpdatedAt,
			LastSelectedAt: thread.LastSelectedAt,
			Model:          thread.Model, Effort: thread.Effort,
		})
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].UpdatedAt == records[j].UpdatedAt {
			return records[i].CreatedAt > records[j].CreatedAt
		}
		return records[i].UpdatedAt > records[j].UpdatedAt
	})
	return records
}

func (s *Store) SettingsForProject(ownerID, projectID, threadID string) (ThreadSettings, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state := findOwner(s.index, ownerID)
	if state == nil {
		return ThreadSettings{}, ErrNotOwned
	}
	thread := state.Threads[threadID]
	if thread == nil || thread.ProjectID != projectID || thread.Archived {
		return ThreadSettings{}, ErrNotOwned
	}
	return ThreadSettings{Model: thread.Model, Effort: thread.Effort}, nil
}

func (s *Store) SetSettingsForProject(ownerID, projectID, threadID string, settings ThreadSettings) error {
	settings.Model = strings.TrimSpace(settings.Model)
	settings.Effort = strings.TrimSpace(settings.Effort)
	if settings.Model == "" || !validThreadSetting(settings.Model, 128) || !validThreadSetting(settings.Effort, 32) {
		return fmt.Errorf("invalid thread settings")
	}
	return s.mutate(func(index *indexFile) error {
		state := findOwner(*index, ownerID)
		if state == nil {
			return ErrNotOwned
		}
		thread := state.Threads[threadID]
		if thread == nil || thread.ProjectID != projectID || thread.Archived {
			return ErrNotOwned
		}
		thread.Model = settings.Model
		thread.Effort = settings.Effort
		return nil
	})
}

func (s *Store) DeleteForProject(ownerID, projectID, threadID, nextActive string) error {
	return s.mutate(func(index *indexFile) error {
		state := findOwner(*index, ownerID)
		if state == nil {
			return ErrNotOwned
		}
		thread := state.Threads[threadID]
		if thread == nil || thread.ProjectID != projectID {
			return ErrNotOwned
		}
		if nextActive != "" {
			next := state.Threads[nextActive]
			if next == nil || next.ProjectID != projectID || next.Archived || nextActive == threadID {
				return ErrNotOwned
			}
		}
		deleted := map[string]bool{threadID: true}
		for changed := true; changed; {
			changed = false
			for id, candidate := range state.Threads {
				if !deleted[id] && deleted[candidate.ForkedFromID] {
					deleted[id] = true
					changed = true
				}
			}
		}
		for id := range deleted {
			delete(state.Threads, id)
		}
		if state.ActiveThreads[projectID] == threadID {
			if nextActive == "" {
				delete(state.ActiveThreads, projectID)
			} else {
				state.ActiveThreads[projectID] = nextActive
			}
		}
		for activeProject, activeThread := range state.ActiveThreads {
			if deleted[activeThread] {
				delete(state.ActiveThreads, activeProject)
			}
		}
		return nil
	})
}

func (s *Store) Resolve(ownerID, reference string, archived bool) (Record, error) {
	return s.ResolveForProject(ownerID, DefaultProjectID, reference, archived)
}

func (s *Store) ResolveForProject(ownerID, projectID, reference string, archived bool) (Record, error) {
	reference = strings.TrimSpace(reference)
	if len(reference) < 6 {
		return Record{}, fmt.Errorf("session code must contain at least 6 characters")
	}
	var matches []Record
	for _, record := range s.RecordsForProject(ownerID, projectID, archived) {
		if record.ID == reference || strings.HasSuffix(record.ID, reference) {
			matches = append(matches, record)
		}
	}
	if len(matches) == 0 {
		return Record{}, ErrNotOwned
	}
	if len(matches) > 1 {
		return Record{}, ErrAmbiguousCode
	}
	return matches[0], nil
}

func (s *Store) Register(ownerID string, thread codex.ThreadInfo, makeActive bool, now time.Time) error {
	return s.RegisterProject(ownerID, DefaultProjectID, thread, makeActive, now)
}

func (s *Store) RegisterProject(ownerID, projectID string, thread codex.ThreadInfo, makeActive bool, now time.Time) error {
	if strings.TrimSpace(ownerID) == "" || strings.TrimSpace(thread.ID) == "" {
		return fmt.Errorf("owner and thread id are required")
	}
	if strings.TrimSpace(projectID) == "" {
		return fmt.Errorf("project id is required")
	}
	return s.mutate(func(index *indexFile) error {
		state := ensureOwner(index, ownerID)
		record := state.Threads[thread.ID]
		if record == nil {
			record = &trackedThread{ID: thread.ID, ProjectID: projectID}
			state.Threads[thread.ID] = record
		}
		if record.ProjectID != projectID {
			return ErrNotOwned
		}
		if thread.ForkedFromID != "" {
			record.ForkedFromID = thread.ForkedFromID
		}
		record.Archived = false
		if thread.CreatedAt > 0 {
			record.CreatedAt = thread.CreatedAt
		} else if record.CreatedAt == 0 {
			record.CreatedAt = now.Unix()
		}
		if thread.UpdatedAt > 0 {
			record.UpdatedAt = thread.UpdatedAt
		} else {
			record.UpdatedAt = now.Unix()
		}
		if makeActive {
			state.ActiveThreads[projectID] = thread.ID
			record.LastSelectedAt = now.Unix()
		}
		return nil
	})
}

func (s *Store) SetActive(ownerID, threadID string, now time.Time) error {
	return s.SetActiveForProject(ownerID, DefaultProjectID, threadID, now)
}

func (s *Store) SetActiveForProject(ownerID, projectID, threadID string, now time.Time) error {
	return s.mutate(func(index *indexFile) error {
		state := findOwner(*index, ownerID)
		if state == nil {
			return ErrNotOwned
		}
		thread, ok := state.Threads[threadID]
		if !ok || thread.Archived || thread.ProjectID != projectID {
			return ErrNotOwned
		}
		state.ActiveThreads[projectID] = threadID
		thread.LastSelectedAt = now.Unix()
		thread.UpdatedAt = maxInt64(thread.UpdatedAt, now.Unix())
		return nil
	})
}

func (s *Store) MarkArchived(ownerID, threadID, nextActive string, archived bool, now time.Time) error {
	return s.MarkArchivedForProject(ownerID, DefaultProjectID, threadID, nextActive, archived, now)
}

func (s *Store) MarkArchivedForProject(ownerID, projectID, threadID, nextActive string, archived bool, now time.Time) error {
	return s.mutate(func(index *indexFile) error {
		state := findOwner(*index, ownerID)
		if state == nil {
			return ErrNotOwned
		}
		thread, ok := state.Threads[threadID]
		if !ok || thread.ProjectID != projectID {
			return ErrNotOwned
		}
		thread.Archived = archived
		thread.UpdatedAt = maxInt64(thread.UpdatedAt, now.Unix())
		if nextActive != "" {
			next, ok := state.Threads[nextActive]
			if !ok || next.ProjectID != projectID || next.Archived {
				return ErrNotOwned
			}
		}
		if archived && state.ActiveThreads[projectID] == threadID {
			if nextActive == "" {
				delete(state.ActiveThreads, projectID)
			} else {
				state.ActiveThreads[projectID] = nextActive
			}
		}
		if !archived && state.ActiveThreads[projectID] == "" && nextActive == threadID {
			state.ActiveThreads[projectID] = threadID
			thread.LastSelectedAt = now.Unix()
		}
		return nil
	})
}

func (s *Store) Touch(ownerID, threadID string, updatedAt int64) error {
	return s.mutate(func(index *indexFile) error {
		state := findOwner(*index, ownerID)
		if state == nil {
			return ErrNotOwned
		}
		thread, ok := state.Threads[threadID]
		if !ok {
			return ErrNotOwned
		}
		thread.UpdatedAt = maxInt64(thread.UpdatedAt, updatedAt)
		return nil
	})
}

func (s *Store) mutate(change func(*indexFile) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next, err := cloneIndex(s.index)
	if err != nil {
		return err
	}
	if err := change(&next); err != nil {
		return err
	}
	if err := validateIndex(next); err != nil {
		return err
	}
	if err := s.save(next); err != nil {
		return err
	}
	s.index = next
	return nil
}

func (s *Store) save(index indexFile) error {
	return statefile.WriteJSON(s.path, index, statefile.Options{
		MaxBytes: 8 << 20,
		Validate: func() error { return validateIndex(index) },
	})
}

func cloneIndex(index indexFile) (indexFile, error) {
	data, err := json.Marshal(index)
	if err != nil {
		return indexFile{}, fmt.Errorf("clone session index: %w", err)
	}
	var cloned indexFile
	if err := json.Unmarshal(data, &cloned); err != nil {
		return indexFile{}, fmt.Errorf("clone session index: %w", err)
	}
	return cloned, nil
}

func ensureOwner(index *indexFile, ownerID string) *ownerData {
	owner := index.Owners[ownerID]
	if owner == nil {
		owner = &ownerData{ActiveThreads: make(map[string]string), Threads: make(map[string]*trackedThread)}
		index.Owners[ownerID] = owner
	}
	return owner
}

func findOwner(index indexFile, ownerID string) *ownerData {
	return index.Owners[ownerID]
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func validThreadSetting(value string, limit int) bool {
	return len([]rune(value)) <= limit && !strings.ContainsAny(value, "\r\n\x00")
}
