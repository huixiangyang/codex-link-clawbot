package request

import (
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/platform/statefile"
	"github.com/huixiangyang/codex-link-clawbot/internal/platform/storage"
)

// ImportLegacy 仅接收最后一代 JSON schema，输入和结果均完整校验后写入暂存库。
func ImportLegacy(destination, source string) error {
	layout, err := storage.NewLayout(destination)
	if err != nil {
		return err
	}
	if err := layout.Ensure(); err != nil {
		return err
	}
	s := &Store{stateRoot: destination, root: layout.Requests(), state: defaultIndex(), now: time.Now}
	found, err := statefile.ReadJSON(filepath.Join(source, "tasks", "index.json"), &s.state, statefile.Options{MaxBytes: 16 << 20, Validate: func() error { return validateIndex(s.state) }})
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if err := s.saveLocked(); err != nil {
		return err
	}
	for _, owner := range s.state.Owners {
		for _, task := range owner.Tasks {
			oldRoot := filepath.Join(source, "tasks", task.ID)
			if err := statefile.EnsurePrivateDirectory(s.taskPath(task.ID)); err != nil {
				return err
			}
			if taskHasPayload(task, s.now().Unix()) {
				var r Request
				path := filepath.Join(oldRoot, "request.json")
				if _, err := os.Lstat(filepath.Join(oldRoot, "prepared.json")); err == nil {
					path = filepath.Join(oldRoot, "prepared.json")
				} else if !os.IsNotExist(err) {
					return err
				}
				found, err := statefile.ReadJSON(path, &r, statefile.Options{MaxBytes: 2 << 20})
				if err != nil {
					return err
				}
				if !found {
					return fmt.Errorf("missing legacy request %s", task.ID)
				}
				for _, items := range [][]Attachment{r.Images, r.Files} {
					for i := range items {
						old := items[i].Path
						if !filepath.IsLocal(old) || filepath.Clean(old) != old || !strings.HasPrefix(old, "inbox"+string(filepath.Separator)) {
							return fmt.Errorf("unsafe legacy attachment")
						}
						items[i].Path = filepath.Join("input", strings.TrimPrefix(old, "inbox"+string(filepath.Separator)))
						if err := copyLegacyArtifact(filepath.Join(oldRoot, old), filepath.Join(s.taskPath(task.ID), items[i].Path), items[i].Size, items[i].SHA256); err != nil {
							return err
						}
					}
				}
				if err := validateRequest(r); err != nil {
					return err
				}
				if r.SourceMessageKey != task.SourceMessageKey {
					return fmt.Errorf("legacy input identity mismatch")
				}
				if err := storage.Update(destination, func(tx *sql.Tx) error { return saveInput(tx, task.ID, r) }); err != nil {
					return err
				}
			}
			if task.ResultExpiresAt > s.now().Unix() || !task.State.Terminal() {
				var result Result
				found, err := statefile.ReadJSON(filepath.Join(oldRoot, "result.json"), &result, statefile.Options{MaxBytes: maxResultReplyBytes + (1 << 20)})
				if err != nil {
					return err
				}
				if found {
					for i := range result.Artifacts {
						a := &result.Artifacts[i]
						old := a.Path
						if !filepath.IsLocal(old) || filepath.Clean(old) != old || !strings.HasPrefix(old, "outbox"+string(filepath.Separator)) {
							return fmt.Errorf("unsafe legacy result path")
						}
						a.Path = filepath.Join("output", strings.TrimPrefix(old, "outbox"+string(filepath.Separator)))
						if err := copyLegacyArtifact(filepath.Join(oldRoot, old), filepath.Join(s.taskPath(task.ID), a.Path), a.Size, a.SHA256); err != nil {
							return err
						}
					}
					if err := validateResult(result, task); err != nil {
						return err
					}
					if err := s.saveResult(task.ID, result); err != nil {
						return err
					}
				} else if task.ExecutionCompletedAt > 0 && !task.ArchiveFailed {
					return fmt.Errorf("missing frozen legacy result")
				}
				var c Completion
				if found, err := statefile.ReadJSON(filepath.Join(oldRoot, "completion.json"), &c, statefile.Options{MaxBytes: maxResultReplyBytes + (1 << 20)}); err != nil {
					return err
				} else if found {
					if c.Version != 1 || c.At < task.StartedAt {
						return fmt.Errorf("invalid legacy completion")
					}
					if err := storage.Update(destination, func(tx *sql.Tx) error { return storage.Put(tx, "completions", completionRow{task.ID, c}) }); err != nil {
						return err
					}
				}
				// 结果尚未归档时保留输出目录，恢复保存不需要重跑 Codex。
				if !found && (task.ArchiveFailed || !task.State.Terminal()) {
					oldOutput := filepath.Join(oldRoot, "outbox")
					var total int64
					err := filepath.WalkDir(oldOutput, func(path string, entry fs.DirEntry, walkErr error) error {
						if os.IsNotExist(walkErr) && path == oldOutput {
							return nil
						}
						if walkErr != nil {
							return walkErr
						}
						if entry.Type()&os.ModeSymlink != 0 {
							return fmt.Errorf("legacy output contains symlink")
						}
						if entry.IsDir() {
							return nil
						}
						info, err := entry.Info()
						if err != nil {
							return err
						}
						total += info.Size()
						if total > MaxTaskBytes {
							return fmt.Errorf("legacy output exceeds limit")
						}
						relative, err := filepath.Rel(oldOutput, path)
						if err != nil {
							return err
						}
						return copyLegacyArtifact(path, filepath.Join(s.taskPath(task.ID), "output", relative), info.Size(), "")
					})
					if err != nil {
						return err
					}
				}
			}
		}
	}
	store, err := NewStore(destination)
	if err != nil {
		return err
	}
	for ownerID, owner := range store.state.Owners {
		for _, task := range owner.Tasks {
			if taskHasPayload(task, store.now().Unix()) {
				if _, err := store.LoadRequest(ownerID, task.ID); err != nil {
					return fmt.Errorf("verify imported input: %w", err)
				}
			}
			if task.ExecutionCompletedAt > 0 && !task.ArchiveFailed && task.ResultExpiresAt > store.now().Unix() {
				if _, err := store.LoadResult(ownerID, task.ID); err != nil {
					return fmt.Errorf("verify imported result: %w", err)
				}
			}
		}
	}
	return nil
}

func copyLegacyArtifact(source, destination string, size int64, expected string) error {
	if size <= 0 || size > MaxFileBytes {
		return fmt.Errorf("invalid legacy file size")
	}
	hash, err := hashRegularFile(source, size)
	if err != nil {
		return err
	}
	if expected != "" && hash != expected {
		return fmt.Errorf("legacy artifact checksum mismatch")
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return statefile.Write(destination, data, statefile.Options{MaxBytes: MaxFileBytes, CreateOnly: true})
}
