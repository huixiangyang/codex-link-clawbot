package access

import (
	"path/filepath"

	"github.com/huixiangyang/codex-link-clawbot/internal/platform/statefile"
)

func ImportLegacy(destination, source, code string) error {
	s := &RemoteLock{path: destination, code: code, state: remoteLockFile{Version: 1, Owners: map[string]bool{}}}
	if _, err := statefile.ReadJSON(filepath.Join(source, "remote-lock.json"), &s.state, statefile.Options{Validate: func() error { return validateRemoteLockState(s.state) }}); err != nil {
		return err
	}
	if err := s.saveLocked(); err != nil {
		return err
	}
	_, err := NewRemoteLock(destination, code)
	return err
}
