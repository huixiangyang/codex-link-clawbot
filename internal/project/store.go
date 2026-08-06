package project

import (
	"fmt"
	"strings"

	"github.com/huixiangyang/weclaw/internal/statefile"
)

func (m *Manager) load() error {
	var decoded stateFile
	found, err := statefile.ReadJSON(m.path, &decoded, statefile.Options{
		Validate: func() error { return m.validateState(decoded) },
	})
	if err != nil {
		return fmt.Errorf("load project state: %w", err)
	}
	if !found {
		return nil
	}
	m.state = decoded
	return nil
}

func (m *Manager) saveState(state stateFile) error {
	return statefile.WriteJSON(m.path, state, statefile.Options{
		Validate: func() error { return m.validateState(state) },
	})
}

func (m *Manager) validateState(state stateFile) error {
	if state.Version != stateVersion || state.Owners == nil {
		return fmt.Errorf("unsupported or invalid project state")
	}
	for ownerID, projectID := range state.Owners {
		if strings.TrimSpace(ownerID) == "" {
			return fmt.Errorf("invalid project state owner")
		}
		if _, exists := m.byID[projectID]; !exists {
			return fmt.Errorf("project state references unconfigured project %q", projectID)
		}
	}
	return nil
}
