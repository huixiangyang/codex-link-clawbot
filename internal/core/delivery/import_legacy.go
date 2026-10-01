package delivery

import (
	"path/filepath"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/platform/statefile"
)

func ImportLegacy(destination, source string) error {
	s, err := OpenNoticeStore(destination, time.Now)
	if err != nil {
		return err
	}
	if _, err := statefile.ReadJSON(filepath.Join(source, "pending-notices.json"), &s.state, statefile.Options{}); err != nil {
		return err
	}
	for owner, notices := range s.state.Owners {
		kept := notices[:0]
		for _, notice := range notices {
			if notice.Kind != "deployment" {
				kept = append(kept, notice)
			}
		}
		s.state.Owners[owner] = kept
	}
	if err := s.validate(); err != nil {
		return err
	}
	return s.saveLocked()
}
