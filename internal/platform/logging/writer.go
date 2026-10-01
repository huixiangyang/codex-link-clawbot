// Package logging 管理独立于业务数据库的私有诊断日志。
package logging

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/platform/statefile"
	"github.com/huixiangyang/codex-link-clawbot/internal/platform/storage"
	"golang.org/x/sys/unix"
	"gopkg.in/natefinch/lumberjack.v2"
)

const FileName = "service.log"

type Writer struct {
	mu      sync.Mutex
	file    *lumberjack.Logger
	mirror  io.Writer
	day     string
	now     func() time.Time
	secrets []string
	failed  bool
	closed  bool
}

// Open 只能在持有实例运行锁后调用，避免多个进程轮转同一个文件。
func Open(root string, mirror io.Writer) (*Writer, error) {
	layout, err := storage.NewLayout(root)
	if err != nil {
		return nil, err
	}
	if err := statefile.EnsurePrivateDirectory(layout.Logs()); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(layout.Logs())
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if name != FileName && !(strings.HasPrefix(name, "service-") && strings.HasSuffix(name, ".log")) {
			continue
		}
		info, err := os.Lstat(filepath.Join(layout.Logs(), name))
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("log must be a regular file: %s", name)
		}
		if err := os.Chmod(filepath.Join(layout.Logs(), name), 0o600); err != nil {
			return nil, err
		}
	}
	path := filepath.Join(layout.Logs(), FileName)
	fd, err := unix.Open(path, unix.O_WRONLY|unix.O_APPEND|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, statErr := file.Stat()
	closeErr := file.Close()
	if err := errors.Join(statErr, closeErr); err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("log must be a regular file")
	}
	if mirror == nil {
		mirror = io.Discard
	}
	writer := &Writer{
		file:   &lumberjack.Logger{Filename: path, MaxSize: 10, MaxBackups: 5, MaxAge: 7},
		mirror: mirror, day: time.Now().UTC().Format(time.DateOnly), now: time.Now,
	}
	// 跨日首次写入前切分；静默期间不额外启动后台轮转任务。
	if info.Size() > 0 && info.ModTime().UTC().Format(time.DateOnly) != writer.day {
		err = writer.file.Rotate()
	} else {
		_, err = writer.file.Write(nil)
	}
	if err != nil {
		_ = writer.file.Close()
		return nil, err
	}
	return writer, nil
}

// Protect 额外屏蔽当前实例已知的凭据，包括未带字段名的上游错误信息。
func (w *Writer) Protect(values ...string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, value := range values {
		if value != "" {
			w.secrets = append(w.secrets, value)
		}
	}
	sort.Slice(w.secrets, func(i, j int) bool { return len(w.secrets[i]) > len(w.secrets[j]) })
}

func (w *Writer) sanitizeLocked(message string) string {
	for _, secret := range w.secrets {
		message = strings.ReplaceAll(message, secret, "[redacted]")
	}
	return Sanitize(message)
}

type safeError struct {
	cause   error
	message string
}

func (e safeError) Error() string { return e.message }
func (e safeError) Unwrap() error { return e.cause }

// SanitizeError 保留错误链，同时避免 CLI 再次输出未脱敏的服务错误。
func (w *Writer) SanitizeError(err error) error {
	if err == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return safeError{cause: err, message: w.sanitizeLocked(err.Error())}
}

func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, os.ErrClosed
	}
	data := []byte(w.sanitizeLocked(strings.TrimRight(string(p), "\r\n")) + "\n")
	// 两个输出独立尝试；文件故障不能令 journal 一并失声。
	_, mirrorErr := w.mirror.Write(data)
	day := w.now().UTC().Format(time.DateOnly)
	var fileErr error
	if day != w.day {
		fileErr = w.file.Rotate()
		if fileErr == nil {
			w.day = day
		}
	}
	if fileErr == nil {
		_, fileErr = w.file.Write(data)
	}
	if fileErr != nil && !w.failed {
		_, _ = fmt.Fprintln(w.mirror, "[logging] file output failed; diagnostics remain on stderr")
	}
	w.failed = fileErr != nil
	if err := errors.Join(fileErr, mirrorErr); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	return w.file.Close()
}
