package preference

import (
	"path/filepath"

	"github.com/huixiangyang/codex-link-clawbot/internal/platform/statefile"
)

// ImportLegacy 仅供停服迁移调用，不进入运行时读取链路。
func ImportLegacy(destination, source string) error {
	s, err := NewStore(destination)
	if err != nil {
		return err
	}
	if _, err := statefile.ReadJSON(filepath.Join(source, "preferences.json"), &s.state, statefile.Options{Validate: func() error { return validateState(s.state) }}); err != nil {
		return err
	}
	return s.saveLocked()
}
