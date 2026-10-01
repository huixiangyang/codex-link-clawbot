package target

import (
	"path/filepath"

	"github.com/huixiangyang/codex-link-clawbot/internal/platform/statefile"
)

func ImportLegacy(destination, source, workspace string) error {
	s, err := Open(destination, workspace)
	if err != nil {
		return err
	}
	legacy := file{Version: 1, Owners: map[string]*ownerState{}}
	if _, err := statefile.ReadJSON(filepath.Join(source, "targets.json"), &legacy, statefile.Options{MaxBytes: 16 << 20, Validate: func() error { return validate(legacy) }}); err != nil {
		return err
	}
	return s.commit(legacy)
}
