package statefile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

type LeaseMode string

const (
	LeaseRuntime   LeaseMode = "runtime"
	LeaseMigration LeaseMode = "migration"
)

// Lease 在整个服务或离线迁移生命周期内持有根目录独占锁。
type Lease struct {
	file *os.File
	path string
}

func DefaultRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", wrapSystem("resolve root", "", err)
	}
	return filepath.Join(home, ".codex-link-clawbot"), nil
}

func Acquire(root string, mode LeaseMode) (*Lease, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if !filepath.IsAbs(root) || root == string(filepath.Separator) {
		return nil, wrap(CategoryPermission, "acquire lease", root, errors.New("state root must be a specific absolute path"))
	}
	if mode != LeaseRuntime && mode != LeaseMigration {
		return nil, wrap(CategorySchema, "acquire lease", root, fmt.Errorf("invalid lease mode %q", mode))
	}
	if err := ensurePrivateDirectory(root); err != nil {
		return nil, err
	}
	path := filepath.Join(root, ".state.lock")
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, wrap(CategoryPermission, "inspect lease", path, errors.New("lease must be a regular file"))
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, wrapSystem("inspect lease", path, err)
	}
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, wrapSystem("open lease", path, err)
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		if err == nil {
			err = errors.New("lease must be a regular file")
		}
		return nil, wrap(CategoryPermission, "inspect lease", path, err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, wrapSystem("protect lease", path, err)
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, wrap(CategoryConflict, "acquire lease", path, fmt.Errorf("state is already owned by another runtime or migration"))
		}
		return nil, wrapSystem("acquire lease", path, err)
	}
	return &Lease{file: file, path: path}, nil
}

func (lease *Lease) Close() error {
	if lease == nil || lease.file == nil {
		return nil
	}
	file := lease.file
	lease.file = nil
	if err := unix.Flock(int(file.Fd()), unix.LOCK_UN); err != nil {
		_ = file.Close()
		return wrapSystem("release lease", lease.path, err)
	}
	if err := file.Close(); err != nil {
		return wrapSystem("close lease", lease.path, err)
	}
	return nil
}
