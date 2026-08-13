package statefile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

const DefaultMaxBytes int64 = 1 << 20

// FaultPoint 只用于确定性故障注入，覆盖持久化事务的四个关键边界。
type FaultPoint string

const (
	FaultWrite         FaultPoint = "write"
	FaultFileSync      FaultPoint = "file_sync"
	FaultRename        FaultPoint = "rename"
	FaultDirectorySync FaultPoint = "directory_sync"
)

type Options struct {
	MaxBytes int64
	// CreateOnly 禁止覆盖已存在状态，适用于冻结结果与不可变请求。
	CreateOnly bool
	Validate   func() error
	Fault      func(FaultPoint) error
}

var pathLocks sync.Map // map[string]*sync.RWMutex

func lockFor(path string) *sync.RWMutex {
	clean := filepath.Clean(path)
	lock, _ := pathLocks.LoadOrStore(clean, &sync.RWMutex{})
	return lock.(*sync.RWMutex)
}

func normalizeOptions(options Options) Options {
	if options.MaxBytes <= 0 {
		options.MaxBytes = DefaultMaxBytes
	}
	return options
}

// ReadJSON 读取严格 JSON：拒绝未知字段、尾随数据、符号链接和超限文件。
// 文件不存在时返回 found=false，调用方负责决定是否创建默认状态。
func ReadJSON(path string, target any, options Options) (found bool, err error) {
	path = filepath.Clean(path)
	options = normalizeOptions(options)
	pathLock := lockFor(path)
	pathLock.RLock()
	defer pathLock.RUnlock()

	file, size, err := openRegularFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer file.Close()
	if size > options.MaxBytes {
		return false, wrap(CategoryCapacity, "read", path, fmt.Errorf("file exceeds %d bytes", options.MaxBytes))
	}
	data, err := io.ReadAll(io.LimitReader(file, options.MaxBytes+1))
	if err != nil {
		return false, wrapSystem("read", path, err)
	}
	if int64(len(data)) > options.MaxBytes {
		return false, wrap(CategoryCapacity, "read", path, fmt.Errorf("file exceeds %d bytes", options.MaxBytes))
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		category := CategorySchema
		var syntaxErr *json.SyntaxError
		if errors.As(err, &syntaxErr) || errors.Is(err, io.ErrUnexpectedEOF) {
			category = CategoryCorrupt
		}
		return false, wrap(category, "decode", path, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return false, wrap(CategorySchema, "decode", path, errors.New("trailing JSON data"))
	}
	if options.Validate != nil {
		if err := options.Validate(); err != nil {
			return false, wrap(CategorySchema, "validate", path, err)
		}
	}
	return true, nil
}

// WriteJSON 在同目录完成写入、文件同步、原子替换和父目录同步。
func WriteJSON(path string, value any, options Options) error {
	if options.Validate != nil {
		if err := options.Validate(); err != nil {
			return wrap(CategorySchema, "validate", filepath.Clean(path), err)
		}
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return wrap(CategorySchema, "encode", filepath.Clean(path), err)
	}
	data = append(data, '\n')
	return Write(path, data, options)
}

// Write 原子写入私有状态。目录同步失败时会恢复旧文件，避免内存回滚而磁盘已提交。
func Write(path string, data []byte, options Options) error {
	path = filepath.Clean(path)
	options = normalizeOptions(options)
	if int64(len(data)) > options.MaxBytes {
		return wrap(CategoryCapacity, "write", path, fmt.Errorf("payload exceeds %d bytes", options.MaxBytes))
	}
	pathLock := lockFor(path)
	pathLock.Lock()
	defer pathLock.Unlock()

	directory := filepath.Dir(path)
	if err := ensurePrivateDirectory(directory); err != nil {
		return err
	}
	if err := rejectUnsafeTarget(path); err != nil {
		return err
	}
	if options.CreateOnly {
		if _, err := os.Lstat(path); err == nil {
			return wrap(CategoryConflict, "create", path, errors.New("state already exists"))
		} else if !errors.Is(err, os.ErrNotExist) {
			return wrapSystem("inspect create target", path, err)
		}
	}
	if err := cleanupOrphans(directory, filepath.Base(path)); err != nil {
		return err
	}

	temporary, err := os.CreateTemp(directory, ".codex-link-clawbot-state-"+filepath.Base(path)+"-*")
	if err != nil {
		return wrapSystem("create temporary", path, err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	closeTemporary := func(cause error) error {
		closeErr := temporary.Close()
		if cause != nil {
			return cause
		}
		return closeErr
	}
	if err := temporary.Chmod(0o600); err != nil {
		return wrapSystem("chmod temporary", path, closeTemporary(err))
	}
	if err := inject(options, FaultWrite); err != nil {
		return closeTemporary(err)
	}
	if _, err := temporary.Write(data); err != nil {
		return wrapSystem("write temporary", path, closeTemporary(err))
	}
	if err := inject(options, FaultFileSync); err != nil {
		return closeTemporary(err)
	}
	if err := temporary.Sync(); err != nil {
		return wrapSystem("sync temporary", path, closeTemporary(err))
	}
	if err := closeTemporary(nil); err != nil {
		return wrapSystem("close temporary", path, err)
	}
	if err := inject(options, FaultRename); err != nil {
		return err
	}

	backupPath, hadPrevious, err := preservePrevious(path)
	if err != nil {
		return err
	}
	if backupPath != "" {
		defer os.Remove(backupPath)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return wrapSystem("replace", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return errors.Join(wrapSystem("chmod", path, err), rollbackReplacement(path, backupPath, hadPrevious))
	}
	if err := inject(options, FaultDirectorySync); err != nil {
		return errors.Join(err, rollbackReplacement(path, backupPath, hadPrevious))
	}
	if err := syncDirectory(directory); err != nil {
		return errors.Join(err, rollbackReplacement(path, backupPath, hadPrevious))
	}
	// 此处已经提交。备份删除后的第二次目录同步失败只会留下可清理的目录项，不回滚已确认的新状态。
	if backupPath != "" {
		_ = os.Remove(backupPath)
		_ = syncDirectory(directory)
	}
	return nil
}

func inject(options Options, point FaultPoint) error {
	if options.Fault == nil {
		return nil
	}
	if err := options.Fault(point); err != nil {
		return wrap(CategoryUnavailable, "fault "+string(point), "", err)
	}
	return nil
}

func openRegularFile(path string) (*os.File, int64, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, 0, wrapSystem("open", path, err)
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, 0, wrapSystem("stat", path, err)
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, 0, wrap(CategoryPermission, "open", path, errors.New("state must be a regular file"))
	}
	if info.Mode().Perm() != 0o600 {
		if err := file.Chmod(0o600); err != nil {
			_ = file.Close()
			return nil, 0, wrapSystem("protect", path, err)
		}
	}
	return file, info.Size(), nil
}

func ensurePrivateDirectory(path string) error {
	if err := rejectSymlinkAncestors(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return wrapSystem("create directory", path, err)
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return wrapSystem("inspect directory", path, err)
	}
	if err := rejectSymlinkAncestors(path); err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return wrap(CategoryPermission, "inspect directory", path, errors.New("state directory must be a real directory"))
	}
	if info.Mode().Perm() != 0o700 {
		if err := os.Chmod(path, 0o700); err != nil {
			return wrapSystem("protect directory", path, err)
		}
	}
	return nil
}

func rejectSymlinkAncestors(path string) error {
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			// 尚未创建的尾部路径继续向上检查，不能借此绕过已存在的符号链接父目录。
		} else if err != nil {
			return wrapSystem("inspect directory ancestor", current, err)
		} else if info.Mode()&os.ModeSymlink != 0 {
			return wrap(CategoryPermission, "inspect directory ancestor", current, errors.New("state path cannot traverse a symbolic link"))
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
	}
}

// EnsurePrivateDirectory 建立或收紧私有状态目录，并拒绝符号链接。
func EnsurePrivateDirectory(path string) error {
	return ensurePrivateDirectory(filepath.Clean(path))
}

func rejectUnsafeTarget(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return wrapSystem("inspect", path, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return wrap(CategoryPermission, "inspect", path, errors.New("state target must be a regular file"))
	}
	return nil
}

func preservePrevious(path string) (backupPath string, existed bool, err error) {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	} else if err != nil {
		return "", false, wrapSystem("inspect previous", path, err)
	}
	backup, err := os.CreateTemp(filepath.Dir(path), ".codex-link-clawbot-backup-"+filepath.Base(path)+"-*")
	if err != nil {
		return "", false, wrapSystem("reserve backup", path, err)
	}
	backupPath = backup.Name()
	if err := backup.Close(); err != nil {
		_ = os.Remove(backupPath)
		return "", false, wrapSystem("close backup", path, err)
	}
	if err := os.Remove(backupPath); err != nil {
		return "", false, wrapSystem("prepare backup", path, err)
	}
	if err := os.Link(path, backupPath); err != nil {
		return "", false, wrapSystem("preserve previous", path, err)
	}
	return backupPath, true, nil
}

func rollbackReplacement(path, backupPath string, hadPrevious bool) error {
	if hadPrevious {
		if backupPath == "" {
			return wrap(CategoryCorrupt, "rollback", path, errors.New("previous state backup is missing"))
		}
		if err := os.Rename(backupPath, path); err != nil {
			return wrapSystem("rollback", path, err)
		}
	} else if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return wrapSystem("rollback", path, err)
	}
	return syncDirectory(filepath.Dir(path))
}

func cleanupOrphans(directory, base string) error {
	prefixes := []string{".codex-link-clawbot-state-" + base + "-", ".codex-link-clawbot-backup-" + base + "-"}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return wrapSystem("list directory", directory, err)
	}
	for _, entry := range entries {
		match := false
		for _, prefix := range prefixes {
			if strings.HasPrefix(entry.Name(), prefix) {
				match = true
				break
			}
		}
		if !match {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			return wrapSystem("inspect orphan", path, err)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return wrap(CategoryPermission, "clean orphan", path, errors.New("orphan state entry must be a regular file"))
		}
		if err := os.Remove(path); err != nil {
			return wrapSystem("clean orphan", path, err)
		}
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return wrapSystem("open directory", path, err)
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return wrapSystem("sync directory", path, err)
	}
	if err := directory.Close(); err != nil {
		return wrapSystem("close directory", path, err)
	}
	return nil
}
