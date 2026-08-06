package statefile

import (
	"errors"
	"fmt"
	"io/fs"
	"sync"
	"syscall"
	"time"
)

type FailureSnapshot struct {
	Category Category
	At       time.Time
}

var failureHealth struct {
	sync.RWMutex
	last FailureSnapshot
}

// Category 是所有持久状态错误的稳定分类，调用方不得依赖底层系统错误文案。
type Category string

const (
	CategorySchema      Category = "schema"
	CategoryPermission  Category = "permission"
	CategoryCapacity    Category = "capacity"
	CategoryCorrupt     Category = "corrupt"
	CategoryConflict    Category = "conflict"
	CategoryUnavailable Category = "unavailable"
)

// Error 保留操作与路径用于本机诊断；面向微信的展示层只能使用 Category。
type Error struct {
	Category Category
	Op       string
	Path     string
	Err      error
}

func (err *Error) Error() string {
	if err.Path == "" {
		return fmt.Sprintf("state %s (%s): %v", err.Op, err.Category, err.Err)
	}
	return fmt.Sprintf("state %s %s (%s): %v", err.Op, err.Path, err.Category, err.Err)
}

func (err *Error) Unwrap() error { return err.Err }

// ErrorCategory 将任意错误收敛为统一分类。
func ErrorCategory(err error) Category {
	var stateErr *Error
	if errors.As(err, &stateErr) {
		return stateErr.Category
	}
	return classifySystemError(err)
}

func wrap(category Category, op, path string, err error) error {
	if err == nil {
		return nil
	}
	failureHealth.Lock()
	failureHealth.last = FailureSnapshot{Category: category, At: time.Now()}
	failureHealth.Unlock()
	return &Error{Category: category, Op: op, Path: path, Err: err}
}

// LastFailure 只暴露稳定分类和时间；路径、操作和底层错误不得进入远程诊断。
func LastFailure() (FailureSnapshot, bool) {
	failureHealth.RLock()
	defer failureHealth.RUnlock()
	return failureHealth.last, failureHealth.last.Category != ""
}

// ClearLastFailure 在运行时完成全部启动校验后建立新的诊断基线。
func ClearLastFailure() {
	failureHealth.Lock()
	failureHealth.last = FailureSnapshot{}
	failureHealth.Unlock()
}

func wrapSystem(op, path string, err error) error {
	return wrap(classifySystemError(err), op, path, err)
}

func classifySystemError(err error) Category {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, fs.ErrPermission), errors.Is(err, syscall.EACCES), errors.Is(err, syscall.EPERM), errors.Is(err, syscall.ELOOP):
		return CategoryPermission
	case errors.Is(err, syscall.ENOSPC), errors.Is(err, syscall.EDQUOT), errors.Is(err, syscall.EFBIG):
		return CategoryCapacity
	case errors.Is(err, syscall.EWOULDBLOCK), errors.Is(err, syscall.EAGAIN), errors.Is(err, syscall.EBUSY):
		return CategoryConflict
	default:
		return CategoryUnavailable
	}
}
