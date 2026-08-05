package visual

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const styleStoreVersion = 1

type styleFile struct {
	Version int              `json:"version"`
	Owners  map[string]Style `json:"owners"`
}

// StyleStore 只保存用户选择的模板 ID，不保存微信消息或渲染内容。
type StyleStore struct {
	mu    sync.RWMutex
	path  string
	state styleFile
}

func NewStyleStore(path string) (*StyleStore, error) {
	if strings.TrimSpace(path) == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve visual style path: %w", err)
		}
		path = filepath.Join(home, ".weclaw", "visual-styles.json")
	}
	store := &StyleStore{
		path:  filepath.Clean(path),
		state: styleFile{Version: styleStoreVersion, Owners: make(map[string]Style)},
	}
	if err := os.MkdirAll(filepath.Dir(store.path), 0o700); err != nil {
		return nil, fmt.Errorf("create visual style directory: %w", err)
	}
	if err := os.Chmod(filepath.Dir(store.path), 0o700); err != nil {
		return nil, fmt.Errorf("protect visual style directory: %w", err)
	}
	data, err := os.ReadFile(store.path)
	if errors.Is(err, os.ErrNotExist) {
		if err := store.saveLocked(); err != nil {
			return nil, fmt.Errorf("initialize visual styles: %w", err)
		}
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read visual styles: %w", err)
	}
	if err := os.Chmod(store.path, 0o600); err != nil {
		return nil, fmt.Errorf("protect visual styles: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&store.state); err != nil {
		return nil, fmt.Errorf("decode visual styles: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("decode visual styles: trailing data")
	}
	if err := validateStyleFile(store.state); err != nil {
		return nil, err
	}
	return store, nil
}

func (store *StyleStore) Get(ownerID string) Style {
	store.mu.RLock()
	defer store.mu.RUnlock()
	return NormalizeStyle(store.state.Owners[strings.TrimSpace(ownerID)])
}

func (store *StyleStore) Set(ownerID string, style Style) error {
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" || !style.Valid() {
		return fmt.Errorf("visual style owner and valid style are required")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	previous, existed := store.state.Owners[ownerID]
	store.state.Owners[ownerID] = style
	if err := store.saveLocked(); err != nil {
		if existed {
			store.state.Owners[ownerID] = previous
		} else {
			delete(store.state.Owners, ownerID)
		}
		return err
	}
	return nil
}

func (store *StyleStore) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(store.path), 0o700); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(store.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(store.state, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(store.path), ".visual-styles-*")
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
	if err := os.Rename(tempPath, store.path); err != nil {
		return err
	}
	return os.Chmod(store.path, 0o600)
}

func validateStyleFile(state styleFile) error {
	if state.Version != styleStoreVersion || state.Owners == nil {
		return fmt.Errorf("invalid visual style schema")
	}
	for ownerID, style := range state.Owners {
		if strings.TrimSpace(ownerID) == "" || !style.Valid() {
			return fmt.Errorf("invalid visual style owner")
		}
	}
	return nil
}
