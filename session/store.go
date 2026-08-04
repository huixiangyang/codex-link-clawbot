package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/huixiangyang/weclaw/codex"
)

const indexVersion = 2

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
	ActiveThreadID string                    `json:"active_thread_id,omitempty"`
	Threads        map[string]*trackedThread `json:"threads"`
}

type trackedThread struct {
	ID             string `json:"id"`
	Archived       bool   `json:"archived"`
	CreatedAt      int64  `json:"created_at"`
	UpdatedAt      int64  `json:"updated_at"`
	LastSelectedAt int64  `json:"last_selected_at"`
}

// Record 是会话管理器可读取的归属记录，不包含聊天正文。
type Record struct {
	ID             string
	Archived       bool
	CreatedAt      int64
	UpdatedAt      int64
	LastSelectedAt int64
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
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read session index: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var decoded indexFile
	if err := decoder.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode session index: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("decode session index: trailing data")
	}
	if err := validateIndex(decoded); err != nil {
		return nil, err
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
		if strings.TrimSpace(ownerID) == "" || owner == nil || owner.Threads == nil {
			return fmt.Errorf("invalid session owner %q", ownerID)
		}
		if owner.ActiveThreadID != "" {
			thread, ok := owner.Threads[owner.ActiveThreadID]
			if !ok || thread == nil || thread.Archived {
				return fmt.Errorf("active thread %q is missing or archived", owner.ActiveThreadID)
			}
		}
		for id, thread := range owner.Threads {
			if thread == nil || id == "" || thread.ID != id {
				return fmt.Errorf("invalid thread record %q", id)
			}
		}
	}
	return nil
}

func (s *Store) Active(ownerID string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state := findOwner(s.index, ownerID)
	if state == nil || state.ActiveThreadID == "" {
		return "", false
	}
	return state.ActiveThreadID, true
}

func (s *Store) Owns(ownerID, threadID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state := findOwner(s.index, ownerID)
	if state == nil {
		return false
	}
	_, ok := state.Threads[threadID]
	return ok
}

func (s *Store) Records(ownerID string, archived bool) []Record {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state := findOwner(s.index, ownerID)
	if state == nil {
		return nil
	}
	records := make([]Record, 0, len(state.Threads))
	for _, thread := range state.Threads {
		if thread.Archived != archived {
			continue
		}
		records = append(records, Record{
			ID: thread.ID, Archived: thread.Archived,
			CreatedAt: thread.CreatedAt, UpdatedAt: thread.UpdatedAt,
			LastSelectedAt: thread.LastSelectedAt,
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

func (s *Store) Resolve(ownerID, reference string, archived bool) (Record, error) {
	reference = strings.TrimSpace(reference)
	if len(reference) < 6 {
		return Record{}, fmt.Errorf("session code must contain at least 6 characters")
	}
	var matches []Record
	for _, record := range s.Records(ownerID, archived) {
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
	if strings.TrimSpace(ownerID) == "" || strings.TrimSpace(thread.ID) == "" {
		return fmt.Errorf("owner and thread id are required")
	}
	return s.mutate(func(index *indexFile) error {
		state := ensureOwner(index, ownerID)
		record := state.Threads[thread.ID]
		if record == nil {
			record = &trackedThread{ID: thread.ID}
			state.Threads[thread.ID] = record
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
			state.ActiveThreadID = thread.ID
			record.LastSelectedAt = now.Unix()
		}
		return nil
	})
}

func (s *Store) SetActive(ownerID, threadID string, now time.Time) error {
	return s.mutate(func(index *indexFile) error {
		state := findOwner(*index, ownerID)
		if state == nil {
			return ErrNotOwned
		}
		thread, ok := state.Threads[threadID]
		if !ok || thread.Archived {
			return ErrNotOwned
		}
		state.ActiveThreadID = threadID
		thread.LastSelectedAt = now.Unix()
		thread.UpdatedAt = maxInt64(thread.UpdatedAt, now.Unix())
		return nil
	})
}

func (s *Store) MarkArchived(ownerID, threadID, nextActive string, archived bool, now time.Time) error {
	return s.mutate(func(index *indexFile) error {
		state := findOwner(*index, ownerID)
		if state == nil {
			return ErrNotOwned
		}
		thread, ok := state.Threads[threadID]
		if !ok {
			return ErrNotOwned
		}
		thread.Archived = archived
		thread.UpdatedAt = maxInt64(thread.UpdatedAt, now.Unix())
		if archived && state.ActiveThreadID == threadID {
			state.ActiveThreadID = nextActive
		}
		if !archived && state.ActiveThreadID == "" && nextActive == threadID {
			state.ActiveThreadID = threadID
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
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session index: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create session index directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".session-index-*")
	if err != nil {
		return fmt.Errorf("create temporary session index: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}
	if err := tmp.Chmod(0o600); err != nil {
		cleanup()
		return fmt.Errorf("set session index permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("write session index: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync session index: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close session index: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("replace session index: %w", err)
	}
	return nil
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
		owner = &ownerData{Threads: make(map[string]*trackedThread)}
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
