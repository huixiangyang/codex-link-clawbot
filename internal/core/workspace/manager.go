package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Definition struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Root string `json:"root"`
}
type Manager struct {
	ordered []Definition
	byID    map[string]Definition
}

func NewManager(items []Definition) (*Manager, error) {
	if len(items) == 0 {
		return nil, fmt.Errorf("至少配置一个工作空间")
	}
	m := &Manager{byID: map[string]Definition{}}
	for _, item := range items {
		item.ID, item.Name = strings.TrimSpace(item.ID), strings.TrimSpace(item.Name)
		item.Root = filepath.Clean(strings.TrimSpace(item.Root))
		if !filepath.IsAbs(item.Root) {
			return nil, fmt.Errorf("工作空间必须使用绝对目录：%s", item.ID)
		}
		info, err := os.Stat(item.Root)
		if err != nil || !info.IsDir() {
			return nil, fmt.Errorf("工作空间目录不可用：%s", item.ID)
		}
		if item.Name == "" {
			item.Name = item.ID
		}
		if _, exists := m.byID[item.ID]; exists || item.ID == "" {
			return nil, fmt.Errorf("工作空间编号重复或为空")
		}
		m.byID[item.ID] = item
		m.ordered = append(m.ordered, item)
	}
	return m, nil
}
func (m *Manager) List() []Definition               { return append([]Definition(nil), m.ordered...) }
func (m *Manager) Get(id string) (Definition, bool) { item, ok := m.byID[id]; return item, ok }

// Match 在真实路径上选择最具体的工作空间，避免父目录抢占嵌套项目。
func (m *Manager) Match(cwd string) (Definition, bool) {
	canonical, err := canonicalPath(cwd)
	if err != nil {
		return Definition{}, false
	}
	var found Definition
	longest := -1
	for _, definition := range m.ordered {
		root, err := canonicalPath(definition.Root)
		if err == nil && len(root) > longest && within(canonical, root) {
			found, longest = definition, len(root)
		}
	}
	return found, longest >= 0
}

// Contains 供执行、切换与详情查询共用同一真实路径边界。
func Contains(cwd, root string) bool {
	canonical, err := canonicalPath(cwd)
	if err != nil {
		return false
	}
	base, err := canonicalPath(root)
	return err == nil && within(canonical, base)
}

func canonicalPath(path string) (string, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("path must be absolute")
	}
	return filepath.EvalSymlinks(path)
}

func within(path, root string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && (relative == "." || filepath.IsLocal(relative))
}
