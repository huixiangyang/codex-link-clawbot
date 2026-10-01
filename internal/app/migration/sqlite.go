// Package migration 只在独占状态锁下执行一次性的 JSON 到 SQLite 切换。
package migration

import (
	"database/sql"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/huixiangyang/codex-link-clawbot/internal/adapters/wechat"
	"github.com/huixiangyang/codex-link-clawbot/internal/adapters/wechat/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/app/config"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/access"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/delivery"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/preference"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/request"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/target"
	"github.com/huixiangyang/codex-link-clawbot/internal/platform/statefile"
	"github.com/huixiangyang/codex-link-clawbot/internal/platform/storage"
)

const maxBackupBytes int64 = 2 << 30

var sourceNames = []string{"config.json", "accounts", "binding.json", "preferences.json", "targets.json", "remote-lock.json", "pending-notices.json", "conversation-drafts.json", "menu-receipts.json", "tasks", "management-token", "renders"}

type sourceRow struct{ Name string }

func Run(root string) (string, error) {
	layout, err := storage.NewLayout(root)
	if err != nil {
		return "", err
	}
	lease, err := statefile.Acquire(root, statefile.LeaseMigration)
	if err != nil {
		return "", err
	}
	defer lease.Close()
	stage := filepath.Join(root, ".sqlite-import")
	if _, err := os.Lstat(layout.Database()); err == nil {
		backup, found, err := storage.GetSetting(root, "import.backup")
		if err != nil {
			return "", err
		}
		if found {
			return filepath.Join(root, backup), finish(root, stage, backup)
		}
		return "", storage.CheckReady(root)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	for _, name := range []string{"project-state.json", "session-index.json", "library.json", ".business-migration.json"} {
		if _, err := os.Lstat(filepath.Join(root, name)); err == nil {
			return "", fmt.Errorf("旧业务 schema 不受支持：%s；请先使用旧版本完成业务升级", name)
		}
	}
	paths, names, err := collect(root, sourceNames)
	if err != nil {
		return "", err
	}
	if len(names) == 0 {
		return "", nil
	}
	manifest, err := statefile.BuildManifest(root, paths, maxBackupBytes)
	if err != nil {
		return "", err
	}
	if err := statefile.EnsurePrivateDirectory(stage); err != nil {
		return "", err
	}
	// 发布前失败可丢弃暂存副本；原始状态从未改写。
	if err := os.RemoveAll(stage); err != nil {
		return "", err
	}
	if err := statefile.EnsurePrivateDirectory(stage); err != nil {
		return "", err
	}
	cfg := config.DefaultConfig()
	if _, err := statefile.ReadJSON(filepath.Join(root, "config.json"), cfg, statefile.Options{MaxBytes: 4 << 20}); err != nil {
		return "", err
	}
	if err := config.SaveRoot(stage, cfg); err != nil {
		return "", err
	}
	steps := []func() error{
		func() error { return ilink.ImportLegacy(stage, root) }, func() error { return preference.ImportLegacy(stage, root) },
		func() error { return target.ImportLegacy(stage, root, cfg.Clawbot.ProjectEntries[0].ID) },
		func() error { return access.ImportLegacy(stage, root, cfg.Clawbot.Security.RemoteLockCode) },
		func() error { return delivery.ImportLegacy(stage, root) }, func() error { return wechat.ImportLegacy(stage, root) },
		func() error { return request.ImportLegacy(stage, root) },
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return "", err
		}
	}
	if data, err := os.ReadFile(filepath.Join(root, "management-token")); err == nil {
		token := strings.TrimSpace(string(data))
		decoded, err := hex.DecodeString(token)
		if err != nil || len(decoded) != 32 {
			return "", fmt.Errorf("invalid legacy management token")
		}
		if err := storage.SetSetting(stage, "management.token", token); err != nil {
			return "", err
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	backup := filepath.Join("backups", "json-"+uuid.NewString())
	raw := filepath.Join(root, backup, "original")
	if err := statefile.EnsurePrivateDirectory(raw); err != nil {
		return "", err
	}
	if err := statefile.VerifyManifest(root, manifest, maxBackupBytes); err != nil {
		return "", err
	}
	if err := statefile.CopyManifestFiles(root, raw, manifest, maxBackupBytes); err != nil {
		return "", err
	}
	if err := storage.Update(stage, func(tx *sql.Tx) error {
		if err := storage.Put(tx, "settings", storage.Setting{Key: "import.backup", Value: backup}); err != nil {
			return err
		}
		if err := storage.Put(tx, "settings", storage.Setting{Key: "import.complete", Value: "false"}); err != nil {
			return err
		}
		for _, entry := range manifest.Entries {
			if err := storage.Put(tx, "import_files", entry); err != nil {
				return err
			}
		}
		for _, name := range names {
			if err := storage.Put(tx, "import_sources", sourceRow{name}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return "", err
	}
	if err := verifyDatabase(stage); err != nil {
		return "", err
	}
	if err := statefile.VerifyManifest(root, manifest, maxBackupBytes); err != nil {
		return "", err
	}
	// 数据库和所有业务产物位于同一个 data 目录，只需一次原子发布。
	if err := os.Rename(filepath.Join(stage, "data"), filepath.Join(root, "data")); err != nil {
		return "", err
	}
	if err := syncDirectory(root); err != nil {
		return filepath.Join(root, backup), err
	}
	return filepath.Join(root, backup), finish(root, stage, backup)
}

func finish(root, stage, backup string) error {
	if !filepath.IsLocal(backup) || filepath.Clean(backup) != backup || !strings.HasPrefix(backup, "backups"+string(filepath.Separator)) {
		return fmt.Errorf("invalid migration backup path")
	}
	if complete, _, err := storage.GetSetting(root, "import.complete"); err != nil {
		return err
	} else if complete == "true" {
		return storage.CheckReady(root)
	}
	var entries []statefile.ManifestEntry
	var names []sourceRow
	if err := storage.View(root, func(tx *sql.Tx) error {
		var err error
		entries, err = storage.Rows[statefile.ManifestEntry](tx, "SELECT * FROM import_files ORDER BY path")
		if err != nil {
			return err
		}
		names, err = storage.Rows[sourceRow](tx, "SELECT * FROM import_sources")
		return err
	}); err != nil {
		return err
	}
	manifest := statefile.Manifest{Version: statefile.ManifestVersion, Entries: entries}
	if err := statefile.VerifyManifest(filepath.Join(root, backup, "original"), manifest, maxBackupBytes); err != nil {
		return err
	}
	for _, row := range names {
		if filepath.Base(row.Name) != row.Name {
			return fmt.Errorf("invalid import source name")
		}
		paths, existing, err := collect(root, []string{row.Name})
		if err != nil {
			return err
		}
		if len(existing) == 0 {
			continue
		}
		current, err := statefile.BuildManifest(root, paths, maxBackupBytes)
		if err != nil {
			return err
		}
		expected := map[string]statefile.ManifestEntry{}
		for _, entry := range entries {
			expected[entry.Path] = entry
		}
		for _, entry := range current.Entries {
			if expected[entry.Path] != entry {
				return fmt.Errorf("legacy source changed after import")
			}
		}
		if err := os.RemoveAll(filepath.Join(root, row.Name)); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(stage); err != nil {
		return err
	}
	if err := syncDirectory(root); err != nil {
		return err
	}
	return storage.SetSetting(root, "import.complete", "true")
}

func collect(root string, names []string) ([]string, []string, error) {
	var paths, existing []string
	for _, name := range names {
		path := filepath.Join(root, name)
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return nil, nil, err
		}
		existing = append(existing, name)
		err := filepath.WalkDir(path, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("legacy state contains symbolic link")
			}
			if entry.IsDir() {
				return nil
			}
			if !entry.Type().IsRegular() {
				return fmt.Errorf("legacy state contains non-regular file")
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			paths = append(paths, relative)
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
	}
	return paths, existing, nil
}

func verifyDatabase(root string) error {
	return storage.View(root, func(tx *sql.Tx) error {
		var result string
		if err := tx.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil {
			return err
		}
		if result != "ok" {
			return fmt.Errorf("SQLite integrity check failed")
		}
		rows, err := tx.Query("PRAGMA foreign_key_check")
		if err != nil {
			return err
		}
		defer rows.Close()
		if rows.Next() {
			return fmt.Errorf("SQLite foreign key check failed")
		}
		return rows.Err()
	})
}
func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
