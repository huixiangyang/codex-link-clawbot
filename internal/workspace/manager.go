package workspace

import (
	"fmt"
	"os"
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
		info, err := os.Stat(item.Root)
		if err != nil || !info.IsDir() {
			return nil, fmt.Errorf("工作空间目录不可用：%s", item.ID)
		}
		item.Name = strings.TrimSpace(item.Name)
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
