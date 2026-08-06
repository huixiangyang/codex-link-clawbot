package messaging

import (
	"crypto/subtle"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/huixiangyang/weclaw/internal/statefile"
)

const remoteLockVersion = 1

type remoteLockFile struct {
	Version int             `json:"version"`
	Owners  map[string]bool `json:"owners"`
}

type RemoteLock struct {
	mu    sync.RWMutex
	path  string
	code  string
	state remoteLockFile
}

func NewRemoteLock(path, code string) (*RemoteLock, error) {
	if strings.TrimSpace(path) == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		path = filepath.Join(home, ".weclaw", "remote-lock.json")
	}
	lock := &RemoteLock{path: path, code: code, state: remoteLockFile{Version: remoteLockVersion, Owners: make(map[string]bool)}}
	found, err := statefile.ReadJSON(path, &lock.state, statefile.Options{
		Validate: func() error { return validateRemoteLockState(lock.state) },
	})
	if err != nil {
		return nil, fmt.Errorf("load remote lock: %w", err)
	}
	if !found {
		return lock, nil
	}
	if code == "" {
		for _, locked := range lock.state.Owners {
			if locked {
				return nil, fmt.Errorf("remote lock code cannot be removed while an owner is locked")
			}
		}
	}
	return lock, nil
}

func (l *RemoteLock) Enabled() bool { return l != nil && l.code != "" }

func (l *RemoteLock) IsLocked(ownerID string) bool {
	if l == nil {
		return false
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.state.Owners[ownerID]
}

func (l *RemoteLock) Lock(ownerID string) error {
	if !l.Enabled() {
		return fmt.Errorf("remote lock code is not configured")
	}
	return l.set(ownerID, true)
}

func (l *RemoteLock) Unlock(ownerID, code string) error {
	if !l.Enabled() {
		return fmt.Errorf("remote lock code is not configured")
	}
	if subtle.ConstantTimeCompare([]byte(code), []byte(l.code)) != 1 {
		return fmt.Errorf("unlock code is incorrect")
	}
	return l.set(ownerID, false)
}

func (l *RemoteLock) set(ownerID string, locked bool) error {
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return fmt.Errorf("owner id is required")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	previous := l.state.Owners[ownerID]
	if locked {
		l.state.Owners[ownerID] = true
	} else {
		delete(l.state.Owners, ownerID)
	}
	if err := l.saveLocked(); err != nil {
		if previous {
			l.state.Owners[ownerID] = true
		} else {
			delete(l.state.Owners, ownerID)
		}
		return err
	}
	return nil
}

func (l *RemoteLock) saveLocked() error {
	return statefile.WriteJSON(l.path, l.state, statefile.Options{
		Validate: func() error { return validateRemoteLockState(l.state) },
	})
}

func validateRemoteLockState(state remoteLockFile) error {
	if state.Version != remoteLockVersion || state.Owners == nil {
		return fmt.Errorf("invalid remote lock schema")
	}
	for ownerID, locked := range state.Owners {
		if strings.TrimSpace(ownerID) == "" || !locked {
			return fmt.Errorf("invalid remote lock owner")
		}
	}
	return nil
}

func (h *Handler) lockRemote(userID string) string {
	if h.remoteLock == nil || !h.remoteLock.Enabled() {
		return "远程锁定未配置。请先在 security.remote_lock_code 设置解锁码并重启服务。"
	}
	h.deleteControlState(userID)
	queueWasPaused := false
	if h.tasks != nil {
		queueWasPaused = h.tasks.Status(userID).Paused
		if err := h.tasks.SetPaused(userID, true); err != nil {
			return fmt.Sprintf("远程锁定失败：无法暂停 WeClaw 请求队列：%v", err)
		}
	}
	if err := h.remoteLock.Lock(userID); err != nil {
		if h.tasks != nil && !queueWasPaused {
			_ = h.tasks.SetPaused(userID, false)
		}
		return fmt.Sprintf("远程锁定失败：%v", err)
	}
	if h.coordinator != nil {
		h.coordinator.Cancel(userID)
	}
	return "WeClaw 已远程锁定。后续消息和附件不会进入 Codex。发送“解锁 解锁码”恢复。"
}

func (h *Handler) confirmRemoteLock(userID string) string {
	if h.remoteLock == nil || !h.remoteLock.Enabled() {
		return "远程锁定未配置。请先在 security.remote_lock_code 设置解锁码并重启服务。"
	}
	if h.remoteLock.IsLocked(userID) {
		return "WeClaw 已处于远程锁定。发送“解锁 解锁码”恢复。"
	}
	options := []controlOption{{Code: "1", Label: "确认远程锁定", Action: actionRemoteLock}}
	if !h.storeChoiceWithBack(userID, viewSecurityLockConfirm, options, controlOption{Action: actionMain}) {
		return controlStateFailureResult().Text
	}
	return "准备远程锁定\n\n锁定会取消 WeClaw 当前执行、暂停请求队列，并阻止后续内容进入 Codex。\n\n" + renderControlOptions(options) + "\n\n回复 1 确认，0 返回操作总览。"
}

func (h *Handler) handleLockedInput(userID, text string) string {
	argument, matched := intentArgument(text, []string{"解锁"})
	if !matched || strings.TrimSpace(argument) == "" {
		return "WeClaw 当前已锁定。发送“解锁 解锁码”恢复。"
	}
	// 解锁码按字面比较，不能套用会话名称中的“改为/叫做”等自然语言清洗。
	code := strings.Trim(strings.TrimSpace(argument), " \t\r\n：:，,。\"“”")
	if err := h.remoteLock.Unlock(userID, code); err != nil {
		return "解锁失败：解锁码不正确。"
	}
	return "WeClaw 已解锁。请求队列仍保持暂停；发送“继续队列”后才会恢复执行。"
}
