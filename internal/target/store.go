// Package target 保存远程会话意图；线程尚未创建时也能冻结请求的对话归属。
package target

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/huixiangyang/codex-link-clawbot/internal/statefile"
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
}

func Open(path, defaultWorkspace string) (*Store, error) {
	if strings.TrimSpace(defaultWorkspace) == "" {
		return nil, fmt.Errorf("default workspace is required")
	}
	s := &Store{path: path, defaultWorkspace: defaultWorkspace, state: file{Version: 1, Owners: map[string]*ownerState{}}}
	found, err := statefile.ReadJSON(path, &s.state, statefile.Options{MaxBytes: 16 << 20, Validate: func() error { return validate(s.state) }})
	if err != nil {
		return nil, err
	}
	if !found {
		if err := s.commit(s.state); err != nil {
			return nil, err
		}
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
	if o := s.state.Owners[owner]; o != nil {
		if i, ok := o.Intents[o.Current[o.WorkspaceID]]; ok {
			return i
		}
		return Intent{WorkspaceID: o.WorkspaceID}
	}
	return Intent{WorkspaceID: s.defaultWorkspace}
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

// Select 把远端校验和本地目标提交串行化。prepare 失败时完全保留原目标。
// prepare 为 nil 表示仅切换工作空间；非 nil 表示选择一个新的会话意图。
func (s *Store) Select(owner, workspace string, prepare func() (string, error)) (Intent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.clone()
	o := s.owner(&next, owner)
	i, ok := o.Intents[o.Current[workspace]]
	if !ok || prepare != nil {
		i = Intent{ID: uuid.NewString(), WorkspaceID: workspace}
	}
	if prepare != nil {
		id, err := prepare()
		if err != nil {
			return Intent{}, err
		}
		if strings.TrimSpace(id) == "" {
			return Intent{}, fmt.Errorf("thread is required")
		}
		i.ThreadID = id
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
	data, _ := json.Marshal(s.state)
	var next file
	_ = json.Unmarshal(data, &next)
	return next
}
func (s *Store) commit(next file) error {
	if err := statefile.WriteJSON(s.path, next, statefile.Options{MaxBytes: 16 << 20, Validate: func() error { return validate(next) }}); err != nil {
		return err
	}
	s.state = next
	return nil
}
