package bridge

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/statefile"
)

// 仅保存来源编号和时间，不保存数字、解锁码、菜单正文或确认操作。
// 菜单会话本身重启失效；收件记录独立持久化，以抵御整批消息重新投递。
type menuReceipts struct {
	Version int                         `json:"version"`
	Owners  map[string]map[string]int64 `json:"owners"`
}

func (r *menuReceipts) contains(owner, source string) bool {
	_, ok := r.Owners[owner][source]
	return ok
}
func (m *numberMenus) receiptPath() string {
	return filepath.Join(filepath.Dir(m.h.tasks.Root()), "menu-receipts.json")
}
func (m *numberMenus) loadReceipts() error {
	if m.receipts != nil {
		return nil
	}
	if m.h.tasks == nil {
		return fmt.Errorf("菜单存储尚未就绪")
	}
	r := menuReceipts{Version: 1, Owners: make(map[string]map[string]int64)}
	_, err := statefile.ReadJSON(m.receiptPath(), &r, statefile.Options{MaxBytes: 8 << 20, Validate: func() error {
		if r.Version != 1 || r.Owners == nil {
			return fmt.Errorf("invalid menu receipt schema")
		}
		for owner, items := range r.Owners {
			if owner == "" || items == nil {
				return fmt.Errorf("invalid menu receipt owner")
			}
			for source, at := range items {
				if source == "" || at <= 0 {
					return fmt.Errorf("invalid menu receipt")
				}
			}
		}
		return nil
	}})
	if err != nil {
		return fmt.Errorf("读取菜单收件记录失败：%w", err)
	}
	m.receipts = &r
	return nil
}
func (m *numberMenus) recordReceipt(owner, source string) error {
	next := menuReceipts{Version: 1, Owners: make(map[string]map[string]int64)}
	cutoff := m.now().Add(-30 * 24 * time.Hour).Unix()
	for id, items := range m.receipts.Owners {
		next.Owners[id] = make(map[string]int64)
		for key, at := range items {
			if at > cutoff {
				next.Owners[id][key] = at
			}
		}
	}
	if next.Owners[owner] == nil {
		next.Owners[owner] = make(map[string]int64)
	}
	next.Owners[owner][source] = m.now().Unix()
	if err := statefile.WriteJSON(m.receiptPath(), next, statefile.Options{MaxBytes: 8 << 20}); err != nil {
		return fmt.Errorf("保存菜单收件记录失败，未执行操作：%w", err)
	}
	m.receipts = &next
	return nil
}
