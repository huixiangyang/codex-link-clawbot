package access

import (
	"crypto/subtle"
	"database/sql"
	"fmt"
	"strings"
	"sync"

	"github.com/huixiangyang/codex-link-clawbot/internal/platform/storage"
)

const remoteLockVersion = 1

type remoteLockFile struct {
	Version int             `json:"version"`
	Owners  map[string]bool `json:"owners"`
}

type RemoteLock struct {
	mu    sync.RWMutex
	path  string
	code  string
	state remoteLockFile
}

func NewRemoteLock(path, code string) (*RemoteLock, error) {
	layout, err := storage.NewLayout(path)
	if err != nil {
		return nil, err
	}
	path = layout.Root
	lock := &RemoteLock{path: path, code: code, state: remoteLockFile{Version: remoteLockVersion, Owners: make(map[string]bool)}}
	err = storage.View(path, func(tx *sql.Tx) error {
		rows, err := storage.Rows[lockRow](tx, "SELECT * FROM remote_locks")
		if err != nil {
			return err
		}
		for _, row := range rows {
			lock.state.Owners[row.OwnerID] = true
		}
		return validateRemoteLockState(lock.state)
	})
	if err != nil {
		return nil, fmt.Errorf("load remote lock: %w", err)
	}
	if code == "" {
		for _, locked := range lock.state.Owners {
			if locked {
				return nil, fmt.Errorf("remote lock code cannot be removed while an owner is locked")
			}
		}
	}
	return lock, nil
}

func (l *RemoteLock) Enabled() bool { return l != nil && l.code != "" }

func (l *RemoteLock) IsLocked(ownerID string) bool {
	if l == nil {
		return false
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.state.Owners[ownerID]
}

func (l *RemoteLock) Lock(ownerID string) error {
	if !l.Enabled() {
		return fmt.Errorf("remote lock code is not configured")
	}
	return l.set(ownerID, true)
}

func (l *RemoteLock) Unlock(ownerID, code string) error {
	if !l.Enabled() {
		return fmt.Errorf("remote lock code is not configured")
	}
	if subtle.ConstantTimeCompare([]byte(code), []byte(l.code)) != 1 {
		return fmt.Errorf("unlock code is incorrect")
	}
	return l.set(ownerID, false)
}

func (l *RemoteLock) set(ownerID string, locked bool) error {
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return fmt.Errorf("owner id is required")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	previous := l.state.Owners[ownerID]
	if locked {
		l.state.Owners[ownerID] = true
	} else {
		delete(l.state.Owners, ownerID)
	}
	if err := l.saveLocked(); err != nil {
		if previous {
			l.state.Owners[ownerID] = true
		} else {
			delete(l.state.Owners, ownerID)
		}
		return err
	}
	return nil
}

func (l *RemoteLock) saveLocked() error {
	if err := validateRemoteLockState(l.state); err != nil {
		return err
	}
	return storage.Update(l.path, func(tx *sql.Tx) error {
		if _, err := tx.Exec("DELETE FROM remote_locks"); err != nil {
			return err
		}
		for owner := range l.state.Owners {
			if err := storage.Put(tx, "remote_locks", lockRow{owner}); err != nil {
				return err
			}
		}
		return nil
	})
}

type lockRow struct {
	OwnerID string `json:"owner_id"`
}

func validateRemoteLockState(state remoteLockFile) error {
	if state.Version != remoteLockVersion || state.Owners == nil {
		return fmt.Errorf("invalid remote lock schema")
	}
	for ownerID, locked := range state.Owners {
		if strings.TrimSpace(ownerID) == "" || !locked {
			return fmt.Errorf("invalid remote lock owner")
		}
	}
	return nil
}
