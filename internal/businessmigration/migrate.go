// Package businessmigration 管理业务模型的离线、可恢复单向迁移。
package businessmigration

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/huixiangyang/codex-link-clawbot/internal/request"
	"github.com/huixiangyang/codex-link-clawbot/internal/statefile"
	"github.com/huixiangyang/codex-link-clawbot/internal/target"
)

const marker = ".business-migration.json"
const maxBackupBytes int64 = 4 << 30

var sourceNames = []string{"tasks", "targets.json", "project-state.json", "session-index.json", "library.json", "deliveries"}

type journal struct {
	Backup   string             `json:"backup"`
	Manifest statefile.Manifest `json:"manifest"`
}

// CheckReady 在构造任何状态服务前拒绝半迁移状态和未迁移的旧路由。
func CheckReady(root string) error {
	if _, err := os.Lstat(filepath.Join(root, marker)); err == nil {
		return fmt.Errorf("业务迁移尚未完成，请停止服务并重新运行 migrate-business --root %s", root)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := os.Lstat(filepath.Join(root, "targets.json")); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, name := range []string{"project-state.json", "session-index.json", "tasks/index.json", "library.json"} {
		if _, err := os.Lstat(filepath.Join(root, name)); err == nil {
			return fmt.Errorf("检测到旧业务状态，请先运行 migrate-business --root %s", root)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func Run(root, defaultWorkspace string) (string, error) {
	root = filepath.Clean(root)
	if !filepath.IsAbs(root) || root == string(filepath.Separator) || defaultWorkspace == "" {
		return "", fmt.Errorf("需要明确的状态根目录和默认工作空间")
	}
	lease, err := statefile.Acquire(root, statefile.LeaseMigration)
	if err != nil {
		return "", err
	}
	defer lease.Close()
	return RunLeased(root, defaultWorkspace)
}

// RunLeased 仅供已持有 LeaseMigration 的离线部署流程调用。
func RunLeased(root, defaultWorkspace string) (string, error) {
	var pending journal
	found, err := statefile.ReadJSON(filepath.Join(root, marker), &pending, statefile.Options{MaxBytes: 16 << 20})
	if err != nil {
		return "", err
	}
	if found {
		if err := rollback(root, pending); err != nil {
			return "", fmt.Errorf("恢复未完成迁移: %w", err)
		}
	}
	var header struct {
		Version int `json:"version"`
	}
	if data, err := os.ReadFile(filepath.Join(root, "tasks", "index.json")); err == nil {
		if err := json.Unmarshal(data, &header); err != nil {
			return "", err
		}
		if header.Version == 4 {
			if err := CheckReady(root); err != nil {
				return "", err
			}
			return "", nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	paths, err := collect(root)
	if err != nil {
		return "", err
	}
	manifest, err := statefile.BuildManifest(root, paths, maxBackupBytes)
	if err != nil {
		return "", err
	}
	backupName := ".business-backup-" + uuid.NewString()
	backup := filepath.Join(root, backupName)
	if err := statefile.CopyManifestFiles(root, backup, manifest, maxBackupBytes); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(root, ".business-stage-")
	if err != nil {
		return backup, err
	}
	defer os.RemoveAll(stage)
	if err := statefile.CopyManifestFiles(root, stage, manifest, maxBackupBytes); err != nil {
		return backup, err
	}
	targets, err := target.Open(filepath.Join(stage, "targets.json"), defaultWorkspace)
	if err != nil {
		return backup, err
	}
	if header.Version < 2 {
		if err := importRouting(stage, targets, defaultWorkspace); err != nil {
			return backup, err
		}
	}
	if err := request.MigrateLegacy(stage, root, targets); err != nil {
		return backup, err
	}
	pending = journal{Backup: backupName, Manifest: manifest}
	if err := statefile.WriteJSON(filepath.Join(root, marker), pending, statefile.Options{MaxBytes: 16 << 20}); err != nil {
		return backup, err
	}
	// 只替换已验证的新状态；断电时 marker 阻止启动，下次迁移先从校验备份恢复。
	for _, name := range []string{"tasks", "targets.json"} {
		if err = os.RemoveAll(filepath.Join(root, name)); err == nil {
			err = os.Rename(filepath.Join(stage, name), filepath.Join(root, name))
		}
		if err != nil {
			return backup, errors.Join(err, rollback(root, pending))
		}
	}
	// 已验证备份保留旧数据，运行目录彻底移除退休状态，避免误恢复为旧路由。
	for _, name := range sourceNames[2:] {
		if err = os.RemoveAll(filepath.Join(root, name)); err != nil {
			return backup, errors.Join(err, rollback(root, pending))
		}
	}
	if err = syncRoot(root); err != nil {
		return backup, err
	}
	if err = os.Remove(filepath.Join(root, marker)); err != nil {
		return backup, err
	}
	return backup, syncRoot(root)
}
func collect(root string) ([]string, error) {
	var paths []string
	for _, name := range sourceNames {
		base := filepath.Join(root, name)
		if _, err := os.Lstat(base); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, err
		}
		err := filepath.WalkDir(base, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("迁移状态不允许符号链接: %s", name)
			}
			if entry.IsDir() {
				return nil
			}
			if !entry.Type().IsRegular() {
				return fmt.Errorf("迁移状态包含特殊文件")
			}
			relative, err := filepath.Rel(root, path)
			if err == nil {
				paths = append(paths, relative)
			}
			return err
		})
		if err != nil {
			return nil, err
		}
	}
	return paths, nil
}
func rollback(root string, j journal) error {
	if !strings.HasPrefix(j.Backup, ".business-backup-") || filepath.Base(j.Backup) != j.Backup {
		return fmt.Errorf("invalid migration backup")
	}
	backup := filepath.Join(root, j.Backup)
	if err := statefile.VerifyManifest(backup, j.Manifest, maxBackupBytes); err != nil {
		return err
	}
	for _, name := range sourceNames {
		if err := os.RemoveAll(filepath.Join(root, name)); err != nil {
			return err
		}
	}
	// CopyManifestFiles 的 manifest 文件留在备份中，根目录无需额外副本。
	for _, entry := range j.Manifest.Entries {
		allowed := false
		for _, name := range sourceNames {
			if entry.Path == name || strings.HasPrefix(entry.Path, name+"/") {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("unexpected migration backup path")
		}
		data, err := os.ReadFile(filepath.Join(backup, filepath.FromSlash(entry.Path)))
		if err != nil {
			return err
		}
		if err := statefile.Write(filepath.Join(root, filepath.FromSlash(entry.Path)), data, statefile.Options{MaxBytes: maxBackupBytes}); err != nil {
			return err
		}
	}
	if err := syncRoot(root); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(root, marker)); err != nil {
		return err
	}
	return syncRoot(root)
}
func syncRoot(root string) error {
	f, err := os.Open(root)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func importRouting(root string, targets *target.Store, defaultWorkspace string) error {
	var projects struct {
		Version int               `json:"version"`
		Owners  map[string]string `json:"owners"`
	}
	found, err := statefile.ReadJSON(filepath.Join(root, "project-state.json"), &projects, statefile.Options{})
	if err != nil {
		return err
	}
	if found && projects.Version != 1 {
		return fmt.Errorf("unsupported workspace state")
	}
	var sessions struct {
		Version int `json:"version"`
		Owners  map[string]struct {
			ActiveThreads map[string]string `json:"active_threads"`
			Threads       json.RawMessage   `json:"threads"`
		} `json:"owners"`
	}
	// 会话索引还包含领域设置，读取保留其原文件，迁移只提取路由字段。
	data, err := os.ReadFile(filepath.Join(root, "session-index.json"))
	if err == nil {
		if err = json.Unmarshal(data, &sessions); err != nil {
			return err
		}
		if sessions.Version != 3 {
			return fmt.Errorf("unsupported session state")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for owner, session := range sessions.Owners {
		for workspace, id := range session.ActiveThreads {
			if _, err := targets.Remember(owner, workspace, id, true); err != nil {
				return err
			}
		}
		selected := projects.Owners[owner]
		if selected == "" {
			selected = defaultWorkspace
		}
		if _, err := targets.Select(owner, selected, nil); err != nil {
			return err
		}
	}
	for owner, workspace := range projects.Owners {
		if _, err := targets.Select(owner, workspace, nil); err != nil {
			return err
		}
	}
	return nil
}
