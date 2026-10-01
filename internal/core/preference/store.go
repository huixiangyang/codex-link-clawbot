package preference

import (
	"database/sql"
	"fmt"
	"strings"
	"sync"

	"github.com/huixiangyang/codex-link-clawbot/internal/core/presentation"
	"github.com/huixiangyang/codex-link-clawbot/internal/platform/storage"
)

const storeVersion = 1

type OwnerPreferences struct {
	Style        presentation.Style        `json:"style"`
	ResponseMode presentation.ResponseMode `json:"response_mode"`
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
	layout, err := storage.NewLayout(path)
	if err != nil {
		return nil, err
	}
	store := &Store{
		path:  layout.Root,
		state: stateFile{Version: storeVersion, Owners: make(map[string]OwnerPreferences)},
	}
	err = storage.View(store.path, func(tx *sql.Tx) error {
		rows, err := storage.Rows[preferenceRow](tx, "SELECT * FROM preferences")
		if err != nil {
			return err
		}
		for _, row := range rows {
			store.state.Owners[row.OwnerID] = row.OwnerPreferences
		}
		return validateState(store.state)
	})
	if err != nil {
		return nil, fmt.Errorf("load preferences: %w", err)
	}
	return store, nil
}

func DefaultOwnerPreferences() OwnerPreferences {
	return OwnerPreferences{Style: presentation.DefaultStyle, ResponseMode: presentation.ResponseText}
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

func (store *Store) SetStyle(ownerID string, style presentation.Style) error {
	if !style.Valid() {
		return fmt.Errorf("valid visual style is required")
	}
	return store.update(ownerID, func(preferences *OwnerPreferences) { preferences.Style = style })
}

func (store *Store) SetResponseMode(ownerID string, mode presentation.ResponseMode) error {
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
	if err := validateState(store.state); err != nil {
		return err
	}
	return storage.Update(store.path, func(tx *sql.Tx) error {
		for owner, prefs := range store.state.Owners {
			if err := storage.Put(tx, "preferences", preferenceRow{owner, prefs}); err != nil {
				return err
			}
		}
		return nil
	})
}

type preferenceRow struct {
	OwnerID string `json:"owner_id"`
	OwnerPreferences
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

// Save 整体校验并一次落盘，避免回复模式与风格部分生效。
func (store *Store) Save(ownerID string, next OwnerPreferences) error {
	if !next.ResponseMode.Valid() || !next.Style.Valid() {
		return fmt.Errorf("回复设置无效")
	}
	return store.update(ownerID, func(value *OwnerPreferences) { *value = next })
}
