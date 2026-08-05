package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/huixiangyang/weclaw/config"
)

const stateVersion = 1

var ErrUnknownProject = errors.New("unknown project")

type Definition struct {
	ID          string
	Name        string
	Root        string
	ServiceName string
	HealthURL   string
}

type stateFile struct {
	Version int               `json:"version"`
	Owners  map[string]string `json:"owners"`
}

// Manager 持有唯一可信的项目清单，并持久化每位微信用户当前选择的项目。
type Manager struct {
	mu      sync.RWMutex
	path    string
	ordered []Definition
	byID    map[string]Definition
	state   stateFile
}

func DefaultStatePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".weclaw", "project-state.json"), nil
}

func NewManager(projects []config.ProjectConfig, path string) (*Manager, error) {
	if len(projects) == 0 {
		return nil, fmt.Errorf("at least one project is required")
	}
	if strings.TrimSpace(path) == "" {
		resolved, err := DefaultStatePath()
		if err != nil {
			return nil, err
		}
		path = resolved
	}
	m := &Manager{
		path:  path,
		byID:  make(map[string]Definition, len(projects)),
		state: stateFile{Version: stateVersion, Owners: make(map[string]string)},
	}
	for _, source := range projects {
		info, err := os.Stat(source.Root)
		if err != nil {
			return nil, fmt.Errorf("inspect project %q root: %w", source.ID, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("project %q root is not a directory", source.ID)
		}
		definition := Definition{
			ID: source.ID, Name: strings.TrimSpace(source.Name), Root: source.Root,
			ServiceName: source.ServiceName, HealthURL: source.HealthURL,
		}
		m.ordered = append(m.ordered, definition)
		m.byID[definition.ID] = definition
	}
	if err := m.load(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) List() []Definition {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneDefinitions(m.ordered)
}

func (m *Manager) Get(projectID string) (Definition, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	definition, ok := m.byID[projectID]
	return cloneDefinition(definition), ok
}

func (m *Manager) Current(ownerID string) Definition {
	m.mu.RLock()
	defer m.mu.RUnlock()
	projectID := m.state.Owners[ownerID]
	if projectID == "" {
		return cloneDefinition(m.ordered[0])
	}
	return cloneDefinition(m.byID[projectID])
}

func (m *Manager) Select(ownerID, projectID string) (Definition, error) {
	ownerID = strings.TrimSpace(ownerID)
	projectID = strings.TrimSpace(projectID)
	if ownerID == "" {
		return Definition{}, fmt.Errorf("owner id is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	definition, ok := m.byID[projectID]
	if !ok {
		return Definition{}, ErrUnknownProject
	}
	next := cloneState(m.state)
	next.Owners[ownerID] = projectID
	if err := m.saveState(next); err != nil {
		return Definition{}, err
	}
	m.state = next
	return cloneDefinition(definition), nil
}

// Resolve 接受精确 ID、精确名称或唯一 ID 前缀，避免模糊选择错误项目。
func (m *Manager) Resolve(reference string) (Definition, error) {
	reference = strings.TrimSpace(reference)
	m.mu.RLock()
	defer m.mu.RUnlock()
	if definition, ok := m.byID[reference]; ok {
		return cloneDefinition(definition), nil
	}
	var matches []Definition
	for _, definition := range m.ordered {
		if definition.Name == reference || strings.HasPrefix(definition.ID, reference) {
			matches = append(matches, definition)
		}
	}
	if len(matches) != 1 {
		return Definition{}, ErrUnknownProject
	}
	return cloneDefinition(matches[0]), nil
}

func cloneDefinition(source Definition) Definition {
	return source
}

func cloneDefinitions(source []Definition) []Definition {
	result := make([]Definition, 0, len(source))
	for _, definition := range source {
		result = append(result, cloneDefinition(definition))
	}
	return result
}

func cloneState(source stateFile) stateFile {
	result := stateFile{Version: source.Version, Owners: make(map[string]string, len(source.Owners))}
	for ownerID, projectID := range source.Owners {
		result.Owners[ownerID] = projectID
	}
	return result
}

// Owners 返回稳定顺序，仅供状态页和测试使用，不暴露可变内部映射。
func (m *Manager) Owners() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	owners := make([]string, 0, len(m.state.Owners))
	for ownerID := range m.state.Owners {
		owners = append(owners, ownerID)
	}
	sort.Strings(owners)
	return owners
}
