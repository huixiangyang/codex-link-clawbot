// Package storage 定义唯一状态根目录、SQLite 事务和关系表读写工具。
package storage

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/huixiangyang/codex-link-clawbot/internal/platform/statefile"
)

type Layout struct{ Root string }

func DefaultRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex-link-clawbot"), nil
}

func NewLayout(root string) (Layout, error) {
	if root == "" {
		var err error
		root, err = DefaultRoot()
		if err != nil {
			return Layout{}, err
		}
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == string(filepath.Separator) {
		return Layout{}, fmt.Errorf("state root must be a clean, specific absolute directory")
	}
	return Layout{Root: root}, nil
}

func (l Layout) Database() string             { return filepath.Join(l.Root, "data", "clawbot.db") }
func (l Layout) Requests() string             { return filepath.Join(l.Root, "data", "artifacts", "requests") }
func (l Layout) Temporary(kind string) string { return filepath.Join(l.Root, "tmp", kind) }
func (l Layout) Backups() string              { return filepath.Join(l.Root, "backups") }
func (l Layout) Logs() string                 { return filepath.Join(l.Root, "logs") }

func (l Layout) Ensure() error {
	for _, dir := range []string{l.Root, filepath.Dir(l.Database()), filepath.Dir(l.Requests()), l.Requests(), l.Temporary("render"), l.Temporary("voice"), l.Backups(), l.Logs()} {
		if err := statefile.EnsurePrivateDirectory(dir); err != nil {
			return err
		}
	}
	return nil
}

// CheckReady 不静默忽略旧文件，也不在服务启动时执行迁移。
func CheckReady(root string) error {
	for _, name := range []string{"config.json", "binding.json", "targets.json", "preferences.json", "remote-lock.json", "pending-notices.json", "conversation-drafts.json", "menu-receipts.json", "management-token", "tasks", "accounts", "project-state.json", "session-index.json", "library.json", ".business-migration.json", ".sqlite-import"} {
		if _, err := os.Lstat(filepath.Join(root, name)); err == nil {
			return fmt.Errorf("发现旧状态 %s；请先停止服务并运行 migrate-state --root %s", name, root)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	layout, err := NewLayout(root)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(layout.Database()); err == nil {
		complete, found, err := GetSetting(root, "import.complete")
		if err != nil {
			return err
		}
		if found && complete != "true" {
			return fmt.Errorf("SQLite 切换尚未完成，请重新运行 migrate-state --root %s", root)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}

// CleanupTemporary 仅在进程持有运行锁、尚未启动渲染任务时清理中间文件。
func (l Layout) CleanupTemporary() error {
	for _, kind := range []string{"render", "voice"} {
		root := l.Temporary(kind)
		if err := statefile.EnsurePrivateDirectory(root); err != nil {
			return err
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}
