package project

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func (m *Manager) load() error {
	data, err := os.ReadFile(m.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read project state: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var decoded stateFile
	if err := decoder.Decode(&decoded); err != nil {
		return fmt.Errorf("decode project state: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("decode project state: trailing data")
	}
	if decoded.Version != stateVersion || decoded.Owners == nil {
		return fmt.Errorf("unsupported or invalid project state")
	}
	for ownerID, projectID := range decoded.Owners {
		if strings.TrimSpace(ownerID) == "" {
			return fmt.Errorf("invalid project state owner")
		}
		if _, exists := m.byID[projectID]; !exists {
			return fmt.Errorf("project state references unconfigured project %q", projectID)
		}
	}
	m.state = decoded
	return nil
}

func saveState(path string, state stateFile) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode project state: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create project state directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".project-state-*")
	if err != nil {
		return fmt.Errorf("create temporary project state: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}
	if err := tmp.Chmod(0o600); err != nil {
		cleanup()
		return fmt.Errorf("set project state permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("write project state: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync project state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close project state: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("replace project state: %w", err)
	}
	return nil
}
