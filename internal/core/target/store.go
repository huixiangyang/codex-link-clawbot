// Package target 保存远程会话意图；线程尚未创建时也能冻结请求的对话归属。
package target

import (
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/huixiangyang/codex-link-clawbot/internal/platform/storage"
)

type Intent struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	ThreadID    string `json:"thread_id,omitempty"`
}
type ownerState struct {
	WorkspaceID string            `json:"workspace_id"`
	Current     map[string]string `json:"current"`
	Intents     map[string]Intent `json:"intents"`
}
type file struct {
	Version int                    `json:"version"`
	Owners  map[string]*ownerState `json:"owners"`
}
type Store struct {
	mu                     sync.Mutex
	path, defaultWorkspace string
	state                  file
	revisions              map[string]uint64
}

// Selection 绑定本进程内一次选择快照，包含切走再切回的修订变化。
type Selection struct {
	Intent
	revision uint64
}

var ErrSelectionChanged = errors.New("目标已被其他操作更新，请刷新后重试")

func Open(path, defaultWorkspace string) (*Store, error) {
	if strings.TrimSpace(defaultWorkspace) == "" {
		return nil, fmt.Errorf("default workspace is required")
	}
	s := &Store{path: path, defaultWorkspace: defaultWorkspace, state: file{Version: 1, Owners: map[string]*ownerState{}}, revisions: map[string]uint64{}}
	err := storage.View(path, func(tx *sql.Tx) error {
		owners, err := storage.Rows[targetOwnerRow](tx, "SELECT * FROM target_owners")
		if err != nil {
			return err
		}
		for _, row := range owners {
			s.owner(&s.state, row.OwnerID).WorkspaceID = row.WorkspaceID
		}
		intents, err := storage.Rows[intentRow](tx, "SELECT * FROM target_intents")
		if err != nil {
			return err
		}
		for _, row := range intents {
			owner := s.state.Owners[row.OwnerID]
			if owner == nil {
				return fmt.Errorf("target intent has no owner")
			}
			owner.Intents[row.ID] = row.Intent
		}
		selected, err := storage.Rows[selectionRow](tx, "SELECT * FROM target_selections")
		if err != nil {
			return err
		}
		for _, row := range selected {
			s.state.Owners[row.OwnerID].Current[row.WorkspaceID] = row.TargetID
		}
		return validate(s.state)
	})
	if err != nil {
		return nil, err
	}
	return s, nil
}

// Remember 记录可管理的会话；current=false 时不改变当前输入目标。
func (s *Store) Remember(owner, workspace, threadID string, current bool) (Intent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.clone()
	o := s.owner(&next, owner)
	i := Intent{ID: uuid.NewString(), WorkspaceID: workspace, ThreadID: threadID}
	for _, existing := range o.Intents {
		if existing.WorkspaceID == workspace && existing.ThreadID == threadID {
			if !current {
				return existing, nil
			}
			i = existing
			break
		}
	}
	o.Intents[i.ID] = i
	if current {
		o.WorkspaceID = workspace
		o.Current[workspace] = i.ID
	}
	if err := s.commit(next); err != nil {
		return Intent{}, err
	}
	return i, nil
}
func validate(f file) error {
	if f.Version != 1 || f.Owners == nil {
		return fmt.Errorf("invalid target schema")
	}
	for owner, o := range f.Owners {
		if strings.TrimSpace(owner) == "" || o == nil || o.WorkspaceID == "" || o.Current == nil || o.Intents == nil {
			return fmt.Errorf("invalid target owner")
		}
		for id, i := range o.Intents {
			if id != i.ID || i.WorkspaceID == "" {
				return fmt.Errorf("invalid target intent")
			}
			if _, err := uuid.Parse(id); err != nil {
				return err
			}
		}
		for workspace, id := range o.Current {
			i, ok := o.Intents[id]
			if !ok || i.WorkspaceID != workspace {
				return fmt.Errorf("invalid current target")
			}
		}
	}
	return nil
}
func (s *Store) Current(owner string) Intent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.currentLocked(owner)
}

func (s *Store) currentLocked(owner string) Intent {
	if o := s.state.Owners[owner]; o != nil {
		if i, ok := o.Intents[o.Current[o.WorkspaceID]]; ok {
			return i
		}
		return Intent{WorkspaceID: o.WorkspaceID}
	}
	return Intent{WorkspaceID: s.defaultWorkspace}
}

func (s *Store) Snapshot(owner string) Selection {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Selection{Intent: s.currentLocked(owner), revision: s.revisions[owner]}
}
func (s *Store) Resolve(owner, id string) (Intent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o := s.state.Owners[owner]; o != nil {
		if i, ok := o.Intents[id]; ok {
			return i, nil
		}
	}
	return Intent{}, fmt.Errorf("conversation target is unavailable")
}

// Capture 在接收时保存稳定意图，连续消息共享同一未解析目标。
func (s *Store) Capture(owner string) (Intent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.clone()
	o := s.owner(&next, owner)
	if i, ok := o.Intents[o.Current[o.WorkspaceID]]; ok {
		return i, nil
	}
	i := Intent{ID: uuid.NewString(), WorkspaceID: o.WorkspaceID}
	o.Intents[i.ID] = i
	o.Current[i.WorkspaceID] = i.ID
	if err := s.commit(next); err != nil {
		return Intent{}, err
	}
	return i, nil
}

// SelectWorkspace 恢复该工作空间上次的目标，不调用远端服务。
func (s *Store) SelectWorkspace(owner, workspace string) (Intent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.selectLocked(owner, workspace, "")
}

// SelectThread 只提交已经校验的线程；远端校验期间发生切换时拒绝旧操作。
func (s *Store) SelectThread(owner, workspace, threadID string, expected Selection) (Intent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(threadID) == "" {
		return Intent{}, fmt.Errorf("thread is required")
	}
	if s.currentLocked(owner) != expected.Intent || s.revisions[owner] != expected.revision {
		return Intent{}, ErrSelectionChanged
	}
	return s.selectLocked(owner, workspace, threadID)
}

func (s *Store) selectLocked(owner, workspace, threadID string) (Intent, error) {
	next := s.clone()
	o := s.owner(&next, owner)
	i, ok := o.Intents[o.Current[workspace]]
	if !ok || threadID != "" && i.ThreadID != threadID {
		i = Intent{ID: uuid.NewString(), WorkspaceID: workspace, ThreadID: threadID}
		for _, existing := range o.Intents {
			if threadID != "" && existing.WorkspaceID == workspace && existing.ThreadID == threadID {
				i = existing
				break
			}
		}
	}
	o.WorkspaceID = workspace
	o.Intents[i.ID] = i
	o.Current[workspace] = i.ID
	if err := s.commit(next); err != nil {
		return Intent{}, err
	}
	return i, nil
}

// NewConversation 准备独立的新对话；首条请求执行时才创建 Codex 线程。
// 正在执行的请求保留原意图，因此无需等待当前请求结束。
func (s *Store) NewConversation(owner, workspace string) (Intent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(owner) == "" || strings.TrimSpace(workspace) == "" {
		return Intent{}, fmt.Errorf("owner and workspace are required")
	}
	next := s.clone()
	o := s.owner(&next, owner)
	i := Intent{ID: uuid.NewString(), WorkspaceID: workspace}
	o.Intents[i.ID], o.Current[workspace], o.WorkspaceID = i, i.ID, workspace
	if err := s.commit(next); err != nil {
		return Intent{}, err
	}
	return i, nil
}

// Bind 只解析原始意图，不移动当前工作空间或覆盖后来建立的新目标。
func (s *Store) Bind(owner, id, threadID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.clone()
	o := next.Owners[owner]
	if o == nil {
		return fmt.Errorf("target owner is unavailable")
	}
	i, ok := o.Intents[id]
	if !ok || strings.TrimSpace(threadID) == "" {
		return fmt.Errorf("target is unavailable")
	}
	if i.ThreadID != "" && i.ThreadID != threadID {
		return fmt.Errorf("target was already resolved")
	}
	i.ThreadID = threadID
	o.Intents[id] = i
	return s.commit(next)
}

// ClearThread 为归档后的当前工作空间创建新意图，旧请求仍保持原归属。
func (s *Store) ClearThread(owner, threadID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.clone()
	o := next.Owners[owner]
	if o == nil {
		return nil
	}
	for workspace, id := range o.Current {
		if o.Intents[id].ThreadID == threadID {
			i := Intent{ID: uuid.NewString(), WorkspaceID: workspace}
			o.Intents[i.ID] = i
			o.Current[workspace] = i.ID
		}
	}
	return s.commit(next)
}
func (s *Store) owner(f *file, id string) *ownerState {
	o := f.Owners[id]
	if o == nil {
		o = &ownerState{WorkspaceID: s.defaultWorkspace, Current: map[string]string{}, Intents: map[string]Intent{}}
		f.Owners[id] = o
	}
	return o
}
func (s *Store) clone() file {
	next := file{Version: 1, Owners: map[string]*ownerState{}}
	for id, owner := range s.state.Owners {
		o := &ownerState{WorkspaceID: owner.WorkspaceID, Current: map[string]string{}, Intents: map[string]Intent{}}
		for key, value := range owner.Current {
			o.Current[key] = value
		}
		for key, value := range owner.Intents {
			o.Intents[key] = value
		}
		next.Owners[id] = o
	}
	return next
}
func (s *Store) commit(next file) error {
	if err := validate(next); err != nil {
		return err
	}
	if err := storage.Update(s.path, func(tx *sql.Tx) error {
		for id := range s.state.Owners {
			if _, exists := next.Owners[id]; !exists {
				if _, err := tx.Exec("DELETE FROM target_owners WHERE owner_id=?", id); err != nil {
					return err
				}
			}
		}
		for id, owner := range next.Owners {
			previous := s.state.Owners[id]
			if previous == nil {
				previous = &ownerState{Current: map[string]string{}, Intents: map[string]Intent{}}
			}
			if previous.WorkspaceID != owner.WorkspaceID {
				if err := storage.Put(tx, "target_owners", targetOwnerRow{id, owner.WorkspaceID}); err != nil {
					return err
				}
			}
			for _, intent := range owner.Intents {
				if previous.Intents[intent.ID] == intent {
					continue
				}
				if err := storage.Put(tx, "target_intents", intentRow{id, intent}); err != nil {
					return err
				}
			}
			for workspace, target := range owner.Current {
				if previous.Current[workspace] == target {
					continue
				}
				if err := storage.Put(tx, "target_selections", selectionRow{id, workspace, target}); err != nil {
					return err
				}
			}
			for workspace := range previous.Current {
				if _, exists := owner.Current[workspace]; !exists {
					if _, err := tx.Exec("DELETE FROM target_selections WHERE owner_id=? AND workspace_id=?", id, workspace); err != nil {
						return err
					}
				}
			}
			for intent := range previous.Intents {
				if _, exists := owner.Intents[intent]; !exists {
					if _, err := tx.Exec("DELETE FROM target_intents WHERE owner_id=? AND id=?", id, intent); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}); err != nil {
		return err
	}
	for id, owner := range next.Owners {
		previous := s.state.Owners[id]
		if previous == nil || previous.WorkspaceID != owner.WorkspaceID || !maps.Equal(previous.Current, owner.Current) {
			s.revisions[id]++
		}
	}
	s.state = next
	return nil
}

type targetOwnerRow struct {
	OwnerID     string `json:"owner_id"`
	WorkspaceID string `json:"workspace_id"`
}
type intentRow struct {
	OwnerID string `json:"owner_id"`
	Intent
}
type selectionRow struct {
	OwnerID     string `json:"owner_id"`
	WorkspaceID string `json:"workspace_id"`
	TargetID    string `json:"target_id"`
}
