package wechat

import (
	"path/filepath"

	"github.com/huixiangyang/codex-link-clawbot/internal/platform/statefile"
)

func ImportLegacy(destination, source string) error {
	var drafts draftState
	if found, err := statefile.ReadJSON(filepath.Join(source, "conversation-drafts.json"), &drafts, statefile.Options{MaxBytes: 8 << 20}); err != nil {
		return err
	} else if found {
		if err := saveDraftState(destination, drafts); err != nil {
			return err
		}
	}
	var receipts menuReceipts
	if found, err := statefile.ReadJSON(filepath.Join(source, "menu-receipts.json"), &receipts, statefile.Options{MaxBytes: 8 << 20}); err != nil {
		return err
	} else if found {
		if err := saveMenuReceipts(destination, receipts); err != nil {
			return err
		}
	}
	return nil
}
