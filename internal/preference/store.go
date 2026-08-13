package preference

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/huixiangyang/codex-link-clawbot/internal/statefile"
	"github.com/huixiangyang/codex-link-clawbot/internal/visual"
)

const storeVersion = 1

type ResponseMode string

const (
	ResponseAdaptive ResponseMode = "adaptive"
	ResponseReading  ResponseMode = "reading"
	ResponseVoice    ResponseMode = "voice"
)

type ResponseModeDefinition struct {
	ID          ResponseMode
	Name        string
	Description string
}

var responseModeDefinitions = []ResponseModeDefinition{
	{ID: ResponseAdaptive, Name: "自适应", Description: "短答文字，长答阅读卡"},
	{ID: ResponseReading, Name: "阅读", Description: "所有回答优先阅读卡"},
	{ID: ResponseVoice, Name: "语音", Description: "阅读卡与 MP3 配套交付"},
}

func ResponseModes() []ResponseModeDefinition {
	return append([]ResponseModeDefinition(nil), responseModeDefinitions...)
}

func (mode ResponseMode) Valid() bool {
	switch mode {
	case ResponseAdaptive, ResponseReading, ResponseVoice:
		return true
	default:
		return false
	}
}

func (mode ResponseMode) Definition() ResponseModeDefinition {
	for _, definition := range responseModeDefinitions {
		if definition.ID == mode {
			return definition
		}
	}
	return responseModeDefinitions[0]
}

type OwnerPreferences struct {
	Style        visual.Style `json:"style"`
	ResponseMode ResponseMode `json:"response_mode"`
}

type stateFile struct {
	Version int                         `json:"version"`
	Owners  map[string]OwnerPreferences `json:"owners"`
}

// Store 只持久化绑定者的界面与回答偏好，不保存消息正文或会话内容。
type Store struct {
	mu    sync.RWMutex
	path  string
	state stateFile
}

func NewStore(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve preferences path: %w", err)
		}
		path = filepath.Join(userHome, ".codex-link-clawbot", "preferences.json")
	}
	store := &Store{
		path:  filepath.Clean(path),
		state: stateFile{Version: storeVersion, Owners: make(map[string]OwnerPreferences)},
	}
	found, err := statefile.ReadJSON(store.path, &store.state, statefile.Options{
		Validate: func() error { return validateState(store.state) },
	})
	if err != nil {
		return nil, fmt.Errorf("load preferences: %w", err)
	}
	if !found {
		if err := store.saveLocked(); err != nil {
			return nil, fmt.Errorf("initialize preferences: %w", err)
		}
	}
	return store, nil
}

func DefaultOwnerPreferences() OwnerPreferences {
	return OwnerPreferences{Style: visual.DefaultStyle, ResponseMode: ResponseAdaptive}
}

func (store *Store) Get(ownerID string) OwnerPreferences {
	store.mu.RLock()
	defer store.mu.RUnlock()
	preferences, ok := store.state.Owners[strings.TrimSpace(ownerID)]
	if !ok {
		return DefaultOwnerPreferences()
	}
	return preferences
}

func (store *Store) SetStyle(ownerID string, style visual.Style) error {
	if !style.Valid() {
		return fmt.Errorf("valid visual style is required")
	}
	return store.update(ownerID, func(preferences *OwnerPreferences) { preferences.Style = style })
}

func (store *Store) SetResponseMode(ownerID string, mode ResponseMode) error {
	if !mode.Valid() {
		return fmt.Errorf("valid response mode is required")
	}
	return store.update(ownerID, func(preferences *OwnerPreferences) { preferences.ResponseMode = mode })
}

func (store *Store) update(ownerID string, change func(*OwnerPreferences)) error {
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return fmt.Errorf("preference owner is required")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	previous, existed := store.state.Owners[ownerID]
	next := previous
	if !existed {
		next = DefaultOwnerPreferences()
	}
	change(&next)
	store.state.Owners[ownerID] = next
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

func (store *Store) saveLocked() error {
	return statefile.WriteJSON(store.path, store.state, statefile.Options{
		Validate: func() error { return validateState(store.state) },
	})
}

func validateState(state stateFile) error {
	if state.Version != storeVersion || state.Owners == nil {
		return fmt.Errorf("invalid preferences schema")
	}
	for ownerID, preferences := range state.Owners {
		if strings.TrimSpace(ownerID) == "" || !preferences.Style.Valid() || !preferences.ResponseMode.Valid() {
			return fmt.Errorf("invalid owner preferences")
		}
	}
	return nil
}
